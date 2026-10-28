package host

// 本文件装「环境面」的两块宿主自建模块（12 §6.12.1）：
//
//   - node:process：上游 alpha.11 连全局 process 都没有（D-7 实测）。这里给一个
//     受限形状：argv/env/cwd/platform/arch/pid/version/versions 是宿主数据，
//     exit 抛 ERR_ACCESS_DENIED，没有 kill/binding/dlopen/setuid/umask。
//   - agent:host：与入口工厂同形的一份 AgentAPI，作为模块导出（06 §6.4）。
//
// 另外 installGuardPrimitives 装的是跨模块的守卫原语：每 Runtime 的定时器计数上限
// （12 §6.11.1 E-2 —— 上游不产生 ERR_TIMER_LIMIT，实测 600 个定时器全部触发）。

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/plugins/host/nodemods"
)

// processShim 是 node:process 的模块体：与上游 node:fs 同一个形状，
// 只读 import.meta.binding 并把成员逐个导出（12 §6.12.3）。
const processShim = `const b = import.meta.binding;
if (b === undefined) throw new Error("Cannot import \"node:process\": the host did not install it in this runtime");
export const argv = b.argv;
export const env = b.env;
export const platform = b.platform;
export const arch = b.arch;
export const pid = b.pid;
export const version = b.version;
export const versions = b.versions;
export const execPath = b.execPath;
export const stdout = b.stdout;
export const stderr = b.stderr;
export const exit = b.exit;
export const cwd = b.cwd;
export const nextTick = b.nextTick;
export const hrtime = b.hrtime;
export default b;
`

// agentHostShim 是 agent:host 的模块体：命名导出与 default 指向同一个绑定对象。
const agentHostShim = `const b = import.meta.binding;
if (b === undefined) throw new Error("Cannot import \"agent:host\": the host did not install it in this runtime");
export const registerTool = b.registerTool;
export const registerProvider = b.registerProvider;
export const registerPrompt = b.registerPrompt;
export const registerAction = b.registerAction;
export const route = b.route;
export const on = b.on;
export default b;
`

// installProcess 装 node:process 的绑定与全局 process（12 §6.12.2 的 process 行）。
//
// env 只含清单 backend.env 白名单与宿主环境的交集（06 §6.2）：缺失的名字根本不出现，
// 不伪造空串。
func (st *capsState) installProcess() error {
	r := st.rt.Realm()
	rt := st.rt
	hc := st.currentContext()

	env := r.NewObject()
	names := make([]string, 0, len(st.caps.Env))
	for name := range st.caps.Env {
		names = append(names, name)
	}
	// 顺序稳定只为可回放：env 的键顺序不影响语义。
	sortStrings(names)
	for _, name := range names {
		if err := env.SetProp(r, r.KeyFromGoString(name), moejs.String(st.caps.Env[name])); err != nil {
			return fmt.Errorf("host: 装 process.env.%s 失败: %w", name, err)
		}
	}

	binding := r.NewObject()
	set := func(key string, fn engine.NativeFunc) error {
		return binding.SetProp(r, r.KeyFromGoString(key), rt.Function("process."+key, 1, fn))
	}
	setVal := func(key string, v moejs.Value) error {
		return binding.SetProp(r, r.KeyFromGoString(key), v)
	}

	if err := setVal("argv", moejs.String("")); err != nil { // argv[0] 只暴露虚拟名，不泄露宿主路径
		return err
	}
	argv := r.NewArray(moejs.String("adag"))
	if err := setVal("argv", engine.ObjectValue(argv)); err != nil {
		return err
	}
	if err := setVal("env", engine.ObjectValue(env)); err != nil {
		return err
	}
	for _, kv := range []struct{ key, val string }{
		{"platform", processPlatform()},
		{"arch", runtime.GOARCH},
		{"version", "v20.0.0"},
		{"execPath", "/adag"},
	} {
		if err := setVal(kv.key, moejs.String(kv.val)); err != nil {
			return err
		}
	}
	versions := r.NewObject()
	for _, kv := range [][2]string{{"node", "20.0.0"}, {"ada", "0.1.0"}, {"moejs", "0.1.0-alpha.11"}} {
		if err := versions.SetProp(r, r.KeyFromGoString(kv[0]), moejs.String(kv[1])); err != nil {
			return err
		}
	}
	if err := setVal("versions", engine.ObjectValue(versions)); err != nil {
		return err
	}
	if err := setVal("pid", engine.Int64Value(1)); err != nil {
		return err
	}
	for _, stream := range []string{"stdout", "stderr"} {
		obj := r.NewObject()
		write := rt.Function(stream+".write", 1, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			// 插件不写进程 stdout：输出只经宿主日志（12 §6.12.1）。
			return engine.Bool(false), nil
		})
		if err := obj.SetProp(r, r.KeyFromGoString("write"), write); err != nil {
			return err
		}
		if err := setVal(stream, engine.ObjectValue(obj)); err != nil {
			return err
		}
	}
	if err := set("exit", func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
		return moejs.Undefined(), throwCoded(r, codeAccessDenied,
			"process.exit 不开放：插件退出只能由宿主结束 Runtime（12 §6.12.2）", map[string]any{"capability": "process"})
	}); err != nil {
		return err
	}
	if err := set("cwd", func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
		mount := st.caps.Mount
		if mount == "" {
			mount = DefaultMount
		}
		return moejs.String(mount), nil
	}); err != nil {
		return err
	}
	if err := set("nextTick", func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		fn := moejs.Arg(args, 0)
		if !engine.IsCallable(fn) {
			return moejs.Undefined(), r.TypeError("process.nextTick(fn): fn 必须是函数")
		}
		rest := append([]moejs.Value(nil), args[1:]...)
		if !st.loop.RunOnLoop(func(rt *mojsRuntime) {
			if _, err := rt.CallFunction(fn, moejs.Undefined(), rest...); err != nil {
				st.auditError("process", "nextTick", "", err)
			}
		}) {
			return moejs.Undefined(), errors.New("host: process.nextTick 队列已随 Runtime 结束（06 §6.4）")
		}
		return moejs.Undefined(), nil
	}); err != nil {
		return err
	}
	if err := set("hrtime", func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
		arr := r.NewArray(engine.Int64Value(0), engine.Int64Value(int64(time.Since(hc.Now).Milliseconds())))
		return engine.ObjectValue(arr), nil
	}); err != nil {
		return err
	}

	st.process = engine.ObjectValue(binding)
	// 全局 process：上游没有，宿主装（12 §6.12.2 process 行）。
	if err := rt.SetGlobal("process", binding); err != nil {
		return fmt.Errorf("host: 装全局 process 失败: %w", err)
	}
	return nil
}

// installHostBridge 装 agent:host：与入口工厂同形的一份 AgentAPI（06 §6.4）。
//
// 模块体在装配早期（loadEntry）就求值，那时还没有 staging，因此导出的函数
// 必须在调用时再取实例（buildAgentAPI 的 owner 回调）。
func (st *capsState) installHostBridge() error {
	st.agent = buildAgentAPI(st.rt, func() *Instance { return st.inst })
	if st.agent.IsUndefined() {
		return errors.New("host: agent:host 绑定构造失败")
	}
	return nil
}

// moduleBinding 按模块名给出 import.meta.binding（caps.go 的 fillMeta 使用）。
func (st *capsState) moduleBinding(name string) (moejs.Value, bool) {
	switch name {
	case moduleProcess:
		if !st.process.IsUndefined() {
			return st.process, true
		}
	case moduleAgentHost:
		if !st.agent.IsUndefined() {
			return st.agent, true
		}
	case moduleChildProcess:
		if !st.child.IsUndefined() {
			return st.child, true
		}
	}
	// 需要宿主状态的绑定（console/os/module）与纯计算模块的绑定（crypto）。
	if v, ok := st.builtinBinding(name); ok {
		return v, true
	}
	if v, ok := nodemods.Binding(st.rt, name); ok {
		return v, true
	}
	return moejs.Value{}, false
}

// installGuardPrimitives 装跨模块的守卫原语：每 Runtime 的定时器计数上限（E-2）。
//
// 上游没有 onunref 之外的定时器管理，也不产生 ERR_TIMER_LIMIT（实测 600 个定时器
// 全部触发）。这里包一层全局 setTimeout/setInterval：超过 Limits.MaxTimers 直接抛
// ERR_TIMER_LIMIT，清掉的句柄释放计数。
func (st *capsState) installGuardPrimitives() error {
	rt := st.rt
	r := rt.Realm()
	limit := st.limits.MaxTimers
	if limit <= 0 {
		limit = DefaultLimits().MaxTimers
	}
	var live atomic.Int64
	wrap := func(name string) error {
		orig, err := globalValue(rt, name)
		if err != nil {
			return fmt.Errorf("host: 读取全局 %s 失败: %w", name, err)
		}
		if !engine.IsCallable(orig) {
			return fmt.Errorf("host: 全局 %s 不是函数，无法加定时器上限", name)
		}
		wrapped := rt.Function(name, 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			if n := live.Add(1); n > int64(limit) {
				live.Add(-1)
				return moejs.Undefined(), throwCoded(r, codeTimerLimit,
					fmt.Sprintf("每 Runtime 的未完成定时器上限是 %d（12 §6.11.1 E-2、09 §9.5）", limit),
					map[string]any{"limit": limit})
			}
			v, err := r.Call(orig, engine.ObjectValue(r.Global), args)
			if err != nil {
				live.Add(-1)
				return moejs.Undefined(), err
			}
			return v, nil
		})
		if err := rt.SetGlobal(name, wrapped); err != nil {
			return fmt.Errorf("host: 包装全局 %s 失败: %w", name, err)
		}
		return nil
	}
	for _, name := range []string{"setTimeout", "setInterval"} {
		if err := wrap(name); err != nil {
			return err
		}
	}
	// 清掉的定时器释放计数：clearTimeout/clearInterval 接受 id 或句柄，这里按「每次清理
	// 最多释放一个」记账；多释放不会越过 0（下面夹住）。
	release := rt.Function("__ada_release_timer", 1, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
		for {
			cur := live.Load()
			if cur <= 0 {
				return moejs.Undefined(), nil
			}
			if live.CompareAndSwap(cur, cur-1) {
				return moejs.Undefined(), nil
			}
		}
	})
	for _, name := range []string{"clearTimeout", "clearInterval"} {
		orig, err := globalValue(rt, name)
		if err != nil || !engine.IsCallable(orig) {
			continue
		}
		inner := orig
		wrapped := rt.Function(name, 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			v, err := r.Call(inner, engine.ObjectValue(r.Global), args)
			if err == nil {
				_, _ = r.Call(release, moejs.Undefined(), nil)
			}
			return v, err
		})
		if err := rt.SetGlobal(name, wrapped); err != nil {
			return fmt.Errorf("host: 包装全局 %s 失败: %w", name, err)
		}
	}
	st.timers = &live
	return nil
}

// globalValue 读全局属性（Runtime 没有 GetGlobal，只有 SetGlobal）。
func globalValue(rt *mojsRuntime, name string) (moejs.Value, error) {
	return rt.Realm().GetV(engine.ObjectValue(rt.Realm().Global), rt.Realm().KeyFromGoString(name))
}

// processPlatform 把 Go 的 GOOS 映射成 Node 的 process.platform。
func processPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return runtime.GOOS
	}
}

// sortStrings 是插入排序：这里的键很少（env 白名单），避免为它引 sort 依赖带来的
// 分配；同时保持确定性输出。
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && strings.Compare(v[j-1], v[j]) > 0; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}

// timerCounter 只是为了让「装过守卫」可断言（测试用）。
func (st *capsState) timerCounter() *atomic.Int64 { return st.timers }
