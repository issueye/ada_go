package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/adag/ada-go/internal/domain"
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
	RunID       string `json:"runId"`
	Status      string `json:"status"`
	Terminal    string `json:"terminal"`
	StartedAt   string `json:"startedAt"`
	EndedAt     string `json:"endedAt"`
	ErrorCode   int    `json:"errorCode"`
	ErrorDetail string `json:"errorDetail"`
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
	CallID    string          `json:"callId"`
	Tool      string          `json:"tool"`
	Status    string          `json:"status"`
	Input     json.RawMessage `json:"input"`
	Arguments json.RawMessage `json:"arguments"`
	Content   []messagePart   `json:"content"`
	Details   json.RawMessage `json:"details"`
	Receipt   struct {
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
	Page                string // chat | overview | extensions | ext:<id>
	NeedsUI             bool   // 页面含 AdaUI 客户端岛时才加载引擎/组件资源
	ProductName         string
	BrandName           string
	ProductBadge        string
	Locale              string
	Identity            string
	LoginPath           string
	CSRF                string
	Workspace           string
	WorkspaceName       string
	WorkspaceIndex      int
	Workspaces          []workspaceOption
	ShowWorkspacePicker bool
	RefreshSeconds      int
	Gateway             string
	ActiveThreadID      string
	Connected           bool
	TopbarStatus        template.HTML
	TopbarActions       template.HTML
	// PluginNav 是前端插件声明的侧栏导航（供应商 / Token 用量等）。
	PluginNav []pluginNavLink
	// Flash 是提示条 HTML（空 = 不渲染）。
	Flash template.HTML
	// Sidebar 是组件库渲染好的侧栏整块 HTML。
	Sidebar template.HTML
	// Icons 是页面级 SVG sprite。
	Icons template.HTML
	// ConfirmDialog 是共享确认弹窗（每个外壳都必须带上）。
	ConfirmDialog template.HTML
	// Slots 是 12 个稳定插槽的 SSR 宿主内容。M2 阶段由基座保持为空，
	// M4 接上插件宿主后由渲染结果填充；模板通过 {{slot ...}} 落位。
	Slots slotViews
	// SlotCounts 是每个插槽的贡献者数量（data-contributors）；PluginUI 缺席时全为 0。
	SlotCounts slotCounts
	// PluginStyles / PluginScripts 是插件静态资源的 URL（/ext/{id}/static/...）。
	PluginStyles  []string
	PluginScripts []string
}

// slotCounts 是 12 个插槽的贡献者计数，字段与 slotViews 一一对应。
type slotCounts struct {
	SidebarTop       int
	SidebarBottom    int
	TopbarRight      int
	ChatCorner       int
	ComposerLeft     int
	ComposerRight    int
	ComposerFooter   int
	ComposerAbove    int
	ThreadTray       int
	TranscriptAfter  int
	ExtensionsList   int
	DashboardWidgets int
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

// pluginNavLink 是前端插件贡献的一个侧栏导航项。
type pluginNavLink struct {
	PluginID string
	Label    string
	Href     string
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
	// TranscriptStateJSON 是对话流岛的初始数据：助手文本是 VNode 数据，
	// 工具/用户/Notice 行仍是服务端渲染 HTML；没有 JS 时用 Transcript 的 SSR 行。
	TranscriptStateJSON string
	// Models 是当前可选的供应商/模型（model.list 投影，供输入框旁的选择器）。
	Models            []modelOption
	CurrentModelLabel string
	CurrentModelValue string
}

type modelOption struct {
	ProviderID string
	ModelID    string
	Label      string
	ShortLabel string
	Current    bool
}

type threadInfo struct {
	ID        string
	Title     string
	CreatedAt string
	UpdatedAt string
	Archived  bool
}

type runView struct {
	Status      string
	Terminal    string
	Label       string
	StartedAt   string
	EndedAt     string
	Active      bool
	ErrorCode   int
	ErrorLabel  string
	ErrorDetail string
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

// transcriptPartState 是助手消息的一段：text 段带 VNode 数据（由 ui-markdown
// 渲染），thinking/image 只带原始数据。
type transcriptPartState struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Nodes json.RawMessage `json:"nodes,omitempty"`
}

// transcriptRowState 是对话流 JSON 的一行：key 用于浏览器 keyed diff。
// 助手行提供 parts（前端组件渲染）；工具/用户/Notice 行提供服务端模板 HTML。
type transcriptRowState struct {
	Key   string                `json:"key"`
	Kind  string                `json:"kind,omitempty"`
	At    string                `json:"at,omitempty"`
	Parts []transcriptPartState `json:"parts,omitempty"`
	HTML  string                `json:"html,omitempty"`
}

// transcriptPayload 是 /partials/transcript/{id} 的响应。
type transcriptPayload struct {
	ThreadID  string               `json:"threadId"`
	RunStatus string               `json:"runStatus"`
	Rows      []transcriptRowState `json:"rows"`
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
	Args        string
	ArgsHint    string
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
	// Questions 只在 kind=form 时有值：多题分步表单的题目（body 里的 JSON）。
	Questions []interactionQuestion
}

// interactionQuestion 是分步表单里的一题；Index 用于表单字段名 q0、q1…
type interactionQuestion struct {
	Index    int
	Question string
	Kind     string // confirm | select | input
	Options  []string
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

// notFoundData 是 404 页：保留完整外壳与导航，避免用户落到裸文本 404。
type notFoundData struct {
	baseData
	Path string
}

type loginData struct {
	baseData
}

// overviewData 是概览页（/overview）：统计卡 + 最近会话 + 插件卡片。
type overviewData struct {
	baseData
	Stats  []overviewStat
	Recent []overviewThread
	// StateJSON 是给浏览器岛（AdaUI）的初始状态；无 JS 时仍渲染 Stats/Recent。
	StateJSON string
}

// dashboardState 是 /partials/dashboard.json 与概览岛共用的数据形态。
type dashboardState struct {
	Stats  []overviewStat   `json:"stats"`
	Recent []overviewThread `json:"recent"`
}

type overviewStat struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Hint  string `json:"hint"`
	Tone  string `json:"tone"` // neutral | brand | warn
}

type overviewThread struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Workspace    string `json:"workspace"`
	Mode         string `json:"mode"`
	Running      bool   `json:"running"`
	ItemCount    int    `json:"itemCount"`
	UpdatedLabel string `json:"updatedLabel"`
}

// workspacesData 是工作区页（/workspaces）：磁盘/常用位置 + 最近工作区 + 目录浏览。
type workspacesData struct {
	baseData
	Roots      []workspaceRoot
	Recent     []workspaceRecent
	Current    string
	Parent     string
	ParentHref string
	Entries    []workspaceEntry
	Truncated  bool
	Omitted    int
	BindThread string
	// Selectable 表示当前浏览目录正好是某个已配置工作区，可以设为当前工作区。
	Selectable bool
	// StateJSON 是给浏览器岛的初始状态；无 JS 时仍渲染上面的字段。
	StateJSON string
}

// workspacesState 是工作区岛与后续 JSON 刷新共用的数据形态。
type workspacesState struct {
	Roots      []workspaceRoot   `json:"roots"`
	Recent     []workspaceRecent `json:"recent"`
	Current    string            `json:"current"`
	Parent     string            `json:"parent"`
	ParentHref string            `json:"parentHref"`
	Entries    []workspaceEntry  `json:"entries"`
	Truncated  bool              `json:"truncated"`
	Omitted    int               `json:"omitted"`
	Selectable bool              `json:"selectable"`
	CSRF       string            `json:"csrf"`
}

type workspaceRoot struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Href  string `json:"href"`
	Kind  string `json:"kind"`
}

type workspaceRecent struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
	Active   bool   `json:"active"`
	// Known 表示该工作区的会话数已知（只有当前工作区查询过）。
	Known bool `json:"known"`
}

type workspaceEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Href string `json:"href"`
}

// extensionsData 是插件页（/extensions）：统一列表（不再按前端/后端分区）。
type extensionsData struct {
	baseData
	Plugins     []pluginRow
	PluginCount int
	// StateJSON 是给浏览器岛的初始状态；无 JS 时仍渲染 Plugins。
	StateJSON string
}

// extensionsState 是插件列表岛与 /partials/extensions.json 共用的数据形态。
type extensionsState struct {
	Plugins []pluginRow `json:"plugins"`
	Count   int         `json:"count"`
	CSRF    string      `json:"csrf"`
}

// pluginRow 是插件列表/详情的一行：同一个 id 的前端与后端能力合并成一条。
// 前端/后端归属（Frontend/Backend）只在详情页展示，列表里不分区。
type pluginRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	APIVersion  int    `json:"apiVersion,omitempty"`
	Trust       string `json:"trust,omitempty"`
	Description string `json:"description"`
	Source      string `json:"source"`
	// Path 是第三方插件的绝对目录；随包插件为空。
	Path         string `json:"path,omitempty"`
	Status       string `json:"status"`
	StatusLabel  string `json:"statusLabel"`
	StatusTone   string `json:"statusTone"`
	Error        string `json:"error"`
	Enabled      bool   `json:"enabled"`
	Frontend     bool   `json:"frontend"`
	Backend      bool   `json:"backend"`
	Tools        int    `json:"tools"`
	Hooks        int    `json:"hooks"`
	Skills       int    `json:"skills"`
	Slots        int    `json:"slots"`
	Pages        int    `json:"pages"`
	Nav          int    `json:"nav"`
	FirstPage    string `json:"firstPage"`
	HasDashboard bool   `json:"hasDashboard"`
	// Readme 是插件 README（无 README 时回退为清单说明）；ReadmeNodes 是
	// 给前端 ui-markdown 的 VNode 数据（详情页岛直接渲染，不再跑一次 Markdown）。
	Readme      string          `json:"readme,omitempty"`
	ReadmeNodes json.RawMessage `json:"readmeNodes,omitempty"`
	// Files 是插件包内文件清单（详情页「插件目录」）。
	Files []pluginFileDTO `json:"files,omitempty"`
}

// pluginFileDTO 是插件包内一个文件（相对路径）。
type pluginFileDTO struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Dir        bool   `json:"dir,omitempty"`
	Depth      int    `json:"depth,omitempty"`
	SizeLabel  string `json:"sizeLabel,omitempty"`
	DepthClass string `json:"depthClass,omitempty"`
}

// pluginDetailData 是 /extensions/{id} 的详情页。
type pluginDetailData struct {
	baseData
	Plugin pluginRow
	// StateJSON 是给浏览器岛的初始状态；无 JS 时仍渲染 Plugin。
	StateJSON string
}

// pluginDetailState 是插件详情岛的数据形态。
type pluginDetailState struct {
	Plugin pluginRow `json:"plugin"`
	CSRF   string    `json:"csrf"`
}

// offlineData 是二级页的离线壳：保留页面外壳，内容区如实说“未连接”。
type offlineData struct {
	baseData
	Reason string
}

// buildRun 把 Core 的运行状态翻译成界面文案。
func buildRun(rs runStateDTO) runView {
	v := runView{
		Status:      rs.Status,
		Terminal:    rs.Terminal,
		StartedAt:   rs.StartedAt,
		EndedAt:     rs.EndedAt,
		ErrorCode:   rs.ErrorCode,
		ErrorLabel:  runErrorLabel(rs.Terminal, rs.ErrorCode),
		ErrorDetail: rs.ErrorDetail,
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

// runErrorLabel 把终态错误映射成给用户看的下一步；详情仍以诊断码为准。
func runErrorLabel(terminal string, code int) string {
	switch terminal {
	case "error":
		if domain.Code(code) == domain.CodeProviderError {
			return "供应商调用失败：请检查 Base URL 与 API Key，或切换到其他模型后重试。"
		}
		return "本轮执行失败；请检查供应商配置与 Core 日志后重试。"
	case "interrupted":
		return "本轮被中断（网关重启或进程退出），可以重新发送。"
	case "budget_exceeded":
		return "本轮超出配置的预算；调整预算或缩短任务后重试。"
	}
	return ""
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
		view := interactionView{
			ID:      x.ID,
			Kind:    x.Kind,
			Title:   x.Title,
			Body:    x.Body,
			Status:  x.Status,
			Options: append([]string(nil), x.Options...),
			Action:  threadPath(threadID) + "/interaction/" + url.PathEscape(x.ID),
			CSRF:    csrf,
		}
		if x.Kind == string(domain.InteractionForm) {
			// 题目存在 body 里（interactions 表没有 payload 列）；解析失败就当空表单，
			// 让用户至少能取消，而不是看到一段裸 JSON。
			view.Questions = parseInteractionQuestions(x.Body)
			view.Body = ""
		}
		out = append(out, view)
	}
	return out
}

// parseInteractionQuestions 解析 form 交互 body 里的题目数组。
func parseInteractionQuestions(raw string) []interactionQuestion {
	var items []struct {
		Question string   `json:"question"`
		Kind     string   `json:"kind"`
		Options  []string `json:"options"`
	}
	if json.Unmarshal([]byte(raw), &items) != nil {
		return nil
	}
	out := make([]interactionQuestion, 0, len(items))
	for i, it := range items {
		kind := strings.TrimSpace(it.Kind)
		if kind != "confirm" && kind != "select" && kind != "input" {
			kind = "input"
		}
		out = append(out, interactionQuestion{
			Index: i, Question: it.Question, Kind: kind, Options: it.Options,
		})
	}
	return out
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
			parts = append(parts, partView{Type: "image"})
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
