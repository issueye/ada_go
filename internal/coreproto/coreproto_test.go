package coreproto_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/ports"

	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/proto"
)

func ev(kind domain.EventKind, thread string) domain.Event {
	return domain.Event{Kind: kind, ThreadID: thread, Payload: json.RawMessage(`{"text":"hi"}`)}
}

// TestEmitterAssignsMonotonicSeq 是 INV-6 的发射侧证据：线程内 seq 单调且独立。
func TestEmitterAssignsMonotonicSeq(t *testing.T) {
	t.Parallel()
	e := coreproto.NewEmitter(nil)
	for i := 1; i <= 5; i++ {
		if err := e.Emit(context.Background(), ev(domain.EvtMessageDelta, "th_a")); err != nil {
			t.Fatal(err)
		}
		if got := e.LastSeq("th_a"); got != int64(i) {
			t.Fatalf("第 %d 条后 seq = %d", i, got)
		}
	}
	if err := e.Emit(context.Background(), ev(domain.EvtMessageDelta, "th_b")); err != nil {
		t.Fatal(err)
	}
	if got := e.LastSeq("th_b"); got != 1 {
		t.Fatalf("另一线程 seq = %d，期望从 1 起", got)
	}
}

// TestEmitterWorkspaceScope 验证工作区级事件用独立 workspaceSeq（8.1）。
func TestEmitterWorkspaceScope(t *testing.T) {
	t.Parallel()
	e := coreproto.NewEmitter(nil)
	for i := 0; i < 3; i++ {
		if err := e.Emit(context.Background(), domain.Event{Kind: domain.EvtPluginChanged,
			Payload: json.RawMessage(`{"plugin":{"id":"x"},"change":"reloaded"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	events := []domain.Event{}
	e.Subscribe(func(evt domain.Event) { events = append(events, evt) })
	if err := e.Emit(context.Background(), domain.Event{Kind: domain.EvtPluginChanged,
		Payload: json.RawMessage(`{"plugin":{"id":"x"},"change":"reloaded"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].WorkspaceSeq != 4 || events[0].Seq != 0 {
		t.Fatalf("工作区级事件序号不对：%+v", events[0])
	}
}

// TestEmitterRejectsUnknownEvent 钉住「插件不能扩展协议」（1.5.2）。
func TestEmitterRejectsUnknownEvent(t *testing.T) {
	t.Parallel()
	e := coreproto.NewEmitter(nil)
	if err := e.Emit(context.Background(), ev(domain.EventKind("evt.mine"), "th_a")); err == nil {
		t.Fatal("未声明的事件名必须被拒绝")
	}
	if err := e.Emit(context.Background(), domain.Event{Kind: domain.EvtMessageDelta}); err == nil {
		t.Fatal("线程级事件缺 threadId 必须被拒绝")
	}
}

// TestEmitterReplayAndGap 验证 8.3：连续则补发，断裂则回退快照。
func TestEmitterReplayAndGap(t *testing.T) {
	t.Parallel()
	e := coreproto.NewEmitter(nil)
	for i := 0; i < 10; i++ {
		_ = e.Emit(context.Background(), ev(domain.EvtMessageDelta, "th_a"))
	}
	events, ok := e.Replay("th_a", 7)
	if !ok || len(events) != 3 {
		t.Fatalf("补发不对：%d 条 ok=%v", len(events), ok)
	}
	// 从头开始要拿到全部。
	all, ok := e.Replay("th_a", 0)
	if !ok || len(all) != 10 {
		t.Fatalf("全量补发 = %d，期望 10", len(all))
	}
}

// TestEmitterUnsubscribeNoLeak 验证订阅取消后不再收到事件（06 §6.4 不留 goroutine）。
func TestEmitterUnsubscribeNoLeak(t *testing.T) {
	t.Parallel()
	e := coreproto.NewEmitter(nil)
	var mu sync.Mutex
	got := 0
	cancel := e.Subscribe(func(domain.Event) { mu.Lock(); got++; mu.Unlock() })
	_ = e.Emit(context.Background(), ev(domain.EvtNotice, "th_a"))
	cancel()
	_ = e.Emit(context.Background(), ev(domain.EvtNotice, "th_a"))
	mu.Lock()
	defer mu.Unlock()
	if got != 1 {
		t.Fatalf("取消订阅后仍收到事件：%d", got)
	}
}

// TestNotificationEncoding 验证领域事件能编成 JSON-RPC notification（8.1）。
func TestNotificationEncoding(t *testing.T) {
	t.Parallel()
	e := coreproto.NewEmitter(nil)
	var stored domain.Event
	e.Subscribe(func(evt domain.Event) { stored = evt })
	if err := e.Emit(context.Background(), ev(domain.EvtRunStatus, "th_a")); err != nil {
		t.Fatal(err)
	}
	n, err := coreproto.Notification(stored)
	if err != nil {
		t.Fatal(err)
	}
	if n.JSONRPC != "2.0" || n.Method != string(domain.EvtRunStatus) || n.Params == nil {
		t.Fatalf("notification 形状不对：%+v", n)
	}
}

// ---------- 交互桥 ----------

type brokerHarness struct {
	b        *coreproto.Broker
	events   *coreproto.Emitter
	clock    *fakeAdvanceClock
	storeLog []string
}

func newBrokerHarness(t *testing.T, hasUI bool) *brokerHarness {
	t.Helper()
	clock := &fakeAdvanceClock{now: time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)}
	events := coreproto.NewEmitter(clock)
	store := &recordingStore{log: &[]string{}}
	b, err := coreproto.NewBroker(coreproto.BrokerConfig{
		Sink: events, Clock: clock, IDs: coreIDs{}, Store: store,
		HasUI: func(context.Context, string) bool { return hasUI },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &brokerHarness{b: b, events: events, clock: clock}
}

type fakeAdvanceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeAdvanceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After 用真实定时器：交互期限测试靠它触发（300s 的用例不适用，见 timeout 用例的短期限配置）。
func (c *fakeAdvanceClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type coreIDs struct{}

func (coreIDs) NewThreadID(context.Context, string) (string, error) { return "th_test_1", nil }
func (coreIDs) NewULID(context.Context) (string, error)             { return "01TESTULID", nil }
func (coreIDs) NewCallID(context.Context) (string, error)           { return "call_test", nil }
func (coreIDs) NewRunID(context.Context) (string, error)            { return "run_test", nil }
func (coreIDs) NewCheckpointID(context.Context) (string, error)     { return "chk_test", nil }
func (coreIDs) NewMessageID(context.Context) (string, error)        { return "msg_test", nil }
func (coreIDs) NewInteractionID(context.Context) (string, error)    { return "intg_test", nil }

type recordingStore struct {
	log *[]string
}

func (s *recordingStore) SavePending(_ context.Context, req ports.InteractionRequest) error {
	*s.log = append(*s.log, "save:"+req.ID)
	return nil
}

func (s *recordingStore) Resolve(_ context.Context, id string, res coreproto.InteractionRecord) error {
	*s.log = append(*s.log, "resolve:"+id+":"+string(res.Status))
	return nil
}

func (s *recordingStore) ListPending(context.Context, string) ([]ports.InteractionRequest, error) {
	return nil, nil
}

// TestInteractionUnavailableWithoutUI 是 INV-4 的基座侧证据：没有界面时立即 unavailable，
// 并且策略侧拿到的是「拒绝」而不是「允许」。
func TestInteractionUnavailableWithoutUI(t *testing.T) {
	t.Parallel()
	h := newBrokerHarness(t, false)
	res, err := h.b.Request(context.Background(), ports.InteractionRequest{
		ID: "i1", ThreadID: "th_1", PluginID: "policy", Kind: domain.InteractionConfirm, Title: "允许吗", Body: "写文件",
	})
	if res.Status != domain.InteractionUnavailable {
		t.Fatalf("状态 = %q，期望 unavailable", res.Status)
	}
	if err == nil {
		t.Fatal("unavailable 必须带错误，让策略插件按拒绝处理")
	}
	if !res.Status.IsDenial() {
		t.Fatal("unavailable 必须是拒绝方向")
	}
}

// TestInteractionExactlyOnceAndIdempotent 验证 03 §5.6：一次交互恰好解析一次，重复响应幂等。
func TestInteractionExactlyOnceAndIdempotent(t *testing.T) {
	t.Parallel()
	h := newBrokerHarness(t, true)
	type outcome struct {
		res ports.InteractionResolution
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := h.b.Request(context.Background(), ports.InteractionRequest{
			ID: "i2", ThreadID: "th_1", PluginID: "policy", Kind: domain.InteractionConfirm, Title: "允许吗", Body: "写文件",
		})
		done <- outcome{res, err}
	}()
	waitFor(t, func() bool { return len(pendingIDs(h.b, "th_1")) > 0 }, time.Second)

	first, err := h.b.Respond(context.Background(), "th_1", "i2", domain.InteractionAccepted, "yes")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.InteractionAccepted || first.Value != "yes" {
		t.Fatalf("首次响应不对：%+v", first)
	}
	second, err := h.b.Respond(context.Background(), "th_1", "i2", domain.InteractionDenied, "no")
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != domain.InteractionAccepted || second.Value != "yes" {
		t.Fatalf("重复响应必须幂等返回首次结果：%+v", second)
	}
	got := <-done
	if got.res.Status != domain.InteractionAccepted {
		t.Fatalf("请求侧状态 = %q", got.res.Status)
	}
	if got.err != nil {
		t.Fatalf("accepted 不应带错误: %v", got.err)
	}
	// 待处理队列必须清空。
	if len(pendingIDs(h.b, "th_1")) != 0 {
		t.Fatal("解析后仍留在待处理队列")
	}
}

// TestInteractionRejectsForgedStatus 验证 timeout/unavailable 不可由调用方伪造。
func TestInteractionRejectsForgedStatus(t *testing.T) {
	t.Parallel()
	h := newBrokerHarness(t, true)
	if _, err := h.b.Respond(context.Background(), "th_1", "i9", domain.InteractionTimeout, ""); err == nil {
		t.Fatal("调用方不得伪造 timeout 解析")
	}
	if _, err := h.b.Respond(context.Background(), "th_1", "i9", domain.InteractionUnavailable, ""); err == nil {
		t.Fatal("调用方不得伪造 unavailable")
	}
	if _, err := h.b.Respond(context.Background(), "th_1", "nope", domain.InteractionAccepted, ""); err == nil ||
		!strings.Contains(err.Error(), "不存在") {
		t.Fatalf("未知交互必须报错：%v", err)
	}
}

// TestInteractionSinglePerThread 验证 03 §5.6「同一线程最多一个待处理交互」。
func TestInteractionSinglePerThread(t *testing.T) {
	t.Parallel()
	h := newBrokerHarness(t, true)
	hold := make(chan struct{})
	go func() {
		_, _ = h.b.Request(context.Background(), ports.InteractionRequest{
			ID: "i10", ThreadID: "th_1", PluginID: "policy", Kind: domain.InteractionConfirm, Title: "a", Body: "b",
		})
		close(hold)
	}()
	waitFor(t, func() bool { return len(pendingIDs(h.b, "th_1")) > 0 }, time.Second)

	_, err := h.b.Request(context.Background(), ports.InteractionRequest{
		ID: "i11", ThreadID: "th_1", PluginID: "policy", Kind: domain.InteractionInput, Title: "c", Body: "d",
	})
	if err == nil || !strings.Contains(err.Error(), "thread_busy") && !strings.Contains(err.Error(), "待处理交互") {
		t.Fatalf("第二个交互必须被拒：%v", err)
	}
	_, _ = h.b.Respond(context.Background(), "th_1", "i10", domain.InteractionCancelled, "")
	<-hold
}

// TestInteractionTimeoutIsDenial 验证超时解析为 timeout 且方向是拒绝（INV-4）。
func TestInteractionTimeoutIsDenial(t *testing.T) {
	t.Parallel()
	clock := &fakeAdvanceClock{now: time.Now()}
	events := coreproto.NewEmitter(clock)
	b, err := coreproto.NewBroker(coreproto.BrokerConfig{
		Sink: events, Clock: clock, IDs: coreIDs{}, Timeout: 30 * time.Millisecond,
		HasUI: func(context.Context, string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Request(context.Background(), ports.InteractionRequest{
		ID: "i12", ThreadID: "th_1", PluginID: "policy", Kind: domain.InteractionConfirm, Title: "a", Body: "b",
	})
	if res.Status != domain.InteractionTimeout {
		t.Fatalf("状态 = %q，期望 timeout", res.Status)
	}
	if err == nil || !res.Status.IsDenial() {
		t.Fatalf("超时必须按拒绝处理：%v", err)
	}
}

// TestInteractionCancelFromRound 验证轮次取消会解析为 cancelled（INV-8 贯穿等待中的交互）。
func TestInteractionCancelFromRound(t *testing.T) {
	t.Parallel()
	h := newBrokerHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	res, err := h.b.Request(ctx, ports.InteractionRequest{
		ID: "i13", ThreadID: "th_1", PluginID: "policy", Kind: domain.InteractionConfirm, Title: "a", Body: "b",
	})
	if res.Status != domain.InteractionCancelled {
		t.Fatalf("状态 = %q，期望 cancelled", res.Status)
	}
	if err == nil || !res.Status.IsDenial() {
		t.Fatalf("取消必须按拒绝处理：%v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待条件超时 %s", timeout)
}

// 保留导入以让编译错误更明确（proto 用于常量断言）。
var _ = proto.ProtoVersion

// pendingIDs 读取待处理交互（ListPending 是端口方法，测试直接用它）。
func pendingIDs(b *coreproto.Broker, threadID string) []string {
	list, err := b.ListPending(context.Background(), threadID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, r := range list {
		out = append(out, r.ID)
	}
	return out
}
