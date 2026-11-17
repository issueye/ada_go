package web

// threadListItem 是 /partials/threads.json 的一行（侧栏/会话列表共用投影，
// 字段语义与 HEAD 侧栏 renderThreadRow 一致：title 空串给客户端兜底，
// running 是 Core 内存态的未终态轮次标记）。
type threadListItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	Archived  bool   `json:"archived"`
	Running   bool   `json:"running"`
}

// threadsPayload 是 /partials/threads.json 的响应：单工作区一次 thread.list。
// workspace 是该批会话归属的工作区路径（跨工作区查询用 ?path=<路径> 显式指定）。
type threadsPayload struct {
	Workspace     string           `json:"workspace"`
	WorkspaceName string           `json:"workspaceName"`
	Threads       []threadListItem `json:"threads"`
	HasMore       bool             `json:"hasMore"`
	NextCursor    string           `json:"nextCursor"`
}

// modelPickerOption 是 /partials/model.json 里的一项（HEAD 模板 data-value/
// data-labels 等属性的 JSON 等价物；value 是 "providerId|modelId" 组合键）。
type modelPickerOption struct {
	Value          string   `json:"value"`
	ProviderID     string   `json:"providerId"`
	ModelID        string   `json:"modelId"`
	Label          string   `json:"label"`
	ShortLabel     string   `json:"shortLabel"`
	Current        bool     `json:"current"`
	ThinkingLevels []string `json:"thinkingLevels"`
	ThinkingLevel  string   `json:"thinkingLevel,omitempty"`
}

// modelPickerState 是 /partials/model.json 的响应（model.list 的展示投影）。
type modelPickerState struct {
	Models       []modelPickerOption `json:"models"`
	CurrentLabel string              `json:"currentLabel"`
	CurrentValue string              `json:"currentValue"`
	// CurrentLevels/CurrentLevel 是当前模型的推理档位与已选档位。
	CurrentLevels []string `json:"currentLevels,omitempty"`
	CurrentLevel  string   `json:"currentLevel,omitempty"`
}

// interactionQuestionState 是 form 交互里的一题（body JSON 投影；
// index 对应表单字段名 q0、q1…，与 handleInteraction 的 formAnswers 对齐）。
type interactionQuestionState struct {
	Index    int      `json:"index"`
	Question string   `json:"question"`
	Kind     string   `json:"kind"` // confirm | select | input
	Options  []string `json:"options,omitempty"`
}

// interactionViewState 是 /threads/{id}/state 里 interactions 的一项：
// 只保留 pending 的交互，字段与 HEAD interactions 子模板一致。
type interactionViewState struct {
	ID        string                     `json:"id"`
	Kind      string                     `json:"kind"`
	Title     string                     `json:"title"`
	Body      string                     `json:"body,omitempty"`
	Options   []string                   `json:"options,omitempty"`
	Questions []interactionQuestionState `json:"questions,omitempty"`
}

// dashboardState 是 /partials/dashboard.json 的数据形态。
type dashboardState struct {
	Stats  []overviewStat   `json:"stats"`
	Recent []overviewThread `json:"recent"`
}

type overviewStat struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Hint    string `json:"hint"`
	Tone    string `json:"tone"` // neutral | brand | warn
	Numeric bool   `json:"numeric,omitempty"`
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

// workspaceEntry 是目录浏览的一项（FilePicker /api/fs/list）。
type workspaceEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Href string `json:"href"`
}

// extensionsState 是插件列表岛与 /partials/extensions.json 共用的数据形态。
type extensionsState struct {
	Plugins []pluginRow `json:"plugins"`
	Count   int         `json:"count"`
	CSRF    string      `json:"csrf"`
}

// pluginRow 是插件列表的一行：同一个 id 的前端与后端能力合并成一条。
type pluginRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	APIVersion  int    `json:"apiVersion,omitempty"`
	Trust       string `json:"trust,omitempty"`
	Description string `json:"description"`
	Source      string `json:"source"`
	// Path 是第三方插件的绝对目录；随包插件为空。
	Path        string `json:"path,omitempty"`
	Status      string `json:"status"`
	StatusLabel string `json:"statusLabel"`
	StatusTone  string `json:"statusTone"`
	Error       string `json:"error"`
	Enabled     bool   `json:"enabled"`
	Frontend    bool   `json:"frontend"`
	Backend     bool   `json:"backend"`
	Tools       int    `json:"tools"`
	Hooks       int    `json:"hooks"`
	Skills      int    `json:"skills"`
	// Readme 是插件 README（无 README 时回退为清单说明）。
	Readme string `json:"readme,omitempty"`
	// Files 是插件包内文件清单（详情的「插件目录」）。
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

// pluginFileView 是 GET /extensions/{id}/file 返回的单个文件内容。
// 服务端不产出高亮/预览 HTML：前端拿原文自行渲染（或走 /markdown.ast）。
type pluginFileView struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SizeLabel string `json:"sizeLabel,omitempty"`
	Content   string `json:"content,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	// Markdown 标记该文件可按 Markdown 预览（前端据此提供「预览/源码」切换）。
	Markdown bool `json:"markdown,omitempty"`
}
