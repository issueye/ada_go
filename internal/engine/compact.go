package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// 上下文水位与压缩（设计 04 §5.7、02 §5.3 第 6b 步）。
//
// 关键口径：
//   - 触发在每次模型请求前与轮次结束后各查一次；超过 contextWindow-reserve 或达到 80% 就压。
//   - 切点只允许落在 user/assistant 边界，绝不在工具调用与其回执之间（否则模型会看到孤儿 tool_call）。
//   - 压缩失败不丢历史：标记 compaction_failed 并继续用裁剪兜底；压缩 + 裁剪后仍放不下，
//     以 context_overflow 结束该轮 —— 这是错误终态，不是步数上限（INV-12）。

// Compactor 生成结构化摘要（04 §5.7：目标、已完成、未完成、改动文件、关键决策、风险）。
type Compactor interface {
	Compact(ctx context.Context, req CompactRequest) (CompactResult, error)
}

// CompactRequest 是一次摘要请求。
type CompactRequest struct {
	ThreadID    string
	RunID       string
	Model       ports.ModelRef
	System      string
	Previous    *CompactSummary // 上一次压缩的摘要，用于增量压缩
	Entries     []ports.Entry   // 待回顾的窗口（已按 seq 升序）
	KeepTokens  int
	Instruction string
}

// CompactResult 是摘要结果。
type CompactResult struct {
	Summary        CompactSummary
	FirstKeptEntry string
	KeptEntries    []ports.Entry
}

// CompactSummary 是写入 compaction entry 的结构化摘要。
type CompactSummary struct {
	Goal        string   `json:"goal"`
	Done        []string `json:"done,omitempty"`
	NotDone     []string `json:"notDone,omitempty"`
	Files       []string `json:"files,omitempty"`
	Decisions   []string `json:"keyDecisions,omitempty"`
	Risks       []string `json:"risks,omitempty"`
	Markdown    string   `json:"markdown"`
	GeneratedAt string   `json:"generatedAt"`
	// FirstKeptEntryID 是重放起点（04 §5.7：从 first_kept_entry_id 重放）。
	// 设计文档只在散文里提到这个字段，形状由 M1-A 钉在这里并写进 decisions.md。
	FirstKeptEntryID string `json:"firstKeptEntryId,omitempty"`
	Failed           bool   `json:"failed,omitempty"`
}

// estimateTokens 是保守的 token 估算：CJK 按每字 1 token，其余按 4 字节 1 token，
// 再叠加每条消息的结构开销。刻意高估而不是低估 —— 低估会让真实窗口溢出。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk := 0
	other := 0
	for _, r := range s {
		if r >= 0x3000 && r <= 0x9FFF || r >= 0xFF00 && r <= 0xFFEF {
			cjk++
			continue
		}
		other += utf8.RuneLen(r)
	}
	return cjk + other/4 + 1
}

// entryTokens 估算一条 entry 的 token。
func entryTokens(e ports.Entry) int {
	n := estimateTokens(string(e.Payload)) + estimateTokens(string(e.Role)) + 4
	if n < 1 {
		return 1
	}
	return n
}

// windowTokens 估算整个上下文窗口的 token（含摘要与系统提示）。
func windowTokens(system string, summary *CompactSummary, entries []ports.Entry) int {
	total := estimateTokens(system)
	if summary != nil {
		total += estimateTokens(summary.Markdown)
	}
	for _, e := range entries {
		total += entryTokens(e)
	}
	return total
}

// needsCompaction 报告是否需要压缩。
func needsCompaction(used int, model ports.ModelRef) bool {
	window := int(model.ContextWindow)
	if window <= 0 {
		return false // 模型没声明窗口：不猜，交给 provider 的错误路径处理。
	}
	if used*100 >= window*domain.ContextHighWaterRatio { // 80% 高水位（04 §5.7）
		return true
	}
	reserve := domain.ContextReserveTokens
	if window < reserve*2 {
		reserve = window / 4
	}
	return used > window-reserve
}

// splitPoint 返回「保留后缀」的起始下标：切点必须落在 user/assistant 边界，
// 绝不允许切开工具调用与其回执（04 §5.7）。找不到安全切点时返回 -1，表示无法切分。
func splitPoint(entries []ports.Entry, keepTokens int) int {
	used := 0
	for i := len(entries) - 1; i >= 0; i-- {
		used += entryTokens(entries[i])
		if used < keepTokens {
			continue
		}
		// 从 i 起是「保留区」；把 i 往前推到安全边界。
		for i > 0 {
			if safeBoundary(entries[i-1], entries[i]) {
				return i
			}
			i--
		}
		return i
	}
	if used < keepTokens {
		return -1 // 整个窗口都不够大，压了也没用。
	}
	return 0
}

// safeBoundary 报告两个相邻 entry 之间是否可以切。
func safeBoundary(prev, next ports.Entry) bool {
	if prev.Kind != domain.EntryMessage || next.Kind != domain.EntryMessage {
		// compaction / branch_summary / model_change 等非消息 entry 不能当切点前的尾巴。
		return next.Role == domain.RoleUser
	}
	// 用户与助手边界可切；工具回执（role=tool）紧跟其助手调用时不可切。
	if next.Role == domain.RoleTool {
		return false
	}
	if prev.Role == domain.RoleAssistant {
		// 助手消息里还有未完成的 toolCalls 时，它的回执必须在同一侧。
		var probe struct {
			ToolCalls []domain.ToolCall `json:"toolCalls"`
		}
		if len(prev.Payload) > 0 && json.Unmarshal(prev.Payload, &probe) == nil && len(probe.ToolCalls) > 0 {
			return false
		}
	}
	return next.Role == domain.RoleUser || next.Role == domain.RoleAssistant
}

// ModelCompactor 是 Compactor 的生产实现：用同一个 ModelClient 生成摘要。
//
// 摘要是 Core 行为，不依赖插件（6.6：插件只能观察 compaction.*，不能替代摘要，
// builtin 信任插件除外）。
type ModelCompactor struct {
	Model ports.ModelClient
	Clock ports.Clock
	IDs   ports.IDGen
}

var _ Compactor = (*ModelCompactor)(nil)

// compactInstruction 是摘要指令（结构化 Markdown 的六个小节）。
const compactInstruction = `把下面的对话历史压缩成继续工作所需的摘要。必须包含六个小节：
## 目标 / ## 已完成 / ## 未完成 / ## 改动文件 / ## 关键决策 / ## 风险
只写事实，不要编造未出现的文件或结论；无法确定的项写「未知」。用不超过 600 词。`

// Compact 请求模型生成摘要。
func (c *ModelCompactor) Compact(ctx context.Context, req CompactRequest) (CompactResult, error) {
	if c.Model == nil {
		return CompactResult{}, domain.NewError(domain.CodeCoreUnavailable, "压缩需要 ModelClient")
	}
	var b strings.Builder
	if req.Previous != nil && req.Previous.Markdown != "" {
		b.WriteString("已有摘要：\n")
		b.WriteString(req.Previous.Markdown)
		b.WriteString("\n\n新增历史：\n")
	}
	for _, e := range req.Entries {
		b.WriteString(fmt.Sprintf("[%s] %s\n", e.Role, string(e.Payload)))
	}

	system := req.System
	if system == "" {
		system = compactInstruction
	}
	stream, err := c.Model.Stream(ctx, ports.Request{
		Model:    req.Model,
		System:   system,
		Messages: []domain.Message{{Role: domain.RoleUser, Parts: []domain.Part{{Type: domain.PartText, Text: b.String()}}}},
	})
	if err != nil {
		return CompactResult{}, domain.WrapError(domain.NewError(domain.CodeProviderError, "压缩请求失败"), err)
	}
	defer stream.Close()

	var text strings.Builder
	finished := false
	for {
		d, err := stream.Recv()
		if err != nil {
			return CompactResult{}, domain.WrapError(domain.NewError(domain.CodeProtocolError, "压缩流读取失败"), err)
		}
		switch d.Kind {
		case ports.DeltaTextDelta:
			text.WriteString(d.Text)
		case ports.DeltaFinish:
			finished = true
		}
		if finished {
			break
		}
	}
	if !finished {
		// EOF 不等于完成（03 §5.5）：没有终止信号的摘要不可信。
		return CompactResult{}, domain.NewError(domain.CodeProtocolError, "压缩流没有显式终止信号")
	}

	markdown := strings.TrimSpace(text.String())
	if markdown == "" {
		return CompactResult{}, domain.NewError(domain.CodeContextOverflow, "摘要为空")
	}
	at := ""
	if c.Clock != nil {
		at = c.Clock.Now().UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	keep := tail(req.Entries, req.KeepTokens)
	return CompactResult{
		Summary: CompactSummary{
			Markdown:    markdown,
			Goal:        firstSection(markdown, "目标"),
			Done:        bullets(markdown, "已完成"),
			NotDone:     bullets(markdown, "未完成"),
			Files:       bullets(markdown, "改动文件"),
			Decisions:   bullets(markdown, "关键决策"),
			Risks:       bullets(markdown, "风险"),
			GeneratedAt: at,
		},
		FirstKeptEntry: firstID(keep),
		KeptEntries:    keep,
	}, nil
}

// tail 取最近约 keepTokens 的 entry（不保证边界安全，边界由 splitPoint 负责）。
func tail(entries []ports.Entry, keepTokens int) []ports.Entry {
	if keepTokens <= 0 {
		keepTokens = domain.CompactKeepTokens
	}
	used := 0
	for i := len(entries) - 1; i >= 0; i-- {
		used += entryTokens(entries[i])
		if used >= keepTokens {
			return entries[i+1:]
		}
	}
	return entries
}

func firstID(entries []ports.Entry) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[0].ID
}

func firstSection(md, title string) string {
	for _, line := range sectionLines(md, title) {
		t := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if t != "" {
			return t
		}
	}
	return ""
}

func bullets(md, title string) []string {
	var out []string
	for _, line := range sectionLines(md, title) {
		t := strings.TrimSpace(line)
		t = strings.TrimPrefix(t, "- ")
		t = strings.TrimPrefix(t, "* ")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// sectionLines 取 "## <title>" 小节内的行。
func sectionLines(md, title string) []string {
	lines := strings.Split(md, "\n")
	var out []string
	inSection := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "## ") {
			inSection = strings.Contains(trimmed, title)
			continue
		}
		if inSection && trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
