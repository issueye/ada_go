package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/proto"
)

// GoldenUpdateEnv 置为 1/true 时，Golden() 重写基线文件而不是比对
// （等价于 -update 旗标；用环境变量是为了不在非测试包里注册 flag）。
const GoldenUpdateEnv = "ADAG_GOLDEN_UPDATE"

// DefaultGoldenDir 是 golden 基线的约定位置（相对 internal/conformance 的测试工作目录）。
var DefaultGoldenDir = filepath.Join("testdata", "golden")

// RecordingEventSink 是 ports.EventSink 的测试替身：记录事件序列并提供断言辅助。
//
// 守的不变量：
//   - INV-6（02 §5.3）：线程内 Seq 单调递增；ToolCallStarted 必先于同 callId 的
//     ToolCallFinished —— 分别由 SeqMonotonic 与 StartedBeforeFinished 断言；
//   - INV-9：terminal 状态每轮恰好一次，错误路径也不能漏发/重发 —— ExactlyOneTerminal；
//   - INV-10：evt.notice 与工具 details 永不进模型 —— 替身不参与上下文重建，
//     但 Golden 把事件序列固定下来，任何「notice 变成 message.delta」的漂移都会在
//     golden 比对里失败；
//   - 08 §8.1/§8.3：工作区级事件用独立 WorkspaceSeq（scope 取自生成的 proto.Events，
//     不在测试替身里另立一套表）。
//
// 序号补齐规则照 ports/eventsink.go：Emit 的实现方负责在「写协程」内分配 Seq，
// 因此调用方留空时由本替身分配；已带序号的原样保留（用于回放既有事件）。
type RecordingEventSink struct {
	mu           sync.Mutex
	events       []domain.Event
	seqByThread  map[string]int64
	workspaceSeq int64
	goldenDir    string
	// Strict 为 true 时拒绝未知事件名与「线程级事件缺 threadId」（默认 true）。
	Strict bool
}

// SinkOption 是构造选项。
type SinkOption func(*RecordingEventSink)

// WithGoldenDir 指定 golden 基线目录。
func WithGoldenDir(dir string) SinkOption {
	return func(s *RecordingEventSink) { s.goldenDir = dir }
}

// WithRelaxed 关闭严格校验（回放畸形事件时用）。
func WithRelaxed() SinkOption {
	return func(s *RecordingEventSink) { s.Strict = false }
}

// NewRecordingEventSink 构造记录器。
func NewRecordingEventSink(opts ...SinkOption) *RecordingEventSink {
	s := &RecordingEventSink{
		seqByThread: make(map[string]int64),
		goldenDir:   DefaultGoldenDir,
		Strict:      true,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Emit 实现 ports.EventSink。
func (s *RecordingEventSink) Emit(_ context.Context, evt domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := string(evt.Kind)
	meta, known := proto.LookupEvent(name)
	if s.Strict && !known {
		return domain.NewError(domain.CodeProtocolError,
			fmt.Sprintf("未知事件名 %q（不在 proto.AllEventNames() 里）", name))
	}
	workspaceScope := known && meta.Scope == "workspace"
	if s.Strict && !workspaceScope && evt.ThreadID == "" && known {
		return domain.NewError(domain.CodeProtocolError,
			fmt.Sprintf("线程级事件 %q 缺少 threadId", name))
	}

	out := evt
	if out.At.IsZero() {
		out.At = FrozenTime
	}
	if workspaceScope {
		if out.WorkspaceSeq == 0 {
			s.workspaceSeq++
			out.WorkspaceSeq = s.workspaceSeq
		}
	} else {
		if out.Seq == 0 {
			s.seqByThread[out.ThreadID]++
			out.Seq = s.seqByThread[out.ThreadID]
		} else if n, ok := s.seqByThread[out.ThreadID]; ok && out.Seq > n {
			s.seqByThread[out.ThreadID] = out.Seq
		} else if !ok {
			s.seqByThread[out.ThreadID] = out.Seq
		}
	}
	s.events = append(s.events, out)
	return nil
}

// Events 返回事件快照（按写入顺序）。
func (s *RecordingEventSink) Events() []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Event(nil), s.events...)
}

// Len 返回事件条数。
func (s *RecordingEventSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// Kinds 返回事件名序列（golden 之外的粗粒度断言）。
func (s *RecordingEventSink) Kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, e := range s.events {
		out = append(out, string(e.Kind))
	}
	return out
}

// Filter 返回某类事件的副本。
func (s *RecordingEventSink) Filter(kind domain.EventKind) []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Event
	for _, e := range s.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// SeqMonotonic 断言 INV-6：每个线程的 Seq 严格递增，工作区级 Seq 同样严格递增。
// 返回 nil 表示成立；否则返回描述第一处违例的错误。
func (s *RecordingEventSink) SeqMonotonic() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lastThread := map[string]int64{}
	var lastWS int64
	for _, e := range s.events {
		if e.Seq != 0 {
			prev, seen := lastThread[e.ThreadID]
			if seen && e.Seq <= prev {
				return fmt.Errorf("testkit: 线程 %s 的 seq 非单调：%d 出现在 %d 之后（事件 %s）",
					e.ThreadID, e.Seq, prev, e.Kind)
			}
			lastThread[e.ThreadID] = e.Seq
		}
		if e.WorkspaceSeq != 0 {
			if e.WorkspaceSeq <= lastWS {
				return fmt.Errorf("testkit: workspaceSeq 非单调：%d 出现在 %d 之后（事件 %s）",
					e.WorkspaceSeq, lastWS, e.Kind)
			}
			lastWS = e.WorkspaceSeq
		}
	}
	return nil
}

// TerminalCount 返回 evt.run.status 中 status=terminal 的条数。
func (s *RecordingEventSink) TerminalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.Kind == domain.EvtRunStatus && runStatusOf(e) == string(domain.RunTerminal) {
			n++
		}
	}
	return n
}

// ExactlyOneTerminal 断言 INV-9：恰好一个 TurnFinished（terminal 状态）。
func (s *RecordingEventSink) ExactlyOneTerminal() error {
	n := s.TerminalCount()
	switch {
	case n == 0:
		return fmt.Errorf("testkit: 一个终态都没有发出（INV-9 要求恰好一个 evt.run.status=terminal）")
	case n > 1:
		return fmt.Errorf("testkit: 发出了 %d 个终态（INV-9 要求恰好一个）", n)
	}
	return nil
}

// StartedBeforeFinished 断言 INV-6 的后半句：同一 callId 的工具卡片，
// started 语义必须先于 finished 语义。工具卡的载体是 evt.item.updated，
// 载荷键是 ItemKey `tool:<callId>`（08 §8.3），状态取 started/running/finished/error/… 之一。
func (s *RecordingEventSink) StartedBeforeFinished(callID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(domain.ItemKey("tool:" + callID))
	startedAt, finishedAt := -1, -1
	for i, e := range s.events {
		if e.Kind != domain.EvtItemUpdated {
			continue
		}
		if itemKeyOf(e) != key {
			continue
		}
		switch strings.ToLower(toolCardStatus(e)) {
		case "started", "running", "waiting_input":
			if startedAt < 0 {
				startedAt = i
			}
		case "finished", "ok", "error", "denied", "timeout", "interrupted":
			if finishedAt < 0 {
				finishedAt = i
			}
		}
	}
	switch {
	case startedAt < 0 && finishedAt < 0:
		return fmt.Errorf("testkit: 没有 callId %q 的任何工具卡事件", callID)
	case startedAt < 0:
		return fmt.Errorf("testkit: callId %q 只有完成事件、没有 started（INV-6）", callID)
	case finishedAt < 0:
		return fmt.Errorf("testkit: callId %q 只有 started、没有完成事件（INV-6）", callID)
	case finishedAt < startedAt:
		return fmt.Errorf("testkit: callId %q 的完成事件出现在 started 之前（INV-6）", callID)
	}
	return nil
}

// Text 渲染 golden 文本：序号 | 事件名 | 线程 | 紧凑载荷（不含时间戳，保证可重放比对）。
func (s *RecordingEventSink) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, e := range s.events {
		seq := e.Seq
		scope := "t"
		if e.WorkspaceSeq != 0 {
			seq = e.WorkspaceSeq
			scope = "ws"
		}
		fmt.Fprintf(&b, "%s#%d\t%s\t%s\t%s\n", scope, seq, e.Kind, e.ThreadID, compact(e.Payload))
	}
	return b.String()
}

// Golden 与 testdata/golden/<name>.txt 比对；基线缺失或 ADAG_GOLDEN_UPDATE=1 时写入基线。
// 比对失败时返回的错误里带 unified-ish 差异（前 12 行不同处）。
func (s *RecordingEventSink) Golden(name string) error {
	got := s.Text()
	path := filepath.Join(s.goldenDir, name+".txt")
	want, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("testkit: 读取 golden %s: %w", path, err)
	}
	if os.IsNotExist(err) || updatingGolden() {
		if mkErr := os.MkdirAll(s.goldenDir, 0o755); mkErr != nil {
			return fmt.Errorf("testkit: 创建 golden 目录: %w", mkErr)
		}
		if wErr := os.WriteFile(path, []byte(got), 0o644); wErr != nil {
			return fmt.Errorf("testkit: 写 golden %s: %w", path, wErr)
		}
		if !os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("testkit: golden 基线不存在，已生成 %s（复核内容后再提交）", path)
	}
	if string(want) == got {
		return nil
	}
	return fmt.Errorf("testkit: golden %s 不匹配\n%s", path, diffLines(string(want), got))
}

func updatingGolden() bool {
	switch strings.ToLower(os.Getenv(GoldenUpdateEnv)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// diffLines 给出前 12 处行级差异。
func diffLines(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := len(wl)
	if len(gl) > n {
		n = len(gl)
	}
	var b strings.Builder
	shown := 0
	for i := 0; i < n && shown < 12; i++ {
		w, g := "", ""
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w == g {
			continue
		}
		fmt.Fprintf(&b, "  第 %d 行 want=%q got=%q\n", i+1, w, g)
		shown++
	}
	fmt.Fprintf(&b, "  （want %d 行 / got %d 行）", len(wl), len(gl))
	return b.String()
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

func payloadField(e domain.Event, key string) string {
	var m map[string]any
	if len(e.Payload) == 0 || json.Unmarshal(e.Payload, &m) != nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func runStatusOf(e domain.Event) string    { return payloadField(e, "status") }
func itemKeyOf(e domain.Event) string      { return payloadField(e, "key") }
func toolCardStatus(e domain.Event) string { return payloadField(e, "status") }

// ---------------------------------------------------------------------------
// 事件夹具构造器（M1 的引擎测试与 golden 回放共用）
// ---------------------------------------------------------------------------

// RunStatusEvent 造一条 evt.run.status（terminal 时必须带 reason，02 §5.3）。
func RunStatusEvent(threadID string, status domain.RunStatus, reason domain.TerminalReason) domain.Event {
	payload := map[string]any{"status": string(status)}
	if reason != "" {
		payload["reason"] = string(reason)
	}
	return threadEvent(domain.EvtRunStatus, threadID, payload)
}

// MessageDeltaEvent 造一条 evt.message.delta（文本增量）。
func MessageDeltaEvent(threadID, messageID, text string) domain.Event {
	return threadEvent(domain.EvtMessageDelta, threadID, map[string]any{
		"messageId": messageID, "kind": "text", "delta": text,
	})
}

// ToolStartedEvent 造一条工具卡 started 的 evt.item.updated（ItemKey tool:<callId>）。
func ToolStartedEvent(threadID, callID, tool string) domain.Event {
	return threadEvent(domain.EvtItemUpdated, threadID, map[string]any{
		"key": "tool:" + callID, "status": "started", "tool": tool,
	})
}

// ToolFinishedEvent 造一条工具卡 finished 的 evt.item.updated；receiptStatus 取
// domain.ReceiptStatus（ok|error|denied|timeout|interrupted）。
func ToolFinishedEvent(threadID, callID string, receiptStatus domain.ReceiptStatus) domain.Event {
	return threadEvent(domain.EvtItemUpdated, threadID, map[string]any{
		"key": "tool:" + callID, "status": string(receiptStatus),
	})
}

// NoticeEvent 造一条 evt.notice：只给人的提示，永不进模型（INV-10）。
func NoticeEvent(threadID string, level domain.NoticeLevel, text string) domain.Event {
	return threadEvent(domain.EvtNotice, threadID, map[string]any{"level": string(level), "text": text})
}

// ErrorEvent 造一条 evt.error（可恢复错误 + 诊断码，08 §8.2/§8.5）。
func ErrorEvent(threadID string, code domain.Code, diagCode domain.Diagnostic) domain.Event {
	e := domain.NewError(code, "契约测试注入的错误").WithDiag(diagCode)
	return threadEvent(domain.EvtError, threadID, map[string]any{
		"code": int(code), "name": domain.CodeName(code), "diag": e.Diag,
	})
}

// ReadyEvent 造一条工作区级 evt.ready。
func ReadyEvent(product string, toolCount, pluginCount int) domain.Event {
	evt := threadEvent(domain.EvtReady, "", map[string]any{
		"product": product, "tools": toolCount, "plugins": pluginCount,
	})
	evt.ThreadID = ""
	return evt
}

func threadEvent(kind domain.EventKind, threadID string, payload map[string]any) domain.Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	return domain.Event{Kind: kind, ThreadID: threadID, At: FrozenTime, Payload: raw}
}
