package host

// ctx.processes —— 进程监督桥（capability: process.supervise，仅 builtin）。
//
// 与 ctx.process.start 的区别：
//   - ctx.process.start 在 Core 自己的 Runtime 里拉起一个带 stdin/stdout 的子进程，
//     生命周期跟着这个插件的 Runtime（关闭/重载即杀）；适合 MCP stdio 这类「本会话内
//     的对话进程」；
//   - ctx.processes 把进程交给**网关**的 ProcessManager：跨会话、可崩溃重启、按引用
//     计数回收，适合 cron scheduler 这类「插件启用期间一直跑」的进程。
//
// 边界（09 §9.2、06 §6.10）：
//   - entry 由宿主解析到插件目录内（`..`、绝对路径、目录外一律拒绝），插件不能跑任意路径；
//   - owner 由宿主按能力集盖章，脚本不能注册别人名下的进程；
//   - 没有 stdin/stdout 句柄，只有 register/ensure/stop/status/unregister/list；
//   - 网关再按 owner/全局限额与 entry 可读性校验一次，两道门都不省。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/plugins"
)

// 进程名字与 entry 的限额（与网关侧 spec 校验同一量级）。
const (
	processNameMaxRunes = 64
	processEntryMaxRune = 4096
	processArgsMax      = 32
	// managedProcessReleaseTimeout 是关闭 Runtime 时注销被管进程的等待上限。
	// 网络调用不能无期限：插件关闭不能被一个卡住的网关拖死。
	managedProcessReleaseTimeout = 5 * time.Second
)

func addProcessSuperviseContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil || !st.caps.Has(plugins.CapProcessSupervise) || st.caps.Trust != plugins.TrustBuiltin {
		return nil
	}
	if hc.Processes == nil {
		return nil
	}
	// 捕获桥：Runtime 关闭时要靠它注销被管进程（见 releaseManagedProcesses）。
	st.mu.Lock()
	st.processes = hc.Processes
	st.mu.Unlock()

	r := st.rt.Realm()
	obj := r.NewObject()

	// resolveEntry 把插件内相对路径解析成插件目录内的绝对路径。
	// Script.Root 为空（随包插件在 Electron resources 里也应当有目录；纯内嵌包可能没有）
	// 时直接拒绝：没有目录就没有「插件内」这个边界。
	resolveEntry := func(raw string) (string, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", fmt.Errorf("entry 不能为空")
		}
		if n := len([]rune(raw)); n > processEntryMaxRune {
			return "", fmt.Errorf("entry 过长")
		}
		if filepath.IsAbs(raw) || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
			return "", fmt.Errorf("entry 必须是插件目录内的相对路径")
		}
		root := strings.TrimSpace(st.pluginRoot())
		if root == "" {
			return "", fmt.Errorf("插件没有可访问的目录，拒绝注册外部进程")
		}
		clean := filepath.Clean(filepath.FromSlash(raw))
		if clean == "." || strings.HasPrefix(clean, "..") {
			return "", fmt.Errorf("entry %q 越出插件目录", baseForLog(raw))
		}
		abs := filepath.Join(root, clean)
		if !strings.HasPrefix(abs, root+string(filepath.Separator)) && abs != root {
			return "", fmt.Errorf("entry %q 越出插件目录", baseForLog(raw))
		}
		return abs, nil
	}

	_ = obj.SetProp(r, r.KeyFromGoString("register"), st.rt.Function("processes.register", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		var spec ProcessSpec
		if len(args) == 0 || moejs.Arg(args, 0).IsNullish() {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes.register: 需要参数", nil)
		}
		raw, err := st.rt.AppendJSON(nil, moejs.Arg(args, 0))
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes.register: 参数不可 JSON 序列化", nil)
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes.register: 参数形状不符", nil)
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes.register: name 不能为空", nil)
		}
		if n := len([]rune(name)); n > processNameMaxRunes {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.processes.register: name 超过 64 字符", nil)
		}
		if len(spec.Args) > processArgsMax {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.processes.register: args 最多 32 项", nil)
		}
		entry, rerr := resolveEntry(spec.Entry)
		if rerr != nil {
			st.auditError("processes", "register", baseForLog(spec.Entry), rerr)
			return moejs.Undefined(), throwCoded(rr, codeAccessDenied, "ctx.processes.register: "+rerr.Error(), nil)
		}
		// Runtime 由脚本选，但 binary 只允许指向插件目录里的文件（已在上面校验）。
		spec.Entry = entry
		if spec.Runtime == "" {
			spec.Runtime = "node"
		}
		if spec.Runtime != "node" && spec.Runtime != "binary" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes.register: runtime 只能是 node|binary", nil)
		}
		spec.Env = nil // 环境由网关按「宿主环境剥凭据 + 显式白名单」决定，脚本不在这里注入任意变量

		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		stt, err := hc.Processes.Register(ctx, st.caps.PluginID, spec)
		if err != nil {
			st.auditError("processes", "register", name, err)
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.processes.register: "+err.Error(), nil)
		}
		// 记下「本插件注册过哪些进程」，Runtime 关闭时按它注销。
		// 不记录就没法在插件禁用/重载时释放引用，进程会一直挂到网关退出。
		st.noteManagedProcess(name, spec.Scope)
		st.audit("processes", "register", name, 0, "ok", map[string]any{
			"scope": spec.Scope, "runtime": spec.Runtime, "pid": stt.PID,
		})
		return st.processStatusValue(processStatusView(stt))
	}))

	simple := func(op string, call func(ctx context.Context, pluginID, name, scope string) (ProcessStatus, error)) moejs.NativeFunc {
		return func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			name := strings.TrimSpace(moejs.Arg(args, 0).String())
			if name == "" {
				return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes."+op+": name 不能为空", nil)
			}
			scope := strings.TrimSpace(moejs.Arg(args, 1).String())
			ctx := hc.Signal
			if ctx == nil {
				ctx = context.Background()
			}
			stt, err := call(ctx, st.caps.PluginID, name, scope)
			if err != nil {
				st.auditError("processes", op, name, err)
				return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.processes."+op+": "+err.Error(), nil)
			}
			st.audit("processes", op, name, 0, "ok", map[string]any{"pid": stt.PID})
			return st.processStatusValue(processStatusView(stt))
		}
	}
	_ = obj.SetProp(r, r.KeyFromGoString("ensure"), st.rt.Function("processes.ensure", 2, simple("ensure", hc.Processes.Ensure)))
	_ = obj.SetProp(r, r.KeyFromGoString("stop"), st.rt.Function("processes.stop", 2, simple("stop", hc.Processes.Stop)))

	_ = obj.SetProp(r, r.KeyFromGoString("status"), st.rt.Function("processes.status", 2, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		name := strings.TrimSpace(moejs.Arg(args, 0).String())
		if name == "" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes.status: name 不能为空", nil)
		}
		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		stt, ok := hc.Processes.Status(ctx, st.caps.PluginID, name, strings.TrimSpace(moejs.Arg(args, 1).String()))
		if !ok {
			return resolveConfig(st, moejs.Undefined()), nil
		}
		return st.processStatusValue(processStatusView(stt))
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("unregister"), st.rt.Function("processes.unregister", 2, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		name := strings.TrimSpace(moejs.Arg(args, 0).String())
		if name == "" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.processes.unregister: name 不能为空", nil)
		}
		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		if err := hc.Processes.Unregister(ctx, st.caps.PluginID, name, strings.TrimSpace(moejs.Arg(args, 1).String())); err != nil {
			st.auditError("processes", "unregister", name, err)
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.processes.unregister: "+err.Error(), nil)
		}
		// 已显式注销：关闭 Runtime 时不再重复放一次引用。
		st.forgetManagedProcess(name)
		st.audit("processes", "unregister", name, 0, "ok", nil)
		return moejs.Undefined(), nil
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("list"), st.rt.Function("processes.list", 0, func(rr *engine.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		ctx := hc.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		list, err := hc.Processes.List(ctx, st.caps.PluginID)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.processes.list: "+err.Error(), nil)
		}
		out := make([]map[string]any, 0, len(list))
		for _, stt := range list {
			out = append(out, processStatusView(stt))
		}
		v, gerr := st.rt.FromGo(out)
		if gerr != nil {
			return moejs.Undefined(), gerr
		}
		return resolveConfig(st, v), nil
	}))

	return put("processes", engine.ObjectValue(obj))
}

// noteManagedProcess 记下一个由本插件注册、需要随 Runtime 关闭而注销的进程。
func (st *capsState) noteManagedProcess(name, scope string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.managedProcs == nil {
		st.managedProcs = map[string]string{}
	}
	st.managedProcs[name] = scope
}

// forgetManagedProcess 在插件主动 unregister 后清掉记录（关闭时不再重复注销）。
func (st *capsState) forgetManagedProcess(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.managedProcs, name)
}

// releaseManagedProcesses 注销本插件注册过的全部被管进程（Runtime 关闭时调用）。
//
// 06 §6.4：关闭 Runtime 要「取消它启动的所有异步工作」；被管进程虽然是网关拉起的，
// 引用却挂在这个 Runtime 上，不释放就会一直跑到网关退出（cron scheduler 尤其明显：
// 插件被禁用后调度照旧发生）。
func (st *capsState) releaseManagedProcesses() {
	if st == nil {
		return
	}
	st.mu.Lock()
	sup := st.processes
	procs := st.managedProcs
	st.managedProcs = nil
	st.mu.Unlock()
	if sup == nil || len(procs) == 0 {
		return
	}
	// 关闭路径不带取消信号：注销本身就是最后的清理，不该被外层 ctx 提前打断。
	ctx, cancel := context.WithTimeout(context.Background(), managedProcessReleaseTimeout)
	defer cancel()
	pluginID := ""
	if st.caps != nil {
		pluginID = st.caps.PluginID
	}
	for name, scope := range procs {
		// 审计用 currentContext 的 Log：关闭路径上它仍在（Context 是装配期那份）。
		if err := sup.Unregister(ctx, pluginID, name, scope); err != nil {
			// 进程可能已经被别人清掉（例如 Core 重签令牌时按签发键回收）。记审计，不阻断关闭。
			st.audit("processes", "release", name, 0, "error", map[string]any{"error": err.Error()})
			continue
		}
		st.audit("processes", "release", name, 0, "ok", nil)
	}
}

// pluginRoot 返回插件包在宿主上的绝对目录（与技能路径同源）。
func (st *capsState) pluginRoot() string {
	if st == nil || st.inst == nil {
		return ""
	}
	return st.inst.script.Root
}

// processStatusView 把宿主状态转成脚本可见的形状（不含内部字段）。
func processStatusView(stt ProcessStatus) map[string]any {
	out := map[string]any{
		"id": stt.ID, "owner": stt.Owner, "name": stt.Name, "scope": stt.Scope,
		"runtime": stt.Runtime, "pid": stt.PID, "running": stt.Running,
		"restarts": stt.Restarts, "refs": stt.Refs,
		"startMode": stt.StartMode, "restart": stt.Restart,
	}
	if stt.Workspace != "" {
		out["workspace"] = stt.Workspace
	}
	if stt.LastError != "" {
		out["lastError"] = stt.LastError
	}
	if stt.StartedAt != "" {
		out["startedAt"] = stt.StartedAt
	}
	return out
}

// processStatusValue 把状态对象转成 JS 值（失败时退回 undefined）。
func (st *capsState) processStatusValue(view map[string]any) (moejs.Value, error) {
	v, err := st.rt.FromGo(view)
	if err != nil {
		return moejs.Undefined(), err
	}
	return resolveConfig(st, v), nil
}
