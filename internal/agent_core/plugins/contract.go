// Package plugins 是插件契约的注册表面：清单结构、能力位、钩子点位与前端插槽。
//
// 这里是「单一真源」在 Go 侧的落点：能力位、钩子点位、插槽名各只有一份声明，
// adag verify 拿它们与 spec/manifest.schema.json 逐字比对，并检查每个点位都有调用方、
// 每个插槽都有宿主（设计 1.3、6.2、6.6、7.2）。
//
// 基座源码不出现任何官方插件 id（设计 1.5.5 的硬边界），选择哪些插件由产品声明决定。
package plugins

import (
	"fmt"
	"strings"

	"github.com/adag/ada-go/internal/common/pluginid"
)

// APIVersionMajor 是当前插件 API 主版本；主版本不匹配直接拒绝加载（6.2）。
const APIVersionMajor = 1

// APIVersionMinor 是次版本；宿主忽略未知字段并记录（6.2）。
const APIVersionMinor = 0

// ValidID 报告插件 id 是否合法；规则只有一份，在 internal/common/pluginid
// （common 组与 gateway 组都要用，agent_core 只做转发）。
func ValidID(id string) bool { return pluginid.Valid(id) }

// Trust 是信任分级（6.9）。
type Trust string

const (
	// TrustBuiltin：预编译 bundle 随二进制发布，只能启用/禁用，不能被磁盘同名插件覆盖。
	TrustBuiltin Trust = "builtin"
	// TrustVerified：manifest.sig 为 Ed25519 签名且校验通过。
	TrustVerified Trust = "verified"
	// TrustThirdParty：默认，受限（禁 exec、控制面钩子、前端脚本）。
	TrustThirdParty Trust = "third-party"
)

// Valid 报告信任级别是否已知。
func (t Trust) Valid() bool {
	switch t {
	case TrustBuiltin, TrustVerified, TrustThirdParty:
		return true
	}
	return false
}

// AllowsUIAsk 报告该信任级别能否拿到 ui.ask / net.sse（6.2、5.6：第三方不能弹窗，避免钓鱼）。
func (t Trust) AllowsUIAsk() bool { return t == TrustBuiltin || t == TrustVerified }

// Role 是清单声明的插件角色。审批策略角色（tool-policy）已整体移除：
// v1 不再定义任何角色，字段保留只为清单兼容（声明非空即装配失败）。
type Role string

// Roles 返回全部合法角色（当前为空：审批策略移除后没有内置角色语义）。
func Roles() []Role { return nil }

// Access 是工具的外部效果分类；只是声明，供 UI 徽标与审计使用（5.4、5.6）。
// 基座不再据此做任何放行/拒绝判定（审批策略已移除）。
type Access string

const (
	AccessRead    Access = "read"
	AccessWrite   Access = "write"
	AccessExec    Access = "exec"
	AccessNetwork Access = "network"
)

// HookAction 是钩子允许返回的动作等级（6.6）。
type HookAction string

const (
	ActionObserve HookAction = "observe" // 只读，任何插件
	ActionNote    HookAction = "note"    // 附加说明，不改数据面
	ActionReplace HookAction = "replace" // 仅 builtin：model.beforeRequest
	ActionAbort   HookAction = "abort"   // 仅 builtin：turn.before / model.beforeRequest
)

// actionRank 用于「清单声明的最高动作等级」比较；越大权限越高。
var actionRank = map[HookAction]int{
	ActionObserve: 0,
	ActionNote:    1,
	ActionReplace: 2,
	ActionAbort:   3,
}

// Rank 返回动作等级的权限序。未知等级返回 -1。
func (a HookAction) Rank() int {
	if r, ok := actionRank[a]; ok {
		return r
	}
	return -1
}

// ActionsAtOrBelow 返回权限不高于 max 的全部动作等级（按权限升序）。
func ActionsAtOrBelow(max HookAction) []HookAction {
	limit := max.Rank()
	out := make([]HookAction, 0, len(actionRank))
	for _, a := range []HookAction{ActionObserve, ActionNote, ActionReplace, ActionAbort} {
		if actionRank[a] <= limit {
			out = append(out, a)
		}
	}
	return out
}

// HookPoint 描述一个钩子点位及其契约（6.6 表格逐行落地）。
type HookPoint struct {
	// Name 是点位名，脚本与引擎都用它。
	Name string
	// Summary 说明时机。
	Summary string
	// Allowed 是该点位允许的动作（不含内置信任限制）。
	Allowed []HookAction
	// BuiltinOnlyActions 是仅 builtin 信任插件可用的动作子集。
	BuiltinOnlyActions []HookAction
	// Pair 是成对点位（可空）。
	Pair string
}

// hookPoints 是 14 个点位的唯一声明。
var hookPoints = []HookPoint{
	{Name: "session.start", Summary: "Core 装配完成、会话加载后", Allowed: []HookAction{ActionObserve, ActionNote}},
	{Name: "session.end", Summary: "Core 关闭前", Allowed: []HookAction{ActionObserve, ActionNote}},
	{Name: "thread.created", Summary: "新线程创建后", Allowed: []HookAction{ActionObserve, ActionNote}},
	{Name: "user.message", Summary: "用户消息追加后、进模型前", Allowed: []HookAction{ActionObserve, ActionNote}},
	{
		Name: "turn.before", Summary: "轮次开始、终止条件初始化前",
		Allowed:            []HookAction{ActionObserve, ActionNote, ActionAbort},
		BuiltinOnlyActions: []HookAction{ActionAbort},
		Pair:               "turn.after",
	},
	{Name: "turn.after", Summary: "轮次结束、TurnFinished 前", Allowed: []HookAction{ActionObserve, ActionNote}, Pair: "turn.before"},
	{
		Name: "model.beforeRequest", Summary: "请求模型前",
		Allowed:            []HookAction{ActionObserve, ActionNote, ActionReplace, ActionAbort},
		BuiltinOnlyActions: []HookAction{ActionReplace, ActionAbort},
		Pair:               "model.afterResponse",
	},
	{Name: "model.afterResponse", Summary: "收到终止信号后", Allowed: []HookAction{ActionObserve, ActionNote}, Pair: "model.beforeRequest"},
	{Name: "tool.afterCall", Summary: "工具回执生成后", Allowed: []HookAction{ActionObserve, ActionNote}},
	{Name: "compaction.before", Summary: "生成摘要前", Allowed: []HookAction{ActionObserve, ActionNote}, Pair: "compaction.after"},
	{Name: "compaction.after", Summary: "压缩 entry 写入后", Allowed: []HookAction{ActionObserve, ActionNote}, Pair: "compaction.before"},
	{Name: "interaction.requested", Summary: "交互请求发出后", Allowed: []HookAction{ActionObserve, ActionNote}},
	{Name: "plugin.loaded", Summary: "插件贡献提交后", Allowed: []HookAction{ActionObserve, ActionNote}},
	{Name: "error", Summary: "轮次内可恢复错误", Allowed: []HookAction{ActionObserve, ActionNote}},
}

// HookPoints 返回全部点位（只读副本）。
func HookPoints() []HookPoint {
	out := make([]HookPoint, len(hookPoints))
	copy(out, hookPoints)
	return out
}

// HookPointNames 返回点位名字符串切片，供 Schema 比对与清单校验。
func HookPointNames() []string {
	out := make([]string, len(hookPoints))
	for i, p := range hookPoints {
		out[i] = p.Name
	}
	return out
}

// LookupHookPoint 按名字找点位。
func LookupHookPoint(name string) (HookPoint, bool) {
	for _, p := range hookPoints {
		if p.Name == name {
			return p, true
		}
	}
	return HookPoint{}, false
}

// AllowsAction 报告点位是否允许某动作（考虑 builtin 限制）。
func (p HookPoint) AllowsAction(a HookAction, builtin bool) bool {
	allowed := false
	for _, x := range p.Allowed {
		if x == a {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	if !builtin {
		for _, x := range p.BuiltinOnlyActions {
			if x == a {
				return false
			}
		}
	}
	return true
}

// Capability 是一个可安装能力位。没声明的能力在脚本里根本不存在（设计 0 摘要第 3 条）。
type Capability string

const (
	// CapFSRead / CapFSWrite 安装受限 fs（根固定工作区；只读时写成员访问即抛 ERR_ACCESS_DENIED）。
	CapFSRead  Capability = "fs.read"
	CapFSWrite Capability = "fs.write"
	// CapNetFetch 是出网能力（当前不限目标主机，host 策略待插件化）；provider 流式协议走 CapNetSSE。
	CapNetFetch Capability = "net.fetch"
	CapNetSSE   Capability = "net.sse"
	// CapExec 只映射到 execFile（无 shell），且仅 builtin/verified。
	CapExec Capability = "exec"
	// CapProcess 是长驻 stdin/stdout 子进程（无 shell），仅 builtin/verified。
	CapProcess Capability = "process"
	// CapTurnInvoke 允许插件在指定线程（或新线程）触发一轮对话（仅 builtin）。
	// 它是通用轮次触发桥：宿主负责建线程/生成 runId/执行轮次，插件只提交提示词；
	// 不授予工具、文件或网络权限，也不等待轮次结束。
	CapTurnInvoke Capability = "turn.invoke"
	// CapState 是线程/工作区级受限 KV。
	CapState Capability = "state"
	// CapGlobalConfig 是机器级、按插件 id 命名空间隔离的配置存储（仅 verified/builtin）。
	CapGlobalConfig Capability = "global.config"
	// CapEventsRead 是只读事件/用量投影（线程上下文内，不能写事件）。
	CapEventsRead Capability = "events.read"
	// CapUIAsk 允许发起通用用户交互，不授予任何工具或文件权限；仅 builtin/verified。
	CapUIAsk Capability = "ui.ask"
	// CapSubagentInvoke 仅 builtin，供子智能体委派（v1.5）。
	CapSubagentInvoke Capability = "subagent.invoke"
	// CapModelAdmin 允许读取供应商/模型目录并切换当前模型（仅 builtin）。
	// 它不授予任何出网、文件或执行权限，也不暴露密钥。
	CapModelAdmin Capability = "model.admin"
	// CapPluginAdmin 允许把插件骨架写入全局插件根 ~/.adago/plugins（仅 builtin）。
	// 宿主负责 id/路径校验与「不覆盖」语义；它不授予任意文件写权限。
	CapPluginAdmin Capability = "plugin.admin"
	// CapToolDiscovery 是 builtin-only 的发现式工具宿主桥（ctx.catalog + ctx.tool.invoke）。
	CapToolDiscovery Capability = "tool.discovery"
	// CapDispatchSend 是 builtin-only 的内部发送桥（ctx.dispatch.send）：把消息投递给
	// 指定工作区的调度层（建线程 / 排队 / 运行中 steer 由调度层决定）。
	// 它不授予工具、文件或网络权限，也不采信脚本给的身份——sender 由宿主盖章。
	CapDispatchSend Capability = "dispatch.send"
	// CapProcessSupervise 是 builtin-only 的进程监督桥（ctx.processes）：把插件的长驻进程
	// 交给网关 ProcessManager 拉起、重启与回收，而不是在 Core 内自己 spawn。
	//
	// 它不开放 stdin/stdout 句柄、不授予任意可执行文件：entry 由宿主解析到插件目录内，
	// owner 由宿主按清单盖章，环境剥掉宿主凭据（09 §9.2、06 §6.10）。
	CapProcessSupervise Capability = "process.supervise"
	// CapBackendRead 是前端能力：预取只读后端动作结果。
	CapBackendRead Capability = "backend.read"
	// CapFrontendScript 是前端能力：加载插件自带 JS（默认关闭，仍受 CSP 限制）。
	CapFrontendScript Capability = "frontend.script"
)

// backendCapabilities 是后端可声明的能力白名单。
var backendCapabilities = []Capability{
	CapFSRead, CapFSWrite, CapNetFetch, CapNetSSE,
	CapExec, CapProcess, CapState, CapGlobalConfig, CapEventsRead, CapUIAsk, CapSubagentInvoke, CapModelAdmin, CapPluginAdmin, CapToolDiscovery, CapTurnInvoke, CapDispatchSend, CapProcessSupervise,
}

// frontendCapabilities 是前端可声明的能力白名单。
var frontendCapabilities = []Capability{CapBackendRead, CapFrontendScript}

// BackendCapabilities 返回后端能力白名单。
func BackendCapabilities() []Capability { return append([]Capability(nil), backendCapabilities...) }

// FrontendCapabilities 返回前端能力白名单。
func FrontendCapabilities() []Capability { return append([]Capability(nil), frontendCapabilities...) }

// BackendCapabilityNames / FrontendCapabilityNames 返回字符串形式，供 Schema 比对。
func BackendCapabilityNames() []string  { return capNames(backendCapabilities) }
func FrontendCapabilityNames() []string { return capNames(frontendCapabilities) }

func capNames(cs []Capability) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

// MinTrust 返回该能力要求的最低信任级别（6.2）。
func (c Capability) MinTrust() Trust {
	switch c {
	case CapExec, CapProcess, CapNetSSE, CapUIAsk, CapGlobalConfig:
		return TrustVerified
	case CapSubagentInvoke, CapModelAdmin, CapPluginAdmin, CapToolDiscovery, CapTurnInvoke, CapDispatchSend, CapProcessSupervise:
		return TrustBuiltin
	default:
		return TrustThirdParty
	}
}

// TrustAllows 报告某信任级别能否声明该能力。
func (c Capability) TrustAllows(t Trust) bool {
	need, have := rank(c.MinTrust()), rank(t)
	return have >= need
}

func rank(t Trust) int {
	switch t {
	case TrustThirdParty:
		return 0
	case TrustVerified:
		return 1
	case TrustBuiltin:
		return 2
	}
	return -1
}

// Slot 是一个稳定的前端插槽位（7.2）。插槽宁少勿多，新增即一次兼容承诺。
type Slot struct {
	Name     string
	Position string
	// BaseOwned 报告外壳是否由基座拥有：thread.tray 的托盘外壳与安全操作始终由基座渲染，
	// 插件只能接管卡片内容（7.2 UI 边界、1.5.6）。
	BaseOwned bool
}

// slots 是 12 个插槽的唯一声明，顺序与 7.2 表格一致。
var slots = []Slot{
	{"sidebar.top", "侧栏品牌区下方", false},
	{"sidebar.bottom", "侧栏导航之后", false},
	{"topbar.right", "顶栏右侧操作区", false},
	{"chat.corner", "会话区右上角悬浮", false},
	{"composer.left", "输入框工具行左侧", false},
	{"composer.right", "输入框工具行右侧", false},
	{"composer.footer", "输入框下方辅助区", false},
	{"composer.above", "输入框上方悬浮区（sticky）", false},
	{"thread.tray", "交互托盘内的插件位", true},
	{"transcript.after", "对话流末尾", false},
	{"extensions.list", "插件列表页", false},
	{"dashboard.widgets", "概览页卡片区", false},
}

// Slots 返回全部插槽。
func Slots() []Slot { return append([]Slot(nil), slots...) }

// SlotNames 返回插槽名，供 Schema 与前端注册表比对。
func SlotNames() []string {
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = s.Name
	}
	return out
}

// LookupSlot 按名字找插槽。
func LookupSlot(name string) (Slot, bool) {
	for _, s := range slots {
		if s.Name == name {
			return s, true
		}
	}
	return Slot{}, false
}

// IsSlotName 报告名字是否已知。
func IsSlotName(name string) bool { _, ok := LookupSlot(name); return ok }

// MaxBackendActions 与预取预算（7.5）：只读动作最多 16 个、并发 4、单个 300ms、结果缓存 8s。
const (
	MaxBackendActions        = 16
	BackendActionConcurrency = 4
	BackendActionTimeoutMS   = 300
	BackendActionCacheTTLMS  = 8000
)

// ValidateCapabilityList 检查能力声明是否合法：已知名字、无重复、与信任级别相容。
func ValidateCapabilityList(caps []Capability, t Trust, backend bool) error {
	seen := make(map[Capability]bool, len(caps))
	whitelist := backendCapabilities
	if !backend {
		whitelist = frontendCapabilities
	}
	for _, c := range caps {
		if seen[c] {
			return fmt.Errorf("plugins: 能力位重复声明 %q", c)
		}
		seen[c] = true
		known := false
		for _, w := range whitelist {
			if w == c {
				known = true
				break
			}
		}
		if !known {
			kind := "后端"
			if !backend {
				kind = "前端"
			}
			return fmt.Errorf("plugins: 未知%s能力位 %q", kind, c)
		}
		if !c.TrustAllows(t) {
			return fmt.Errorf("plugins: 信任级别 %q 不允许能力 %q（需要至少 %q）", t, c, c.MinTrust())
		}
	}
	return nil
}

// ValidateHookDeclaration 检查清单里的钩子声明：点位已知、动作不超过该点位允许集合。
func ValidateHookDeclaration(declared map[string]HookAction, t Trust) error {
	for point, max := range declared {
		if strings.TrimSpace(point) == "" {
			return fmt.Errorf("plugins: 钩子点位名为空")
		}
		p, ok := LookupHookPoint(point)
		if !ok {
			return fmt.Errorf("plugins: 未知钩子点位 %q", point)
		}
		if max.Rank() < 0 {
			return fmt.Errorf("plugins: 未知动作等级 %q（点位 %q）", max, point)
		}
		for _, a := range ActionsAtOrBelow(max) {
			if !containsAction(p.Allowed, a) {
				return fmt.Errorf("plugins: 点位 %q 不允许动作 %q", point, a)
			}
			if !p.AllowsAction(a, t == TrustBuiltin) {
				return fmt.Errorf("plugins: 信任级别 %q 在点位 %q 不能使用 %q", t, point, a)
			}
		}
	}
	return nil
}

func containsAction(list []HookAction, a HookAction) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}
