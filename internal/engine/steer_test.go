package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/domain"
)

// steeringStub 是 engine.SteeringSource 的脚本替身：按次返回预置批次，
// 并记录引擎在哪些线程上调用了安全点。
type steeringStub struct {
	mu      sync.Mutex
	batches [][]domain.Message
	drains  []string
}

func newSteeringStub(msgs ...domain.Message) *steeringStub {
	return &steeringStub{batches: [][]domain.Message{msgs}}
}

func (s *steeringStub) DrainSteering(_ context.Context, threadID string) []domain.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drains = append(s.drains, threadID)
	if len(s.batches) == 0 {
		return nil
	}
	out := s.batches[0]
	s.batches = s.batches[1:]
	return out
}

func (s *steeringStub) drainCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.drains)
}

func (s *steeringStub) remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.batches {
		n += len(b)
	}
	return n
}

// TestSteeringInjectedAfterToolBatch 钉住安全点：工具批次与回执 entry 之后、
// 下一次模型请求之前排空 steering，并把消息作为 user entry 追加。
func TestSteeringInjectedAfterToolBatch(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_1", "read_file", `{"path":"a.go"}`),
		testkit.TextTurn("按你的补充改用 b.go"),
	)
	h := newHarness(t, model, testkit.Descriptor("read_file", "tools-fs", domain.AccessRead))
	steer := newSteeringStub(domain.Message{
		Role:  domain.RoleUser,
		Parts: []domain.Part{{Type: domain.PartText, Text: "改用 b.go"}},
	})
	h.deps.Steer = steer
	th, err := h.store.CreateThread(context.Background(), h.workspace, "steer")
	if err != nil {
		t.Fatalf("建线程失败: %v", err)
	}

	if got := h.run(t, context.Background(), th.ID, "读 a.go"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q，期望 completed", got)
	}

	entries := h.store.Entries(th.ID)
	// user(初始), assistant(tool_call), tool(回执), user(steer), assistant(收束)
	if len(entries) != 5 {
		t.Fatalf("entry 数 = %d，期望 5：%+v", len(entries), entries)
	}
	steerEntry := entries[3]
	if steerEntry.Kind != domain.EntryMessage || steerEntry.Role != domain.RoleUser {
		t.Fatalf("steering entry 形状不对: kind=%s role=%s", steerEntry.Kind, steerEntry.Role)
	}
	if steerEntry.ParentID != entries[2].ID {
		t.Fatalf("steering entry parent = %q，期望工具回执 %q", steerEntry.ParentID, entries[2].ID)
	}
	if steerEntry.Seq <= entries[2].Seq {
		t.Fatalf("steering seq = %d，应晚于工具回执 seq=%d", steerEntry.Seq, entries[2].Seq)
	}
	if !strings.Contains(string(steerEntry.Payload), "改用 b.go") {
		t.Fatalf("steering entry 载荷 = %s", steerEntry.Payload)
	}

	reqs := h.provider.Requests()
	if len(reqs) != 2 {
		t.Fatalf("模型请求 = %d，期望 2", len(reqs))
	}
	if messagesContain(reqs[0].Messages, "改用 b.go") {
		t.Fatal("steering 消息出现在工具批次之前的模型请求里，安全点位置错误")
	}
	if !messagesContain(reqs[1].Messages, "改用 b.go") {
		t.Fatalf("steering 消息没有进入下一次模型请求: %+v", reqs[1].Messages)
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != domain.RoleUser {
		t.Fatalf("steering 消息 role = %q，期望 user", last.Role)
	}

	if got := steer.drainCount(); got != 1 {
		t.Fatalf("DrainSteering 调用次数 = %d，期望 1", got)
	}
	if err := h.events.ExactlyOneTerminal(); err != nil {
		t.Fatal(err)
	}
	if err := h.events.SeqMonotonic(); err != nil {
		t.Fatal(err)
	}
	if !hasItemUpdateFor(h, "改用 b.go") {
		t.Fatal("缺少 steering user entry 的 item.updated 事件")
	}
}

// TestSteeringNotInjectedWithoutToolBatch：模型直接自然收束时没有安全点，
// 队列必须留在下一轮，不能强插到本轮。
func TestSteeringNotInjectedWithoutToolBatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testkit.NewScriptedModel(testkit.TextTurn("直接回答")))
	steer := newSteeringStub(domain.Message{
		Role:  domain.RoleUser,
		Parts: []domain.Part{{Type: domain.PartText, Text: "这条应该留到下一轮"}},
	})
	h.deps.Steer = steer
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "no-tools")

	if got := h.run(t, context.Background(), th.ID, "你好"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q，期望 completed", got)
	}
	if n := len(h.store.Entries(th.ID)); n != 2 {
		t.Fatalf("entry 数 = %d，期望 2（user+assistant，不被 steering 强插）", n)
	}
	if got := steer.drainCount(); got != 0 {
		t.Fatalf("没有工具批次却调用了 DrainSteering %d 次", got)
	}
	if got := steer.remaining(); got != 1 {
		t.Fatalf("队列应原样保留，剩余 = %d", got)
	}
	if len(h.provider.Requests()) != 1 {
		t.Fatalf("模型请求 = %d，期望 1", len(h.provider.Requests()))
	}
	if err := h.events.ExactlyOneTerminal(); err != nil {
		t.Fatal(err)
	}
}

// TestSteeringDrainsEveryToolBatch：同一轮内每个工具批次后都能排空一次。
func TestSteeringDrainsEveryToolBatch(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_1", "read_file", `{"path":"a.go"}`),
		testkit.ToolTurn("call_2", "read_file", `{"path":"b.go"}`),
		testkit.TextTurn("两批工具后收束"),
	)
	h := newHarness(t, model, testkit.Descriptor("read_file", "tools-fs", domain.AccessRead))
	steer := &steeringStub{batches: [][]domain.Message{
		{{Role: domain.RoleUser, Parts: []domain.Part{{Type: domain.PartText, Text: "第一批插话"}}}},
		{{Role: domain.RoleUser, Parts: []domain.Part{{Type: domain.PartText, Text: "第二批插话"}}}},
	}}
	h.deps.Steer = steer
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "multi-steer")

	if got := h.run(t, context.Background(), th.ID, "读两个文件"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q，期望 completed", got)
	}
	if got := steer.drainCount(); got != 2 {
		t.Fatalf("DrainSteering 调用次数 = %d，期望 2（每个工具批次一次）", got)
	}
	reqs := h.provider.Requests()
	if len(reqs) != 3 {
		t.Fatalf("模型请求 = %d，期望 3", len(reqs))
	}
	if messagesContain(reqs[0].Messages, "第一批插话") {
		t.Fatal("第一批插话出现在首个请求之前")
	}
	if !messagesContain(reqs[1].Messages, "第一批插话") || messagesContain(reqs[1].Messages, "第二批插话") {
		t.Fatalf("第二个请求的消息窗口不对: %+v", reqs[1].Messages)
	}
	if !messagesContain(reqs[2].Messages, "第一批插话") || !messagesContain(reqs[2].Messages, "第二批插话") {
		t.Fatalf("第三个请求应包含两条插话: %+v", reqs[2].Messages)
	}
	if err := h.events.ExactlyOneTerminal(); err != nil {
		t.Fatal(err)
	}
}

func messagesContain(messages []domain.Message, text string) bool {
	raw, err := json.Marshal(messages)
	return err == nil && strings.Contains(string(raw), text)
}

func hasItemUpdateFor(h *harness, text string) bool {
	for _, evt := range h.events.Filter(domain.EvtItemUpdated) {
		if strings.Contains(string(evt.Payload), "msg:") && strings.Contains(string(evt.Payload), text) {
			return true
		}
	}
	return false
}
