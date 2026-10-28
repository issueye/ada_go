package web

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/url"
	"strings"
	"unicode/utf8"
)

// 展示层裁剪阈值：Core 已对单字段做 32KB 截断（8.3），这里再收一层，
// 避免一条历史消息把整页撑得不可用。
const (
	maxPartBytes   = 16 << 10
	maxToolOutput  = 16 << 10
	maxDetailsSize = 8 << 10
)

// ---- Core 返回的最小 DTO（只声明 SSR 用到的字段）----

type threadDTO struct {
	ID          string `json:"id"`
	Workspace   string `json:"workspace"`
	Title       string `json:"title"`
	HeadEntryID string `json:"headEntryId"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	Archived    bool   `json:"archived"`
}

type threadSummaryDTO struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	HeadEntryID string `json:"headEntryId"`
	UpdatedAt   string `json:"updatedAt"`
	Archived    bool   `json:"archived"`
}

type threadListResult struct {
	Threads    []threadSummaryDTO `json:"threads"`
	NextCursor string             `json:"nextCursor"`
	HasMore    bool               `json:"hasMore"`
}

type itemDTO struct {
	Key         string          `json:"key"`
	Type        string          `json:"type"`
	At          string          `json:"at"`
	Fingerprint string          `json:"fingerprint"`
	Truncated   bool            `json:"truncated"`
	Payload     json.RawMessage `json:"payload"`
}

type runStateDTO struct {
	RunID     string `json:"runId"`
	Status    string `json:"status"`
	Terminal  string `json:"terminal"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
	ErrorCode int    `json:"errorCode"`
}

type interactionDTO struct {
	ID         string   `json:"id"`
	ThreadID   string   `json:"threadId"`
	RunID      string   `json:"runId"`
	CallID     string   `json:"callId"`
	PluginID   string   `json:"pluginId"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Options    []string `json:"options"`
	Status     string   `json:"status"`
	CreatedAt  string   `json:"createdAt"`
	ResolvedAt string   `json:"resolvedAt"`
	ExpiresAt  string   `json:"expiresAt"`
}

type threadGetResult struct {
	Thread       threadDTO        `json:"thread"`
	Items        []itemDTO        `json:"items"`
	Seq          int64            `json:"seq"`
	Run          runStateDTO      `json:"run"`
	Interactions []interactionDTO `json:"interactions"`
	HasMore      bool             `json:"hasMore"`
}

type entryDTO struct {
	ID        string          `json:"id"`
	ThreadID  string          `json:"threadId"`
	ParentID  string          `json:"parentId"`
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Role      string          `json:"role"`
	Payload   json.RawMessage `json:"payload"`
	Tokens    int64           `json:"tokens"`
	CreatedAt string          `json:"createdAt"`
}

type entryPage struct {
	Entries    []entryDTO `json:"entries"`
	NextCursor string     `json:"nextCursor"`
	HasMore    bool       `json:"hasMore"`
}

// messagePart/messagePayload 对应条目 payload 里的消息形态：
// {"role":"assistant","parts":[{"type":"text"|"thinking",...}]}。
type messagePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Mime string `json:"mime"`
	Data string `json:"data"`
}

type messagePayload struct {
	Role  string        `json:"role"`
	Parts []messagePart `json:"parts"`
	Text  string        `json:"text"`
}

// toolPayload 对应工具结果条目（08 §8.4 / spec ToolResult）。
type toolPayload struct {
	CallID  string          `json:"callId"`
	Tool    string          `json:"tool"`
	Status  string          `json:"status"`
	Content []messagePart   `json:"content"`
	Details json.RawMessage `json:"details"`
	Receipt struct {
		Status     string `json:"status"`
		DurationMS int64  `json:"durationMs"`
		Truncated  bool   `json:"truncated"`
		Reason     string `json:"reason"`
	} `json:"receipt"`
}

// ---- 模板视图 ----

// baseData 是所有页面共用的外壳字段（嵌入后模板按提升字段访问）。
type baseData struct {
	Title               string
	ProductName         string
	Locale              string
	Identity            string
	LoginPath           string
	CSRF                string
	Workspace           string
	WorkspaceIndex      int
	Workspaces          []workspaceOption
	ShowWorkspacePicker bool
	RefreshSeconds      int
	// Slots 是 12 个稳定插槽的 SSR 宿主内容。M2 阶段由基座保持为空，
	// M4 接上插件宿主后由渲染结果填充；模板通过 {{slot ...}} 落位。
	Slots slotViews
	// Icons 是页面级 SVG 雪碧图/图标 HTML（M2 先为空，图标仍在模板内联）。
	Icons template.HTML
}

// slotViews 是 12 个插槽的模板字段，字段名与 7.2 表格一一对应。
// 所有值都是 template.HTML，只能由基座（或经过 VNode 校验的插件宿主）写入；
// 插件与消息文本不会被提升为 template.HTML。
type slotViews struct {
	SidebarTop       template.HTML
	SidebarBottom    template.HTML
	TopbarRight      template.HTML
	ChatCorner       template.HTML
	ComposerLeft     template.HTML
	ComposerRight    template.HTML
	ComposerFooter   template.HTML
	ComposerAbove    template.HTML
	ThreadTray       template.HTML
	TranscriptAfter  template.HTML
	ExtensionsList   template.HTML
	DashboardWidgets template.HTML
}

// workspaceOption 是工作区选择器的一项。
type workspaceOption struct {
	Index int
	Name  string
	Path  string
}

type threadsData struct {
	baseData
	IsHome     bool
	Threads    []threadRow
	Limit      int
	NextCursor string
	HasMore    bool
}

type threadRow struct {
	ID        string
	Title     string
	UpdatedAt string
	Archived  bool
}

type threadData struct {
	baseData
	Thread       threadInfo
	Run          runView
	Transcript   []transcriptItem
	Interactions []interactionView
	TurnKey      string
}

type threadInfo struct {
	ID        string
	Title     string
	CreatedAt string
	UpdatedAt string
	Archived  bool
}

type runView struct {
	Status    string
	Terminal  string
	Label     string
	StartedAt string
	EndedAt   string
	Active    bool
}

type transcriptItem struct {
	Kind  string // user | assistant | tool | notice
	Key   string
	At    string
	Parts []partView
	Tool  *toolView
	Level string
	Text  string
}

type partView struct {
	Type string // text | thinking | image
	Text string
}

type toolView struct {
	CallID      string
	Tool        string
	Status      string
	StatusLabel string
	DurationMS  int64
	Truncated   bool
	Output      string
	Details     string
	Reason      string
}

type interactionView struct {
	ID      string
	Kind    string
	Title   string
	Body    string
	Status  string
	Options []string
	Action  string
	CSRF    string
}

type errorData struct {
	baseData
	Status     int
	Heading    string
	Message    string
	Detail     string
	RetryPath  string
	RetryLabel string
}

type loginData struct {
	baseData
}

// buildRun 把 Core 的运行状态翻译成界面文案。
func buildRun(rs runStateDTO) runView {
	v := runView{
		Status:    rs.Status,
		Terminal:  rs.Terminal,
		StartedAt: rs.StartedAt,
		EndedAt:   rs.EndedAt,
	}
	switch rs.Status {
	case "running":
		v.Label = "运行中"
		v.Active = true
	case "waiting_input":
		v.Label = "等待答复"
	case "finishing":
		v.Label = "收尾中"
		v.Active = true
	case "terminal", "":
		switch rs.Terminal {
		case "completed":
			v.Label = "完成"
		case "cancelled":
			v.Label = "已停止"
		case "error":
			v.Label = "失败"
		case "interrupted":
			v.Label = "已中断"
		case "budget_exceeded":
			v.Label = "超出预算"
		default:
			v.Label = "空闲"
		}
	default:
		v.Label = rs.Status
	}
	return v
}

// buildThreadList 把线程列表投影成模板行。
func buildThreadList(in []threadSummaryDTO) []threadRow {
	out := make([]threadRow, 0, len(in))
	for _, t := range in {
		out = append(out, threadRow{
			ID:        t.ID,
			Title:     t.Title,
			UpdatedAt: t.UpdatedAt,
			Archived:  t.Archived,
		})
	}
	return out
}

// buildThreadInfo 填充会话页头部。
func buildThreadInfo(t threadDTO) threadInfo {
	return threadInfo{
		ID:        t.ID,
		Title:     t.Title,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
		Archived:  t.Archived,
	}
}

// buildInteractions 只保留 pending 的交互，并为每条生成无 JS 表单地址。
func buildInteractions(in []interactionDTO, threadID, csrf string) []interactionView {
	out := make([]interactionView, 0, len(in))
	for _, x := range in {
		if x.Status != "" && x.Status != "pending" {
			continue
		}
		out = append(out, interactionView{
			ID:      x.ID,
			Kind:    x.Kind,
			Title:   x.Title,
			Body:    x.Body,
			Status:  x.Status,
			Options: append([]string(nil), x.Options...),
			Action:  threadPath(threadID) + "/interaction/" + url.PathEscape(x.ID),
			CSRF:    csrf,
		})
	}
	return out
}

// buildTranscript 把 entries（Core 按 seq 降序返回）转成按时间正序的展示项。
func buildTranscript(entries []entryDTO) []transcriptItem {
	out := make([]transcriptItem, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		var probe messagePayload
		_ = json.Unmarshal(e.Payload, &probe)
		role := e.Role
		if role == "" {
			role = probe.Role
		}
		switch {
		case role == "tool":
			if tv := toolViewFromPayload(e.Payload, false); tv != nil {
				out = append(out, transcriptItem{Kind: "tool", Key: "tool:" + tv.CallID, At: e.CreatedAt, Tool: tv})
			}
		case role == "user" || role == "assistant":
			out = append(out, messageItem(e.Payload, role, e.CreatedAt))
		case e.Kind == "compaction":
			out = append(out, transcriptItem{Kind: "notice", Level: "info", At: e.CreatedAt, Text: "上下文已压缩。"})
		}
	}
	return out
}

// transcriptFromItems 是 thread.get items 的回退路径（entries 不可用时）。
func transcriptFromItems(items []itemDTO) []transcriptItem {
	out := make([]transcriptItem, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case "message":
			var probe messagePayload
			_ = json.Unmarshal(it.Payload, &probe)
			role := probe.Role
			if role == "" {
				role = "assistant"
			}
			out = append(out, messageItem(it.Payload, role, it.At))
		case "tool":
			if tv := toolViewFromPayload(it.Payload, it.Truncated); tv != nil {
				out = append(out, transcriptItem{Kind: "tool", Key: it.Key, At: it.At, Tool: tv})
			}
		}
	}
	return out
}

// noticesFromItems 把只给界面的 notice 投影追加到对话流末尾。
func noticesFromItems(items []itemDTO) []transcriptItem {
	out := make([]transcriptItem, 0)
	for _, it := range items {
		if it.Type != "notice" {
			continue
		}
		var n struct {
			Level string `json:"level"`
			Text  string `json:"text"`
		}
		if json.Unmarshal(it.Payload, &n) != nil || strings.TrimSpace(n.Text) == "" {
			continue
		}
		out = append(out, transcriptItem{
			Kind:  "notice",
			Level: n.Level,
			Text:  clip(n.Text, maxPartBytes),
			At:    it.At,
		})
	}
	return out
}

func messageItem(raw json.RawMessage, role, at string) transcriptItem {
	var m messagePayload
	_ = json.Unmarshal(raw, &m)
	if role != "user" && role != "assistant" {
		role = "assistant"
	}
	parts := make([]partView, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch p.Type {
		case "thinking":
			parts = append(parts, partView{Type: "thinking", Text: clip(p.Text, maxPartBytes)})
		case "image":
			parts = append(parts, partView{Type: "image"})
		default:
			parts = append(parts, partView{Type: "text", Text: clip(p.Text, maxPartBytes)})
		}
	}
	// 兼容只有顶层 text 的简化载荷。
	if len(parts) == 0 && m.Text != "" {
		parts = append(parts, partView{Type: "text", Text: clip(m.Text, maxPartBytes)})
	}
	return transcriptItem{Kind: role, At: at, Parts: parts}
}

func toolViewFromPayload(raw json.RawMessage, itemTruncated bool) *toolView {
	var p toolPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil
	}
	status := p.Status
	if status == "" {
		status = p.Receipt.Status
	}
	var texts []string
	for _, c := range p.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		case "image":
			texts = append(texts, "[图片]")
		}
	}
	tool := p.Tool
	if tool == "" {
		tool = "tool"
	}
	return &toolView{
		CallID:      p.CallID,
		Tool:        tool,
		Status:      status,
		StatusLabel: toolStatusLabel(status),
		DurationMS:  p.Receipt.DurationMS,
		Truncated:   p.Receipt.Truncated || itemTruncated,
		Output:      clip(strings.Join(texts, "\n"), maxToolOutput),
		Details:     prettyJSON(p.Details),
		Reason:      p.Receipt.Reason,
	}
}

func toolStatusLabel(status string) string {
	switch status {
	case "ok":
		return "成功"
	case "error":
		return "失败"
	case "denied":
		return "已拒绝"
	case "timeout":
		return "超时"
	case "interrupted":
		return "已中断"
	default:
		return status
	}
}

// prettyJSON 只用于展示 details（界面专用，永不进模型，INV-10）。
func prettyJSON(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return clip(s, maxDetailsSize)
	}
	return clip(buf.String(), maxDetailsSize)
}

// clip 按字节上限截断，并回退到合法 UTF-8 边界。
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…（已截断）"
}
