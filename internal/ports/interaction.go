package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// InteractionRequest 是一次通用用户交互（confirm/select/input，05 §6.3 UIContext、
// 03 §5.6；id/类型/标题/正文/选项/截止时间即 08 §8.2 evt.interaction.requested 的载荷，
// 存储形状是 04 §5.7 interactions 表）。它不含审批语义——审批规则属于官方默认策略插件。
type InteractionRequest struct {
	// ID 由 IDGen.NewInteractionID 生成。
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId,omitempty"`
	CallID   string `json:"callId,omitempty"`
	// PluginID 是发起交互的插件（官方策略插件或 ask 类工具插件）。
	PluginID  string                 `json:"pluginId"`
	Kind      domain.InteractionKind `json:"kind"`
	Title     string                 `json:"title"`
	Body      string                 `json:"body"`
	Options   []string               `json:"options,omitempty"`
	Payload   json.RawMessage        `json:"payload,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
	ExpiresAt time.Time              `json:"expiresAt"`
}

// InteractionResolution 是恰好一次解析后的交互结果（evt.interaction.resolved 载荷形状）。
type InteractionResolution struct {
	RequestID string                   `json:"requestId"`
	Status    domain.InteractionStatus `json:"status"`
	// Value：confirm 为 "true"/"false"；select 为所选 option；input 为文本。
	Value      string    `json:"value,omitempty"`
	ResolvedAt time.Time `json:"resolvedAt"`
}

// InteractionBroker 是通用用户交互端口：请求、等待、恰好解析一次（01 §5.2、03 §5.6）。
// 语义约束：
//   - 恰好解析一次：pending → accepted|denied|cancelled|timeout|unavailable（04 interactions.status）；
//   - 无 UI（CLI/断线）解析为 unavailable；超期解析为 timeout；
//     INV-4：两者都必须被调用方当作拒绝（domain.FailClosed），基座不会替它放行；
//   - 待处理交互持久化，Gateway/Core 重启后可恢复（03 §5.6）；
//   - Respond 必须同时匹配 threadID 与 requestID，只接受 pending 且未过期的请求；
//     重复响应幂等返回首次结果，过期/伪造返回错误且不改变状态；
//   - 同一线程最多一个待处理交互（03 §5.6）；等待中的实例不被空闲回收（01 §4.2）。
//
// 生产实现：internal/coreproto 事件 + Gateway 托盘 + 04 interactions 表（M1-A/M2）。
// 测试替身：internal/testsupport 的脚本化 broker——按序注入 accepted/denied/timeout/unavailable
// 以驱动失败方向测试（10 §11.5）。
type InteractionBroker interface {
	// Request 发起交互并阻塞等待解析；ctx 取消时解析为 cancelled（03 §5.6 取消段）。
	// 默认期限 domain.DefaultInteractionTimeout（300s）。
	Request(ctx context.Context, req InteractionRequest) (InteractionResolution, error)
	// Respond 提交响应（用户点选/表单/幂等重放）；返回首次解析结果。
	// status 只能是 accepted/denied/cancelled；timeout/unavailable 由实现方在
	// 期限到/无 UI 时自行写入，调用方不得伪造（03 §5.6）。
	Respond(ctx context.Context, threadID, requestID string, status domain.InteractionStatus, value string) (InteractionResolution, error)
	// ListPending 列出线程的待处理交互（重启恢复、interaction.list，08 §8.2）。
	ListPending(ctx context.Context, threadID string) ([]InteractionRequest, error)
	// HasUI 报告当前线程是否存在可交互的界面（05 §6.3 ctx.ui.available 的基座侧真源；
	// false 时 Request 立即解析为 unavailable）。
	HasUI(ctx context.Context, threadID string) bool
}
