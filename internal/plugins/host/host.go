// Package host 是插件的 moejs 宿主：每个插件一个 Runtime、一条专用 goroutine，
// 只安装清单声明过的能力，运行期通过 ports.ScriptHost 与引擎打交道。
//
// 边界（设计 1.5.2、6.4）：
//   - 引擎不知道 moejs，插件不知道宿主句柄：中间只有本文件定义的 Contribution 与 HostCall。
//   - 官方插件与第三方插件走同一条加载路径，差别只有 trust 与随包发布。
//   - 没有声明的能力在脚本里根本不存在；声明了但成员越权是「调用即抛 ERR_ACCESS_DENIED」。
//
// 与上游的对应关系（实测见 docs/decisions.md D-7、tools/moejspike）：
// 模块白名单与能力位映射由本包的 resolver 负责；node:fs 用上游实现 + 宿主包装；
// node:child_process 由宿主提供（上游 alpha.11 没有这个模块）。
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

// CapabilitySet 是装配期算出的「这个插件实际能拿到什么」。
//
// 它是清单与信任级别求交的结果：清单写了 exec 但信任是 third-party，这里就没有 exec。
type CapabilitySet struct {
	Trust    plugins.Trust
	Backend  map[plugins.Capability]bool
	Frontend map[plugins.Capability]bool
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
		Trust:         m.Trust,
		Backend:       map[plugins.Capability]bool{},
		Frontend:      map[plugins.Capability]bool{},
		Hooks:         map[string]plugins.HookAction{},
		Env:           map[string]string{},
		WorkspaceRoot: workspaceRoot,
		Mount:         DefaultMount,
	}
	if m.Backend != nil {
		for _, cap := range m.Backend.Capabilities {
			if !cap.TrustAllows(c.Trust) {
				return nil, fmt.Errorf("host: %s 信任级别 %s 不允许能力 %s（6.2）", m.ID, m.Trust, cap)
			}
			c.Backend[cap] = true
		}
		for _, name := range m.Backend.Env {
			if v, ok := hostEnv[name]; ok {
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

// DefaultMount 是脚本视图里工作区的挂载点（12 §6.12.1：宿主绝对路径不进脚本）。
const DefaultMount = "/workspace"

// Has 报告后端能力是否被授予。
func (c *CapabilitySet) Has(cap plugins.Capability) bool { return c.Backend[cap] }

// Context 是一次调用注入的宿主上下文。
//
// 引擎只交出这些字段，插件拿不到数据库句柄、密钥文件、原始 socket 或进程（1.5.2）。
type Context struct {
	Workspace string
	ThreadID  string
	RunID     string
	CallID    string
	PluginID  string
	Now       time.Time
	Signal    context.Context // 取消贯穿：ctx 取消即中止脚本与宿主长调用。

	// Secret 只在清单声明过该名字时返回值；否则第二返回值为 false。
	Secret func(name string) (string, bool)
	// State 是插件 KV（capability: state）。
	State func(ctx context.Context, op StateOp) (json.RawMessage, error)
	// UI 是通用交互桥（capability: ui.ask）；nil 表示未授予。
	UI func(ctx context.Context, req UIRequest) (UIResolution, error)
	// HTTP 是 provider 的出网桥（capability: net.fetch / net.sse）；nil 表示未授予。
	HTTP HTTPBridge
	// Log 写结构化日志；密钥一律脱敏。
	Log func(level, msg string, fields map[string]any)
	// Checkpoint 把写入路径并入检查点（回滚用）。
	Checkpoint func(paths ...string)
}

// StateOp 是一次插件 KV 访问。
type StateOp struct {
	Kind   string // get | set | delete | list
	Key    string
	Value  json.RawMessage
	Prefix string
}

// UIRequest 是一次通用用户交互（05 §6.3 UIContext）。
type UIRequest struct {
	Kind      domain.InteractionKind
	Title     string
	Message   string
	Options   []string
	TimeoutMS int
}

// UIResolution 是交互结果；调用方必须把 timeout/unavailable 当作拒绝（INV-4）。
type UIResolution struct {
	Status domain.InteractionStatus
	Value  string
}

// HTTPBridge 是宿主执行的出站调用（provider 唯一出口，03 §5.5）。
type HTTPBridge interface {
	// JSON 发一次请求并返回解析后的响应体；allowlist、大小、超时、重定向由宿主执行。
	JSON(ctx context.Context, method, url string, headers map[string]string, body string, maxBytes int) (HTTPResult, error)
	// SSE 逐事件回调，直到显式终止或 ctx 取消。
	SSE(ctx context.Context, method, url string, headers map[string]string, body string, onEvent func(event, data string) error) (HTTPResult, error)
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
