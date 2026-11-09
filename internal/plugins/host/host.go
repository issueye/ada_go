// Package host 是插件的 moejs 宿主：每个插件一个 Runtime、一条专用 goroutine，
// 只安装清单声明过的能力，运行期通过 ports.ScriptHost 与引擎打交道。
//
// 边界（设计 1.5.2、6.4）：
//   - 引擎不知道 moejs，插件不知道宿主句柄：中间只有本文件定义的 Contribution 与 HostCall。
//   - 官方插件与第三方插件走同一条加载路径，差别只有 trust 与随包发布。
//   - 没有声明的能力在脚本里根本不存在；声明了但成员越权是「调用即抛 ERR_ACCESS_DENIED」。
//
// 与上游的对应关系（实测见 docs/11-structure-roadmap.md D-7、tools/moejspike）：
// 模块白名单与能力位映射由本包的 resolver 负责；node:fs 用上游实现 + 宿主包装；
// node:child_process 由宿主提供（上游 alpha.11 没有这个模块）。
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
)

// 默认预算（06 §6.4、09 §9.5）。工具默认 120s，钩子默认 200ms 上限 2s，脚本调用 5s。
const (
	DefaultToolTimeout   = 120 * time.Second
	DefaultHookTimeout   = 200 * time.Millisecond
	MaxHookTimeout       = 2 * time.Second
	DefaultCallTimeout   = 5 * time.Second
	DefaultActionTimeout = 5 * time.Second

	// MemoryLimit 是每插件 Runtime 的脚本堆上限（64MB）。
	MemoryLimit = 64 << 20
	// MaxCallDepth 与 StackSize 对应 moejs 的构造期边界。
	MaxCallDepth = 2000
	StackSize    = 1 << 16

	// MaxResultBytes 是单次调用的可序列化结果上限（1MB，05 §6.3）。
	MaxResultBytes = 1 << 20
	// ConsecutiveFailureLimit 是自动禁用插件前的连续失败次数（06 §6.4）。
	ConsecutiveFailureLimit = 3

	// DisabledCooldownInit 是熔断禁用后的首次冷却时长：冷却结束后下一次调用
	// 自动半开探测（重建运行时并放行该调用），成功即恢复，失败则指数退避。
	DisabledCooldownInit = 5 * time.Minute
	// DisabledCooldownMax 是半开探测反复失败时的退避上限。
	DisabledCooldownMax = 10 * time.Minute
)

// ErrDirtyRuntime 表示这次调用把 Runtime 弄脏了（超时、超内存、引擎内部错误、panic）：
// 本次调用已失败，插件降级为 degraded，下一次调用前重建 Runtime（06 §6.4 回收策略）。
var ErrDirtyRuntime = errors.New("host: 插件运行时需重建")

// Script 是一个待加载插件的编译产物与清单。
//
// Source 是 esbuild 之后的 ESM bundle（官方插件构建期预编译，第三方安装期编译）；
// 宿主不做 TypeScript 编译，也不做磁盘 node_modules 解析（6.10）。
type Script struct {
	Manifest *plugins.Manifest
	// EntryName 是入口模块的虚拟名，形如 plugin://<id>/index.js。
	EntryName string
	// Source 是入口模块源码。
	Source string
	// Bundle 是随包的其它模块（specifier → 源码）；loader 只在其中解析，不落盘。
	Bundle map[string]string
	// StaticAssets 是插件包内静态资源的只读视图（plugin://<id>/…）。
	StaticAssets map[string][]byte
	// Schemas 是前端 block 数据 Schema 的原文（清单路径 → JSON 字节）。
	Schemas map[string][]byte
	// Root 是插件包在宿主上的绝对目录（可选）；技能路径按它拼出 SKILL.md 的绝对路径。
	Root string
	// Readme 是插件包内 README.md 的原文（可选；详情页展示用）。
	Readme string
}

// CapabilitySet 是装配期算出的「这个插件实际能拿到什么」。
//
// 它是清单与信任级别求交的结果：清单写了 exec 但信任是 third-party，这里就没有 exec。
type CapabilitySet struct {
	// PluginID 是清单 id；fs 写插件目录时用于区分「自己的目录」与「别人的目录」。
	PluginID string
	// ConfigNamespace 是 ctx.config 的命名空间（缺省插件 id；可用清单 backend.configNamespace 覆盖）。
	ConfigNamespace string
	Trust           plugins.Trust
	Backend         map[plugins.Capability]bool
	Frontend        map[plugins.Capability]bool
	// Secrets 是清单 backend.secrets 声明、且宿主确实配置了的密钥名。
	Secrets []string
	// Env 是清单 backend.env 白名单与宿主环境的交集；缺失的名字不注入（6.2）。
	Env map[string]string
	// Hooks 是点位 → 该插件使用的最高动作等级。
	Hooks map[string]plugins.HookAction
	// WorkspaceRoot 是宿主真实工作区根（fs 的 Root；脚本里挂到 Mount）。
	WorkspaceRoot string
	// Mount 是脚本视图里的工作区绝对路径，固定 "/workspace"。
	Mount string
}

// NewCapabilitySet 由清单与宿主环境算出实际授予集合。
func NewCapabilitySet(m *plugins.Manifest, workspaceRoot string, hostEnv map[string]string, availableSecrets func(string) bool) (*CapabilitySet, error) {
	if m == nil {
		return nil, fmt.Errorf("host: 缺少清单")
	}
	c := &CapabilitySet{
		PluginID:        m.ID,
		ConfigNamespace: configNamespaceOf(m),
		Trust:           m.Trust,
		Backend:         map[plugins.Capability]bool{},
		Frontend:        map[plugins.Capability]bool{},
		Hooks:           map[string]plugins.HookAction{},
		Env:             map[string]string{},
		WorkspaceRoot:   workspaceRoot,
		Mount:           DefaultMount,
	}
	if m.Backend != nil {
		for _, cap := range m.Backend.Capabilities {
			if !cap.TrustAllows(c.Trust) {
				return nil, fmt.Errorf("host: %s 信任级别 %s 不允许能力 %s（6.2）", m.ID, m.Trust, cap)
			}
			c.Backend[cap] = true
		}
		for _, name := range m.Backend.Env {
			// 宿主白名单语义：只交付清单声明的名字，仍不伪造空串。
			// 组合根注入的 HostEnv 优先（测试与嵌入场景可冻结特定值）；没有注入时
			// 回退到宿主进程环境，否则 backend.env 在生产装配里等于不存在
			// （MCP 的 ~/.adago/mcp.json 与 stdio 子进程环境都依赖它）。
			if v, ok := hostEnv[name]; ok {
				c.Env[name] = v
				continue
			}
			if v, ok := os.LookupEnv(name); ok {
				c.Env[name] = v
			}
		}
		for _, name := range m.Backend.Secrets {
			if availableSecrets == nil || availableSecrets(name) {
				c.Secrets = append(c.Secrets, name)
			}
		}
		for point, level := range m.Backend.Hooks {
			c.Hooks[point] = level
		}
	}
	if m.Frontend != nil {
		for _, cap := range m.Frontend.Capabilities {
			if !cap.TrustAllows(c.Trust) {
				return nil, fmt.Errorf("host: %s 信任级别 %s 不允许前端能力 %s（6.2）", m.ID, m.Trust, cap)
			}
			c.Frontend[cap] = true
		}
	}
	return c, nil
}

// CallMeta 是一次嵌套工具调用的归属（tool.discovery 宿主桥用）。
type CallMeta struct {
	ThreadID string
	RunID    string
	CallID   string
}

// EventQuery 是 ctx.events.query 的入参（字段与 05 §6.3 对齐）。
type EventQuery struct {
	ThreadID string
	FromSeq  int64
	Types    []string
	Limit    int
}

// EventView 是一条只读事件投影；Data 原样是事件载荷 JSON。
type EventView struct {
	Seq      int64           `json:"seq"`
	Type     string          `json:"type"`
	ThreadID string          `json:"threadId,omitempty"`
	At       time.Time       `json:"at"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// UsageQuery 是 ctx.events.usage 的入参：ThreadID 为空表示当前工作区全部原始行。
type UsageQuery struct {
	ThreadID string
	Limit    int
}

// UsageRow 是一条原始用量行（每轮 provider/model 计数），基座不做展示口径聚合。
type UsageRow struct {
	ThreadID     string    `json:"threadId"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CacheTokens  int64     `json:"cacheTokens"`
	CostMicros   int64     `json:"costMicros"`
	At           time.Time `json:"at"`
}

// TurnRequest 是 ctx.turn.invoke 的入参：threadId 为空时新建线程。
type TurnRequest struct {
	ThreadID string `json:"threadId,omitempty"`
	Title    string `json:"title,omitempty"`
	Prompt   string `json:"prompt"`
}

// TurnResult 是轮次触发桥的即时回执：只表示已受理，不表示轮次结束。
type TurnResult struct {
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
}

// DispatchRequest 是 ctx.dispatch.send 的入参（capability: dispatch.send）。
//
// 刻意没有 sender 字段：身份由宿主按能力集盖章，脚本给什么都不影响归属。
type DispatchRequest struct {
	// Workspace 为空表示当前工作区（Core 自己的默认工作区）。
	Workspace string `json:"workspace,omitempty"`
	// ThreadID 为空表示由调度层新建线程。
	ThreadID string `json:"threadId,omitempty"`
	Title    string `json:"title,omitempty"`
	Text     string `json:"text,omitempty"`
	// Mode 是 auto|start|steer；空同 auto（插件通常用 start，避免插进别人的轮次）。
	Mode string `json:"mode,omitempty"`
	// IdempotencyKey 用于「同一任务同一时刻只触发一次」；空由调度层生成一次性键。
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// TaskID 只进审计（如 cron 任务 id），不参与路由。
	TaskID string `json:"taskId,omitempty"`
}

// DispatchResult 是投递回执：调度层已受理，不表示轮次结束。
type DispatchResult struct {
	Workspace string `json:"workspace"`
	ThreadID  string `json:"threadId"`
	RunID     string `json:"runId,omitempty"`
	Mode      string `json:"mode"`
}

// ProcessSpec 是 ctx.processes.register 的入参（capability: process.supervise）。
//
// 刻意没有 owner 字段：归属由宿主按能力集盖章。Entry 是插件内相对路径，
// 宿主负责解析到插件目录（越界或不存在直接拒绝），再交给网关的 ProcessManager。
type ProcessSpec struct {
	Name string `json:"name"`
	// Scope 是 global|workspace；缺省 global。workspace 进程的 cwd 是当前工作区。
	Scope string `json:"scope,omitempty"`
	// Runtime 是 node|binary；缺省 node（插件脚本就是 JS）。
	Runtime string `json:"runtime,omitempty"`
	// Entry 是相对插件目录的脚本路径（node）或可执行文件名（binary）。
	Entry string            `json:"entry"`
	Args  []string          `json:"args,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	// Start 是 on_enable|lazy；缺省 lazy。
	Start string `json:"start,omitempty"`
	// Restart 是 no|on_failure|always；缺省 no。
	Restart string `json:"restart,omitempty"`
	// StopTimeoutMs 是停止等待上限；0 表示用网关默认值。
	StopTimeoutMs int `json:"stopTimeoutMs,omitempty"`
}

// ProcessStatus 是进程的可观测快照（网关 ProcessManager 的子集）。
type ProcessStatus struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Workspace string `json:"workspace,omitempty"`
	Runtime   string `json:"runtime"`
	PID       int    `json:"pid,omitempty"`
	Running   bool   `json:"running"`
	Restarts  int    `json:"restarts"`
	Refs      int    `json:"refs"`
	StartMode string `json:"startMode"`
	Restart   string `json:"restart"`
	LastError string `json:"lastError,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
}

// ProcessSupervisor 是进程监督桥的宿主侧接口。
//
// 它只有注册/取消注册/确保运行/停止/查状态：没有 stdin/stdout、没有日志流、
// 没有任意命令行。日志由网关侧持有，插件通过网关日志读，不在这里开通道。
//
// 每个方法都显式带 pluginID：归属由宿主按能力集盖章后传进来，而不是实现自己
// 记住某一次装配的 id——Context 是每调用的，实现不能假设它不变。
type ProcessSupervisor interface {
	Register(ctx context.Context, pluginID string, spec ProcessSpec) (ProcessStatus, error)
	Ensure(ctx context.Context, pluginID, name, scope string) (ProcessStatus, error)
	Stop(ctx context.Context, pluginID, name, scope string) (ProcessStatus, error)
	Status(ctx context.Context, pluginID, name, scope string) (ProcessStatus, bool)
	Unregister(ctx context.Context, pluginID, name, scope string) error
	List(ctx context.Context, pluginID string) ([]ProcessStatus, error)
}

func configNamespaceOf(m *plugins.Manifest) string {
	if m == nil {
		return ""
	}
	if m.Backend != nil && m.Backend.ConfigNamespace != "" {
		return m.Backend.ConfigNamespace
	}
	return m.ID
}

// DefaultMount 是脚本视图里工作区的挂载点（12 §6.12.1：宿主绝对路径不进脚本）。
const DefaultMount = "/workspace"

// Has 报告后端能力是否被授予。
func (c *CapabilitySet) Has(cap plugins.Capability) bool { return c.Backend[cap] }

// Context 是一次调用注入的宿主上下文。
//
// 引擎只交出这些字段，插件拿不到数据库句柄、密钥文件、原始 socket 或进程（1.5.2）。
type Context struct {
	Workspace string
	// ConfigDir 是 ctx.config 的机器级配置根目录（显式 --data-dir 隔离）；空时用全局配置根。
	ConfigDir string
	// OnConfigChange 在 ctx.config 写/删后回调（命名空间名）；宿主可据此刷新派生缓存。
	OnConfigChange func(namespace string)
	// EnvVars 是用户管理的环境变量（~/.adago/env.json + <workspace>/.adago/env.json），
	// 注入到插件启动的所有子进程（exec / 后台任务 / process.start），同名覆盖宿主环境；
	// 不进入插件 process.env（那里仍按清单 backend.env 白名单）。值不写日志。
	EnvVars  map[string]string
	ThreadID string
	RunID    string
	CallID   string
	PluginID string
	Now      time.Time
	Signal   context.Context // 取消贯穿：ctx 取消即中止脚本与宿主长调用。

	// Secret 只在清单声明过该名字时返回值；否则第二返回值为 false。
	Secret func(name string) (string, bool)
	// State 是插件 KV（capability: state）。
	State func(ctx context.Context, op StateOp) (json.RawMessage, error)
	// Events 是只读事件/用量查询（capability: events.read）；nil 表示未授予。
	// 查询被限制在调用线程上下文内，不能跨线程/工作区，也不能写事件。
	Events func(ctx context.Context, q EventQuery) ([]EventView, error)
	// Usage 返回工作区内的原始用量行（capability: events.read）；nil 表示未授予。
	// threadID 为空表示整个工作区；基座不在这里做展示口径聚合。
	Usage func(ctx context.Context, q UsageQuery) ([]UsageRow, error)
	// TurnInvoke 触发一轮对话（capability: turn.invoke，仅 builtin）；nil 表示未授予。
	// 宿主负责建线程/生成 runId/异步执行；调用只返回受理回执。
	TurnInvoke func(ctx context.Context, req TurnRequest) (TurnResult, error)
	// Catalog 返回当前工具目录快照（capability: tool.discovery，仅 builtin）。
	Catalog func(ctx context.Context) ([]domain.Descriptor, error)
	// InvokeTool 按内层工具描述符重跑审批链后执行（capability: tool.discovery，仅 builtin）。
	InvokeTool func(ctx context.Context, meta CallMeta, call domain.ToolCall) (domain.Result, error)
	// Dispatch 把消息投递给调度层（capability: dispatch.send，仅 builtin）；nil 表示
	// 本 Core 未接入网关调度层（CLI 直连、无网关的部署）。
	//
	// 插件给的是「发给谁、发什么」，身份由宿主盖章（PluginID 来自能力集，
	// 脚本不能自填）；投递语义（建线程 / 空闲 start / 运行中 steer）由调度层决定。
	Dispatch func(ctx context.Context, req DispatchRequest) (DispatchResult, error)
	// Processes 是进程监督桥（capability: process.supervise，仅 builtin）；nil 表示
	// 本 Core 未接入网关 ProcessManager（CLI 直连、无网关的部署）。
	//
	// 插件给的是「跑什么脚本、什么启动/重启策略」；owner 由宿主按能力集盖章，
	// entry 由宿主解析到插件目录内并拒绝越界，进程本身由网关拉起（不走 Core 进程）。
	Processes ProcessSupervisor
	// UI 是通用交互桥（capability: ui.ask）；nil 表示未授予。
	UI func(ctx context.Context, req UIRequest) (UIResolution, error)
	// HTTP 是 provider 的出网桥（capability: net.fetch / net.sse）；nil 表示未授予。
	HTTP HTTPBridge
	// Log 写结构化日志；密钥一律脱敏。
	Log func(level, msg string, fields map[string]any)
	// Skills 返回当前装配里全部技能的只读元数据（6.7：name/description/path，不含正文）。
	// 这是基座向插件开放的唯一技能通道（技能搜索类工具用它）；
	// nil 表示本次装配未启用技能，ctx.skills 不存在。
	Skills func() []domain.SkillMeta
	// Checkpoint 把写入路径并入检查点（回滚用，M5）。
	//
	// 调用时机是「覆盖写入之前」：回调负责把被覆盖文件的当前内容备份进数据目录
	// changes/<checkpointId>/（内容 + mode）。meta 携带本笔调用的归属；CallID 为空
	// 表示本次写入不在 Rollback=true 的工具调用里，回调直接忽略。
	Checkpoint func(meta CheckpointMeta, paths ...string)
	// Settings 是该插件自己的设置值（13 §附录 B：[plugins.settings.<id>] 解析后的对象）。
	// 基座不解释字段，只按插件 id 透传给脚本（ctx.settings）。
	Settings map[string]any
	// Providers 返回供应商/模型目录（capability: model.admin）；nil 表示未授予。
	// 目录不含密钥；Active 由 Current 表达。
	Providers func() ProviderCatalog
	// SelectModel 切换当前模型（capability: model.admin）。
	// threadID 为空表示工作区级；否则先校验线程存在再写线程级覆盖。
	SelectModel func(ctx context.Context, providerID, modelID, threadID string) (ModelRef, error)
	// InstallPlugin 把一份插件骨架写入全局插件根（capability: plugin.admin；仅 builtin）。
	// 宿主负责 id/相对路径校验、大小上限与「不覆盖已有插件」；脚本拿不到任意文件写权限。
	InstallPlugin func(ctx context.Context, spec PluginInstallSpec) (PluginInstallResult, error)
	// CheckPlugin 汇报插件的健康状态（capability: plugin.admin；仅 builtin）。
	// pluginID 为空时返回全部插件的摘要；非空时返回单个插件的完整诊断报告
	// （发现位置、清单、启停决策、装配状态与入口预加载结果）。
	CheckPlugin func(ctx context.Context, pluginID string) (PluginCheckResult, error)
	// RemovePlugin 删除一个已安装插件的目录（capability: plugin.admin；仅 builtin）。
	// 工作区副本优先于全局副本；随包插件不可移除。生效需重载。
	RemovePlugin func(ctx context.Context, pluginID string) (PluginRemoveResult, error)
}

// PluginFileSpec 是安装骨架里的一个文件：path 是插件目录内的相对路径。
type PluginFileSpec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// PluginInstallSpec 是 ctx.pluginAdmin.install 的入参。
type PluginInstallSpec struct {
	ID    string           `json:"id"`
	Files []PluginFileSpec `json:"files"`
}

// PluginInstallResult 是安装结果：dir 是全局根下的插件目录（本机绝对路径，
// 仅用于展示，脚本没有它的读写能力）。
type PluginInstallResult struct {
	ID    string   `json:"id"`
	Dir   string   `json:"dir"`
	Files []string `json:"files"`
}

// PluginCheckSummary 是插件健康清单里的一行（check_plugin 不带 id 时）。
type PluginCheckSummary struct {
	ID        string `json:"id"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status"`
	Enabled   bool   `json:"enabled"`
	LastError string `json:"lastError,omitempty"`
}

// PluginCheckItem 是诊断报告里的一项检查。
type PluginCheckItem struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// PluginCheckReport 是单个插件的完整诊断报告。
type PluginCheckReport struct {
	ID           string            `json:"id"`
	Found        bool              `json:"found"`
	Name         string            `json:"name,omitempty"`
	Version      string            `json:"version,omitempty"`
	Trust        string            `json:"trust,omitempty"`
	Source       string            `json:"source,omitempty"` // bundled | global | workspace
	Dir          string            `json:"dir,omitempty"`
	Entry        string            `json:"entry,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Enabled      bool              `json:"enabled"`
	Status       string            `json:"status,omitempty"`
	LastError    string            `json:"lastError,omitempty"`
	Tools        []string          `json:"tools,omitempty"`
	Files        []string          `json:"files,omitempty"`
	Checks       []PluginCheckItem `json:"checks,omitempty"`
}

// PluginCheckResult 是 ctx.pluginAdmin.check 的返回：id 为空时只有 Plugins 摘要。
type PluginCheckResult struct {
	Plugins []PluginCheckSummary `json:"plugins,omitempty"`
	Report  *PluginCheckReport   `json:"report,omitempty"`
}

// PluginRemoveResult 是移除结果：source 说明删掉的是哪一份副本。
type PluginRemoveResult struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"`
	Source string `json:"source"` // workspace | global
}

// ModelRef 是当前模型的引用。
type ModelRef struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	// ThinkingLevel 是当前选择的推理档位；空表示不传。
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

// ProviderCatalog 是 model.admin 能力看到的供应商/模型目录（不含端点密钥）。
type ProviderCatalog struct {
	Providers []ProviderInfo `json:"providers"`
	Current   ModelRef       `json:"current"`
}

// ProviderInfo 是单个供应商的只读投影。
type ProviderInfo struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"displayName,omitempty"`
	Protocol    string            `json:"protocol,omitempty"`
	BaseURL     string            `json:"baseUrl,omitempty"`
	Proxy       string            `json:"proxy,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	Available   bool              `json:"available"`
	// 下列字段只给供应商管理插件的 /models 拉取使用；不含密钥。
	DefaultBaseURL   string          `json:"defaultBaseUrl,omitempty"`
	AuthHeader       string          `json:"authHeader,omitempty"`
	AuthPrefix       string          `json:"authPrefix,omitempty"`
	APIVersionHeader string          `json:"apiVersionHeader,omitempty"`
	APIVersion       string          `json:"apiVersion,omitempty"`
	HasAPIKey        bool            `json:"hasApiKey"`
	APIKeyHint       string          `json:"apiKeyHint,omitempty"`
	Models           []ProviderModel `json:"models"`
}

// ProviderModel 是可编辑的模型配置（含启用状态）。
type ProviderModel struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"displayName,omitempty"`
	ContextWindow    int      `json:"contextWindow,omitempty"`
	MaxOutput        int      `json:"maxOutput,omitempty"`
	Input            []string `json:"input,omitempty"`
	SupportsTools    bool     `json:"supportsTools"`
	SupportsThinking bool     `json:"supportsThinking,omitempty"`
	// ThinkingLevels 是该模型可选的推理等级（从低到高）。
	ThinkingLevels []string `json:"thinkingLevels,omitempty"`
	Disabled       bool     `json:"disabled,omitempty"`
}

// CheckpointMeta 是一次检查点登记的归属（M5）。
//
// Executor 在工具执行前已知 threadId/runId/tool/callId，经 ctx → app 的 Invoker →
// host.Context 传到这里；fs 写入回调把它写进检查点 manifest 的账目里。
type CheckpointMeta struct {
	ThreadID string
	RunID    string
	CallID   string
	Tool     string
}

// StateOp 是一次插件 KV 访问。
type StateOp struct {
	// PluginID 是发起访问的插件；宿主在调用前补齐，存储层用它做命名空间（04 §5.7）。
	PluginID string
	// ThreadID 是本次调用归属的线程（工具/路由上下文）；为空时由宿主回退到当前线程。
	// 它让 route 这类没有轮次 ctx 的调用也能读写正确的线程命名空间。
	ThreadID string
	Kind     string // get | set | delete | list
	Key      string
	Value    json.RawMessage
	Prefix   string
}

// UIRequest 是一次通用用户交互（05 §6.3 UIContext）。
type UIRequest struct {
	// PluginID 是发起交互的插件：交互账目与事件载荷都要它（谁问的、谁负责）。
	PluginID  string
	Kind      domain.InteractionKind
	Title     string
	Message   string
	Options   []string
	TimeoutMS int
	// Payload 是 kind=form 的题目数组（JSON）；其它形态为空。
	Payload json.RawMessage
}

// UIResolution 是交互结果；调用方必须把 timeout/unavailable 当作拒绝（INV-4）。
type UIResolution struct {
	Status domain.InteractionStatus
	Value  string
}

// HTTPBridge 是宿主执行的出站调用（provider 唯一出口，03 §5.5）。
type HTTPBridge interface {
	// JSON 发一次请求并返回解析后的响应体；协议、大小、超时、重定向由宿主执行。
	JSON(ctx context.Context, method, url string, headers map[string]string, body string, maxBytes int) (HTTPResult, error)
	// SSE 逐事件回调，直到显式终止或 ctx 取消。
	SSE(ctx context.Context, method, url string, headers map[string]string, body string, onEvent func(event, data string) error) (HTTPResult, error)
	// JSONWithProxy / SSEWithProxy 使用供应商配置的代理；空代理等价于 JSON/SSE。
	JSONWithProxy(ctx context.Context, method, url string, headers map[string]string, body string, maxBytes int, proxy string) (HTTPResult, error)
	SSEWithProxy(ctx context.Context, method, url string, headers map[string]string, body string, onEvent func(event, data string) error, proxy string) (HTTPResult, error)
}

// HTTPResult 是出网调用的摘要结果。
type HTTPResult struct {
	Status    int
	Headers   map[string][]string
	Body      json.RawMessage
	SSEEvents int
}

// DomainTrust 把契约层的信任级别映射到 domain 层同义类型（两处定义是历史包袱，
// M1 里保持 domain 不 import plugins，由宿主在边界处转换）。
func (c *CapabilitySet) DomainTrust() domain.Trust {
	switch c.Trust {
	case plugins.TrustBuiltin:
		return domain.TrustBuiltin
	case plugins.TrustVerified:
		return domain.TrustVerified
	default:
		return domain.TrustThirdParty
	}
}

// InstallCapabilities 是「能力安装」的挂载点，由 caps.go 在同包内赋值（init）。
//
// 拆成函数变量的原因：Runtime 生命周期（runtime.go）与能力安装（caps.go）由两条
// 并行工作流实现，用变量解耦可以让两边各自编译与测试；装配路径必须走同一份实现，
// 因此这里只允许一个赋值点。未赋值时宿主拒绝加载插件，而不是静默不装能力。
var InstallCapabilities func(rt *mojsRuntime, loop *mojsLoop, caps *CapabilitySet, hc Context) error

// mojsRuntime / mojsLoop 是 moejs 句柄的包内别名：把上游类型集中在一处引用，
// 升级 moejs 时只改这里（依赖约束：只有本包允许 import moejs，引擎与 toolkit 都不知到它）。
type (
	mojsRuntime = moejs.Runtime
	mojsLoop    = eventloop.Loop
)
