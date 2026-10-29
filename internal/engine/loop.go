package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/adag/ada-go/internal/domain"
)

// TerminalError 是 Run 的错误终态别名（domain.TerminalError 的简称，便于本包可读性）。
const (
	TerminalError     = domain.TerminalError
	TerminalCompleted = domain.TerminalCompleted
	TerminalCancelled = domain.TerminalCancelled
	TerminalInterrupt = domain.TerminalInterrupted
	TerminalBudget    = domain.TerminalBudgetExceeded
)

// loopDetector 守 02 §5.3 的循环检测：它不是变相的步数上限（INV-12）。
//
// 规则：同一轮内「相同工具 + 相同参数 + 相同回执」累计达到 LoopRepeatLimit（默认 3）次，
// 先给模型一条 loop_detected 错误回执提示换方法；累计到 LoopAbortLimit（默认 5）次则按
// error 结束轮次。按指纹累计而不是只看「连续」：模型常在两次相同调用之间插一条别的
// 工具或一句文本，连续计数被打散，但浪费的仍是同一批重复调用。repeatable 工具（轮询类）
// 不参与计数；用户发新消息后计数重置。
type loopDetector struct {
	repeatLimit int
	abortLimit  int

	counts    map[string]int
	lastKey   string
	lastCount int
	totalSeen int
}

func newLoopDetector(repeat, abort int) *loopDetector {
	if repeat <= 0 {
		repeat = domain.LoopRepeatLimit
	}
	if abort <= repeat {
		abort = domain.LoopAbortLimit
	}
	return &loopDetector{repeatLimit: repeat, abortLimit: abort, counts: map[string]int{}}
}

// Observe 记录一次调用结果，返回需要采取的动作。
type loopAction int

const (
	loopNone loopAction = iota
	// loopWarn：给模型一次 loop_detected 错误回执，轮次继续。
	loopWarn
	// loopAbort：连续重复仍未改道，按 error 结束轮次。
	loopAbort
)

func (l *loopDetector) Observe(descriptor domain.Descriptor, args json.RawMessage, res domain.Result) loopAction {
	if descriptor.Repeatable {
		// 轮询类工具允许重复相同调用（02 §5.4），不计入循环判定。
		l.totalSeen++
		return loopNone
	}
	key := fingerprint(descriptor.Name, args, res)
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.counts[key]++
	n := l.counts[key]
	l.lastKey, l.lastCount = key, n
	l.totalSeen++
	switch {
	case n >= l.abortLimit:
		return loopAbort
	case n == l.repeatLimit:
		return loopWarn
	default:
		return loopNone
	}
}

// Reset 在用户插入新消息后调用（02 §5.3：计数重置）。
func (l *loopDetector) Reset() {
	l.counts = map[string]int{}
	l.lastKey = ""
	l.lastCount = 0
}

// RepeatRun 返回当前指纹的累计重复次数（测试与诊断用）。
func (l *loopDetector) RepeatRun() int { return l.lastCount }

// fingerprint 把工具名、参数与「给模型的内容」哈希成一个键。
//
// Details 不参与：它只给界面，不参与模型决策，因此不应影响循环判定（INV-10）。
func fingerprint(tool string, args json.RawMessage, res domain.Result) string {
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{0})
	h.Write(normalizeJSON(args))
	h.Write([]byte{0})
	for _, p := range res.Content {
		if p.Type != domain.PartText {
			h.Write([]byte{byte(p.Type[0]), 0})
			continue
		}
		h.Write([]byte(p.Text))
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	h.Write([]byte(res.Receipt.Status))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// normalizeJSON 让语义相同的参数得到同一个键（键序不影响）。
func normalizeJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}
