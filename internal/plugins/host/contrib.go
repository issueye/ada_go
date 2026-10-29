package host

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
)

// 本文件钉住「插件能贡献什么」与「宿主对外提供什么操作」两套类型。
//
// runtime.go（生命周期与预算）、caps.go（能力安装）、agentapi.go（agent 对象与结算）
// 都实现这里的接口，因此先把形状固定下来，避免三个方向各写一套。

var (
	toolNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	actionNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
)

// ToolDef 是 registerTool 的入参（05 §6.3）。
type ToolDef struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Parameters     json.RawMessage `json:"parameters"`
	Access         string          `json:"access"`
	TimeoutMS      int             `json:"timeoutMs,omitempty"`
	ParallelSafe   bool            `json:"parallelSafe,omitempty"`
	Repeatable     bool            `json:"repeatable,omitempty"`
	Rollback       bool            `json:"rollback,omitempty"`
	TerminatesTurn bool            `json:"terminatesTurn,omitempty"`
	// Handler 是脚本侧函数在宿主内的句柄；由 agentapi 填充，不来自 JSON。
	Handler HandlerRef `json:"-"`
}

// ProviderDef 是 registerProvider 的入参（仅 builtin/verified，03 §5.5）。
type ProviderDef struct {
	ID     string      `json:"id"`
	Models []ModelInfo `json:"models"`
	// Stream 是脚本侧 provider 的句柄；ctx.http 由 CapabilitySet 决定。
	Stream HandlerRef `json:"-"`
}

// ModelInfo 是 provider 声明的模型能力。
type ModelInfo struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"displayName,omitempty"`
	ContextWindow    int      `json:"contextWindow"`
	MaxOutput        int      `json:"maxOutput,omitempty"`
	Input            []string `json:"input,omitempty"`
	SupportsTools    bool     `json:"supportsTools"`
	SupportsThinking bool     `json:"supportsThinking,omitempty"`
}

// PromptDef 是 registerPrompt 的入参（05 §6.3）。
type PromptDef struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Priority int    `json:"priority,omitempty"`
}

// ActionDef 是 registerAction 的入参：只给界面调用，不进模型上下文（6.3）。
type ActionDef struct {
	Name      string     `json:"name"`
	Label     string     `json:"label"`
	Mode      string     `json:"mode"` // read | write
	Confirm   string     `json:"confirm,omitempty"`
	TimeoutMS int        `json:"timeoutMs,omitempty"`
	Handler   HandlerRef `json:"-"`
}

// RouteDef 是 route(method, path, handler) 登记的一条动态路由（7.6）。
// SSE=true 的路由只接受 GET，调用方以 EventSource 订阅，处理器用 ctx.sse.send 推帧。
type RouteDef struct {
	Method  string     `json:"method"`
	Path    string     `json:"path"`
	SSE     bool       `json:"sse,omitempty"`
	Handler HandlerRef `json:"-"`
}

// RouteEvent 是 SSE 路由的一帧：Event 为空表示默认 message。
type RouteEvent struct {
	Event string
	Data  json.RawMessage
}

// HookDef 是 agent.on(point, fn, opts) 登记的一个钩子。
type HookDef struct {
	Point    string `json:"point"`
	Priority int    `json:"priority,omitempty"`
	// Action 是该处理器实际使用的最高动作等级；装配期与清单声明比对。
	Action  string     `json:"action,omitempty"`
	Handler HandlerRef `json:"-"`
}

// HandlerRef 指向脚本侧的一个函数。宿主负责调用与预算，调用方只拿到句柄。
type HandlerRef struct {
	// Slot 是宿主内部的索引（脚本函数在 Runtime 里的登记位置）。
	Slot int
}

// Contribution 是一个插件在装配期登记的全部贡献（06 §6.5）。
//
// 运行期再调用 register* 只记警告，不改变当前目录；要生效必须重载插件。
type Contribution struct {
	PluginID   string
	Version    string
	Trust      plugins.Trust
	Tools      []ToolDef
	Providers  []ProviderDef
	Prompts    []PromptDef
	Actions    []ActionDef
	Routes     []RouteDef
	Hooks      []HookDef
	Frontend   FrontendContribution
	Skills     []domain.SkillMeta
	Components []string
}

// FrontendContribution 是清单 frontend 段的结算结果（模板路径已校验过）。
type FrontendContribution struct {
	Slots     []plugins.SlotContribution `json:"slots,omitempty"`
	Pages     []plugins.PageContribution `json:"pages,omitempty"`
	Nav       []plugins.NavContribution  `json:"nav,omitempty"`
	Dashboard []plugins.DashboardContribution
	Actions   map[string]plugins.ActionBinding
	Styles    []string
	Scripts   []string
	Assets    string
}

// Validate 做装配期校验：重名、命名规则、能力与钩子等级、插槽名。
//
// 任何一项失败都必须由调用方回滚整个插件（其它插件不受影响，6.5）。
func (c *Contribution) Validate(caps *CapabilitySet) error {
	if c == nil {
		return fmt.Errorf("host: 贡献为空")
	}
	if strings.TrimSpace(c.PluginID) == "" {
		return fmt.Errorf("host: 贡献缺少插件 id")
	}
	if !plugins.ValidID(c.PluginID) {
		return fmt.Errorf("host: 插件 id %q 非法", c.PluginID)
	}
	seenTool := map[string]bool{}
	for _, t := range c.Tools {
		switch {
		case !toolNameRe.MatchString(t.Name):
			return fmt.Errorf("host: %s 的工具名 %q 不满足 ^[a-z][a-z0-9_]{1,63}$", c.PluginID, t.Name)
		case seenTool[t.Name]:
			return fmt.Errorf("host: %s 重复注册工具 %q", c.PluginID, t.Name)
		case t.Handler.Slot == 0:
			return fmt.Errorf("host: 工具 %q 没有脚本句柄", t.Name)
		}
		switch domain.Access(t.Access) {
		case domain.AccessRead, domain.AccessWrite, domain.AccessExec, domain.AccessNetwork:
		default:
			return fmt.Errorf("host: 工具 %q 的 access %q 非法", t.Name, t.Access)
		}
		if len(t.Parameters) == 0 {
			return fmt.Errorf("host: 工具 %q 缺少 parameters Schema", t.Name)
		}
		if !json.Valid(t.Parameters) {
			return fmt.Errorf("host: 工具 %q 的 parameters 不是合法 JSON", t.Name)
		}
		seenTool[t.Name] = true
	}
	seenAction := map[string]bool{}
	for _, a := range c.Actions {
		if !actionNameRe.MatchString(a.Name) {
			return fmt.Errorf("host: %s 的动作名 %q 命名非法", c.PluginID, a.Name)
		}
		if seenAction[a.Name] {
			return fmt.Errorf("host: %s 重复注册动作 %q", c.PluginID, a.Name)
		}
		if a.Mode != "read" && a.Mode != "write" {
			return fmt.Errorf("host: 动作 %q 的 mode %q 必须是 read 或 write", a.Name, a.Mode)
		}
		if a.Mode == "write" && strings.TrimSpace(a.Confirm) == "" {
			return fmt.Errorf("host: 写动作 %q 缺少确认文案（7.6）", a.Name)
		}
		seenAction[a.Name] = true
	}
	for _, s := range c.Frontend.Slots {
		if !plugins.IsSlotName(s.Slot) {
			return fmt.Errorf("host: %s 声明了未知插槽 %q", c.PluginID, s.Slot)
		}
	}
	for _, h := range c.Hooks {
		point, ok := plugins.LookupHookPoint(h.Point)
		if !ok {
			return fmt.Errorf("host: %s 登记了未知钩子点位 %q", c.PluginID, h.Point)
		}
		level := plugins.HookAction(h.Action)
		if level == "" {
			level = plugins.ActionObserve
		}
		declared, ok := caps.Hooks[h.Point]
		if !ok {
			return fmt.Errorf("host: %s 未在清单声明点位 %q，运行期拒绝调用（6.2）", c.PluginID, h.Point)
		}
		if level.Rank() > declared.Rank() {
			return fmt.Errorf("host: %s 在 %s 上用了 %s，清单只声明到 %s", c.PluginID, h.Point, level, declared)
		}
		if !point.AllowsAction(level, c.Trust == plugins.TrustBuiltin) {
			return fmt.Errorf("host: %s（trust=%s）不能在 %s 上使用 %s", c.PluginID, c.Trust, h.Point, level)
		}
	}
	if len(c.Providers) > 0 && c.Trust != plugins.TrustBuiltin && c.Trust != plugins.TrustVerified {
		return fmt.Errorf("host: %s 是 %s，不能注册 provider（05 §6.3）", c.PluginID, c.Trust)
	}
	for _, p := range c.Providers {
		if !caps.Has(plugins.CapNetSSE) {
			return fmt.Errorf("host: provider %q 需要 net.sse 能力（03 §5.5）", p.ID)
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("host: provider %q 没有声明任何模型", p.ID)
		}
	}
	return nil
}

// Descriptor 把 ToolDef 变成引擎侧描述符（02 §5.4）。
func (t ToolDef) Descriptor(pluginID string) domain.Descriptor {
	timeout := t.TimeoutMS
	if timeout <= 0 {
		timeout = int(DefaultToolTimeout / time.Millisecond)
	}
	return domain.Descriptor{
		Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		Access: domain.Access(t.Access), Timeout: time.Duration(timeout) * time.Millisecond,
		ParallelSafe: t.ParallelSafe, Repeatable: t.Repeatable, Rollback: t.Rollback,
		TerminatesTurn: t.TerminatesTurn, PluginID: pluginID,
	}
}

// Summary 返回供 RPC 与页面展示的摘要（不含脚本句柄）。
func (c *Contribution) Summary(enabled bool, fingerprint, lastError string) plugins.Summary {
	s := plugins.Summary{
		ID: c.PluginID, Version: c.Version, Trust: string(c.Trust), Enabled: enabled,
		Fingerprint: fingerprint, LastError: lastError, Status: "ready",
	}
	if lastError != "" {
		s.Status = "degraded"
	}
	for _, t := range c.Tools {
		s.Tools = append(s.Tools, t.Name)
	}
	for _, h := range c.Hooks {
		s.Hooks = append(s.Hooks, h.Point)
	}
	for _, slot := range c.Frontend.Slots {
		s.Slots = append(s.Slots, slot.Slot)
	}
	for _, skill := range c.Skills {
		s.Skills = append(s.Skills, skill.Name)
	}
	sort.Strings(s.Tools)
	sort.Strings(s.Hooks)
	sort.Strings(s.Slots)
	sort.Strings(s.Skills)
	return s
}

// Plugin 是一个已加载插件的宿主句柄：调用脚本函数、关闭并重建。
//
// 一个 Plugin 绑定一个 moejs Runtime 与一条 goroutine（06 §6.4）：所有调用通过
// 带缓冲的队列串行进入，Runtime 的值不跨 goroutine。ports.ScriptHost 依赖本接口。
type Plugin interface {
	// ID 是插件 id（等于目录名）。
	ID() string
	// Manifest 是已校验的清单。
	Manifest() *plugins.Manifest
	// Contribution 是装配期结算的贡献集合（只读）。
	Contribution() *Contribution
	// CallTool 执行一次工具调用：args 与返回值都只走 JSON；预算取描述符 timeoutMs。
	CallTool(ctx context.Context, h HandlerRef, args json.RawMessage, callCtx Context) (domain.Result, error)
	// CallHook 执行一次钩子：默认 200ms、上限 2s；等待 UI 的时间不计入预算。
	CallHook(ctx context.Context, h HandlerRef, event ports.HookEvent, hookCtx Context) (ports.HookOutcome, error)
	// CallAction 执行一次前端动作：默认 5s，结果上限 1MB。
	CallAction(ctx context.Context, h HandlerRef, input json.RawMessage, actCtx Context) (json.RawMessage, error)
	// CallRoute 执行一次动态路由处理：默认 5s，响应上限 64KB。
	CallRoute(ctx context.Context, h HandlerRef, req json.RawMessage, routeCtx Context) (json.RawMessage, error)
	// CallProviderStream 执行 provider 的 stream：长任务，预算为建连 15s/空闲 90s/总 30 分钟。
	CallProviderStream(ctx context.Context, h HandlerRef, req json.RawMessage, streamCtx Context, onDelta func(json.RawMessage) error) error
	// Close 关闭 Runtime 并取消它启动的所有异步工作（不泄漏 goroutine）。
	Close(ctx context.Context) error
	// Dirty 报告 Runtime 是否已被一次失败弄脏（需要重建）。
	Dirty() bool
}
