package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// RuntimeLimits 是单个插件运行时的资源上限；零值表示采用 domain/limits.go 的默认值。
// 默认值来源（06 §6.4、09 §9.5）：堆 64MB、栈 64KB、调用深度 2000、单次脚本调用 5s、
// 钩子 200ms/最大 2s、工具按 Descriptor.Timeout（默认 120s）、provider 流 15s/90s/30min。
type RuntimeLimits struct {
	MemoryLimit   int64         `json:"memoryLimit"`
	StackSize     int64         `json:"stackSize"`
	MaxCallDepth  int           `json:"maxCallDepth"`
	CallTimeout   time.Duration `json:"callTimeout"`
	HookTimeout   time.Duration `json:"hookTimeout"`
	ResultMaxByte int           `json:"resultMaxBytes"`
}

// PluginScript 描述一份待装载的插件脚本：已编译 bundle 的字节 + 清单声明的
// 能力白名单（未声明的能力在脚本里不存在，05 §6.2、06 §6.4）。
// 引擎细节（moejs 的 Option、Runtime、SetModule）属于适配器内部，不出现在本端口——
// 这是“engine 不知道 moejs”的依赖方向保证（01 §3.3）。
type PluginScript struct {
	PluginID     string        `json:"pluginId"`
	Version      string        `json:"version"`
	Trust        domain.Trust  `json:"trust"`
	APIVersion   int           `json:"apiVersion"`
	Compiled     []byte        `json:"-"`
	Capabilities []string      `json:"capabilities,omitempty"`
	EnvWhitelist []string      `json:"env,omitempty"`
	Limits       RuntimeLimits `json:"limits,omitempty"`
}

// ScriptCall 是一次跨边界调用：宿主把 Go 值编成 JSON 进脚本，结果只接受
// JSON 可序列化值、最大 1MB（06 §6.4）。Kind 决定预算档位（domain 常量）。
type ScriptCallKind string

const (
	CallTool     ScriptCallKind = "tool"     // 预算 = Descriptor.Timeout（默认 120s）。
	CallHook     ScriptCallKind = "hook"     // 预算 200ms/最大 2s；tool.beforeCall 等待 ui 时暂停计时。
	CallAction   ScriptCallKind = "action"   // 默认 5s（05 §6.3 ActionDef.timeoutMs）。
	CallRoute    ScriptCallKind = "route"    // 默认 5s，响应上限 64KB（05 §6.3）。
	CallProvider ScriptCallKind = "provider" // 长任务：建连 15s/空闲 90s/总 30min（03 §5.5）。
)

// ScriptCall 是调用描述；Args 必须是已校验的 JSON。
type ScriptCall struct {
	Kind    ScriptCallKind  `json:"kind"`
	Handler string          `json:"handler"` // 注册名（工具名/动作名/点位/模型 id）。
	Args    json.RawMessage `json:"args"`
	Timeout time.Duration   `json:"timeout,omitempty"` // 0 = 按 Kind 取默认预算。
}

// ScriptResult 是脚本调用的原始 JSON 返回值与运行时状态标记。
// Err 非 nil 表示脚本侧失败；超时/超内存/panic 由实现方标记 RuntimeDirty，
// 宿主关闭并重建该 Runtime，当前调用返回 plugin_crashed（06 §6.4 回收策略）。
type ScriptResult struct {
	Data         json.RawMessage `json:"data,omitempty"`
	RuntimeDirty bool            `json:"runtimeDirty"`
}

// ScriptHost 是插件脚本生命周期端口（01 §5.2；实现：internal/plugins 的 moejs 宿主，M1-C）。
// 语义约束：
//   - 每插件一个 Runtime、绑定一个专用 goroutine，调用经带缓冲 channel 串行进入（06 §6.4）；
//   - 脚本等待宿主长调用（ui/exec/http.sse/subagent）时暂停调用超时计时（06 §6.4）；
//   - 取消贯穿脚本且不可被脚本捕获（INV-8：Runtime.Interrupt 由宿主调用）；
//   - 连续 3 次失败 → 自动禁用插件、状态 degraded，可手动重载（06 §6.4/§6.9）；
//   - 热重载双代：新代构建完成后原子替换，在途调用走旧代（06 §6.5、§6.9）。
//
// 测试替身：internal/testsupport 的脚本桩（按剧本返回 JSON/错误/超时，支撑
// 10 §11.5 的插件失败矩阵）。
type ScriptHost interface {
	// Load 创建 Runtime、执行入口工厂并收集贡献（staging；校验与提交属于装配层）。
	// ctx 取消或工厂 Promise 失败返回 domain.CodePluginCrashed/CodePluginTimeout。
	Load(ctx context.Context, script PluginScript) (PluginHandle, error)
}

// PluginHandle 是一个已加载插件的调用句柄。
type PluginHandle interface {
	// Invoke 串行执行一次脚本调用（见 ScriptCall 预算档位）。
	Invoke(ctx context.Context, call ScriptCall) (ScriptResult, error)
	// Status 报告 degraded/disabled 等状态变化（06 §6.4、§6.9，evt.plugin.changed 真源）。
	Status() (failures int, disabled bool)
	// Close 取消该 Runtime 启动的所有异步工作并释放资源（06 §6.4 防 goroutine 泄漏）。
	Close(ctx context.Context) error
}
