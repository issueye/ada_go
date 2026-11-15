package web

import "encoding/json"

// ---- Core 返回的最小 DTO（只声明 JSON 端点用到的字段）----

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
	// Running 是该会话是否有未终态的轮次（thread.list 的 Core 内存态投影）。
	Running bool `json:"running"`
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
	// Usage 是本轮累计用量（Core 的 runInfo.usage）。输入框下方的
	// tok/s 与缓存命中率读它；为空表示本轮还没有用量事件。
	Usage *runUsageDTO `json:"usage,omitempty"`
}

// runUsageDTO 是 run.usage 的投影（字段与 domain.Usage / proto.Usage 同名）。
type runUsageDTO struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	CacheTokens  int64 `json:"cacheTokens"`
	CostMicros   int64 `json:"costMicros"`
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

type contextStatsDTO struct {
	// Tokens 是三段合计（系统提示 + 工具声明 + 对话消息），Percent 以它为准。
	Tokens   int `json:"tokens"`
	Messages int `json:"messages"`
	System   int `json:"system"`
	Tools    int `json:"tools"`
	Window   int `json:"window"`
	Percent  int `json:"percent"`
}

type threadGetResult struct {
	Thread       threadDTO        `json:"thread"`
	Items        []itemDTO        `json:"items"`
	Seq          int64            `json:"seq"`
	Run          runStateDTO      `json:"run"`
	Context      *contextStatsDTO `json:"context"`
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
