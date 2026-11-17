package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 展示层裁剪阈值：Core 已对单字段做 32KB 截断（8.3），这里再收一层，
// 避免一条历史消息把 JSON 端点的响应撑得不可用。
const (
	maxPartBytes   = 16 << 10
	maxToolOutput  = 16 << 10
	maxDetailsSize = 8 << 10
)

// transcriptItem 是对话流的一行（中间形态：Core entries → JSON 数据行）。
type transcriptItem struct {
	Kind  string // user | assistant | tool | notice
	Key   string
	At    string
	Parts []partView
	Tool  *toolView
	Level string
	Text  string
	// 以下字段只给 user 行：「编辑并重发」的上下文。
	// EditText 是原文（只含文本段，图片由服务端沿用），CanEdit 由轮次状态决定；
	// EditKey 是每个表单独立的幂等键，防止双击重复起轮。
	EditText string
	EditKey  string
	ThreadID string
	CSRF     string
	CanEdit  bool
}

type partView struct {
	Type string // text | thinking | image
	Text string
	// Mime/Data 仅在 Type=image 时有值：data URL 的两半，
	// JSON 行拼成 src="data:{{.Mime}};base64,{{.Data}}" 渲染消息里的图片。
	Mime string
	Data string
}

type toolView struct {
	CallID      string
	Tool        string
	Status      string
	StatusLabel string
	DurationMS  int64
	Truncated   bool
	Args        string
	ArgsHint    string
	Output      string
	Details     string
	Reason      string
}

// transcriptPartState 是对话流 JSON 行里的一段：
//   - assistant 行：text 段带 VNode 节点（nodes），thinking/image 只带原始数据；
//   - user 行：text/thinking 带原文，image 带 data URL 的两半（mime/data）。
type transcriptPartState struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Nodes json.RawMessage `json:"nodes,omitempty"`
	Mime  string          `json:"mime,omitempty"`
	Data  string          `json:"data,omitempty"`
}

// toolRowState 是工具行 JSON 的 tool 对象（页面侧 row→tool 的字段投影）。
type toolRowState struct {
	CallID      string `json:"callId,omitempty"`
	Name        string `json:"name"`
	Status      string `json:"status,omitempty"`
	StatusLabel string `json:"statusLabel,omitempty"`
	DurationMS  int64  `json:"durationMs,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Args        string `json:"args,omitempty"`
	ArgsHint    string `json:"argsHint,omitempty"`
	Output      string `json:"output,omitempty"`
	Details     string `json:"details,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// transcriptRowState 是 /partials/transcript/{id} 的一行：key 用于浏览器 keyed diff。
//
// 行 JSON 形状（kind → 字段）：
//
//	assistant: {key, kind:"assistant", at, parts[]}；
//	           parts[i] = {type:"text", nodes:[VNode]} | {type:"thinking", text} | {type:"image"}
//	           （助手行保持 VNode 形态，nodes 由 ui-markdown 渲染）
//	user:      {key, kind:"user", at, parts[], canEdit?, editKey?, editText?, threadId?, csrf?}；
//	           parts[i] = {type:"text", text} | {type:"thinking", text} | {type:"image", mime, data}
//	           编辑上下文只在 canEdit=true 的行出现（threadId 用于表单地址，
//	           csrf 供「编辑并重发」表单的双提交校验，与页面行内表单语义一致）
//	tool:      {key, kind:"tool", at, tool:{callId, name, status, statusLabel, durationMs,
//	           truncated, args, argsHint, output, details, reason}}（图标/色调由客户端派生）
//	notice:    {key, kind:"notice", at, level, text}（level: info | warn | error）
//	plain:     {key, kind:"plain", at, text}（兜底行；正常投影不产出）
type transcriptRowState struct {
	Key   string                `json:"key"`
	Kind  string                `json:"kind"`
	At    string                `json:"at,omitempty"`
	Parts []transcriptPartState `json:"parts,omitempty"`
	Tool  *toolRowState         `json:"tool,omitempty"`
	Level string                `json:"level,omitempty"`
	Text  string                `json:"text,omitempty"`
	// user 行的「编辑并重发」上下文；CanEdit=false 时全部省略。
	CanEdit  bool   `json:"canEdit,omitempty"`
	EditKey  string `json:"editKey,omitempty"`
	EditText string `json:"editText,omitempty"`
	ThreadID string `json:"threadId,omitempty"`
	CSRF     string `json:"csrf,omitempty"`
}

// transcriptPayload 是 /partials/transcript/{id} 的响应。
// HasMore/NextCursor 只在历史分页（?before=<cursor>）时有值：客户端据此
// 渲染「加载更早消息」入口（8.2 游标分页，与 HEAD 的 OlderHref 语义一致）。
type transcriptPayload struct {
	ThreadID   string               `json:"threadId"`
	RunStatus  string               `json:"runStatus"`
	Rows       []transcriptRowState `json:"rows"`
	HasMore    bool                 `json:"hasMore,omitempty"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

// buildTranscript 把 entries（Core 按 seq 降序返回）转成按时间正序的展示项。
func buildTranscript(entries []entryDTO) []transcriptItem {
	out := make([]transcriptItem, 0, len(entries))
	// EntriesBefore 返回按 seq 升序的窗口（旧→新），这里正向投影，不能再反转。
	for i := 0; i < len(entries); i++ {
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
			// 只带工具调用的助手消息没有任何可显示内容：不画空头像行。
			if item := messageItem(e.Payload, role, e.CreatedAt); len(item.Parts) > 0 {
				item.Key = e.ID // 供浏览器端按行复用（实时换行时不重画整份对话流）
				out = append(out, item)
			}
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
			if item := messageItem(it.Payload, role, it.At); len(item.Parts) > 0 {
				item.Key = it.Key
				out = append(out, item)
			}
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
			// 只有空白的思考块渲染出来就是孤零零一个「思考」，纯噪音。
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			parts = append(parts, partView{Type: "thinking", Text: clip(p.Text, maxPartBytes)})
		case "image":
			// Data 进 JSON 行的 data URL 两半；不截断（截断会让图片解码失败）。
			parts = append(parts, partView{Type: "image", Mime: p.Mime, Data: p.Data})
		default:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
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
	args := p.Input
	if len(args) == 0 || string(args) == "null" {
		args = p.Arguments
	}
	return &toolView{
		CallID:      p.CallID,
		Tool:        tool,
		Status:      status,
		StatusLabel: toolStatusLabel(status),
		DurationMS:  p.Receipt.DurationMS,
		Truncated:   p.Receipt.Truncated || itemTruncated,
		Args:        prettyJSON(args),
		ArgsHint:    compactArgs(args),
		Output:      clip(strings.Join(texts, "\n"), maxToolOutput),
		Details:     prettyJSON(p.Details),
		Reason:      p.Receipt.Reason,
	}
}

// compactArgs 把入参压成一行给工具行看（"path=\".\" recursive=true"）。
//
// 展开后的「输入参数」仍是完整 JSON；这里只解决「不展开就不知道它调了什么」。
// 单值超过 24 字符截断，整体超过 96 字符截断。
func compactArgs(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || len(obj) == 0 {
		return ""
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+compactValue(obj[k]))
	}
	return shorten(strings.Join(parts, " "), 96)
}

// shorten 是给行内摘要用的截断：只补一个省略号，不带「已截断」这类整句提示。
func shorten(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// compactValue 把单个参数值压成可读短串。
func compactValue(v any) string {
	switch value := v.(type) {
	case string:
		return `"` + shorten(value, 24) + `"`
	case bool:
		if value {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case []any:
		return "[" + strconv.Itoa(len(value)) + "]"
	case map[string]any:
		return "{…}"
	default:
		return shorten(fmt.Sprint(value), 24)
	}
}

// toolStatusLabel 只给需要文字的异常态一个标签：成功和失败靠行样式表达
// （失败用红色左条 + 红色工具名），不再多一个徽标占位。
func toolStatusLabel(status string) string {
	switch status {
	case "ok", "error":
		return ""
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
