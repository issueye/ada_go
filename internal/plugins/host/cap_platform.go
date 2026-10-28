package host

// 本文件装「平台面」的三个宿主绑定：console / os / module（12 §6.12.2 的三行）。
//
// 它们与 crypto 不同：需要当前调用的 Context（日志、插件 id）或宿主的安装结果
// （已安装模块清单），所以绑定在 host 包而不是 nodemods 包；模块体仍在
// nodemods/src/*.js，形状完全一样（只读 import.meta.binding）。

import (
	"fmt"
	goruntime "runtime"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

// installConsole 装全局 console：模块没被 import 时插件也要能 console.log。
//
// 全局这份用原生函数直接写日志（不走 console.js 的格式化），因为它是「总是存在」的
// 兜底；import "node:console" 得到的那份会把参数按 util.inspect 形状格式化后再交给
// 同一个 write。两份都不写进程 stdout（12 §6.12.2）。
func (st *capsState) installConsole() error {
	r := st.rt.Realm()
	obj := r.NewObject()
	for _, level := range []string{"debug", "info", "warn", "error"} {
		lv := level
		fn := st.rt.Function("console."+lv, 3, func(_ *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
			hc := st.currentContext()
			if hc.Log == nil {
				return moejs.Undefined(), nil
			}
			hc.Log(lv, st.joinLogArgs(args), map[string]any{"pluginId": hc.PluginID, "module": "console"})
			return moejs.Undefined(), nil
		})
		if err := obj.SetProp(r, r.KeyFromGoString(lv), fn); err != nil {
			return fmt.Errorf("host: 装 console.%s 失败: %w", lv, err)
		}
	}
	// log/info 是 info 的别名；trace 走 error（与 Node 的「总是打印」一致）。
	for _, alias := range []struct{ from, to string }{
		{"log", "info"}, {"trace", "error"}, {"dir", "info"}, {"table", "info"},
	} {
		src, err := obj.GetProp(r, r.KeyFromGoString(alias.to))
		if err != nil {
			continue
		}
		_ = obj.SetProp(r, r.KeyFromGoString(alias.from), src)
	}
	if err := st.rt.SetGlobal("console", obj); err != nil {
		return fmt.Errorf("host: 装全局 console 失败: %w", err)
	}
	return nil
}

// joinLogArgs 把 console.log 的参数拼成一行（字符串原样，其它走 JSON）。
func (st *capsState) joinLogArgs(args []engine.Value) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if arg.IsString() {
			parts = append(parts, arg.String())
			continue
		}
		raw, err := st.rt.AppendJSON(nil, arg)
		if err != nil || len(raw) == 0 || string(raw) == "null" {
			parts = append(parts, arg.String())
			continue
		}
		parts = append(parts, string(raw))
	}
	out := strings.Join(parts, " ")
	if len(out) > 8192 {
		out = out[:8192] + "... (truncated)"
	}
	return out
}

// consoleBinding 是 node:console 模块的绑定：只有一个 write(level, text)。
func (st *capsState) consoleBinding() moejs.Value {
	r := st.rt.Realm()
	obj := r.NewObject()
	write := st.rt.Function("console.write", 2, func(_ *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		level := strings.ToLower(strings.TrimSpace(moejs.Arg(args, 0).String()))
		switch level {
		case "debug", "info", "warn", "error":
		default:
			level = "info"
		}
		text := moejs.Arg(args, 1).String()
		hc := st.currentContext()
		if hc.Log != nil {
			hc.Log(level, text, map[string]any{"pluginId": hc.PluginID, "module": "console"})
		}
		return moejs.Undefined(), nil
	})
	_ = obj.SetProp(r, r.KeyFromGoString("write"), write)
	return engine.ObjectValue(obj)
}

// osBinding 是 node:os 的前置数据（不读宿主文件系统，tmpdir 固定虚拟路径）。
func (st *capsState) osBinding() moejs.Value {
	r := st.rt.Realm()
	obj := r.NewObject()
	set := func(key string, v any) {
		gv, err := r.FromGo(v)
		if err != nil {
			return
		}
		_ = obj.SetProp(r, r.KeyFromGoString(key), gv)
	}
	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	eol := "\n"
	if goruntime.GOOS == "windows" {
		eol = "\r\n"
	}
	endian := "LE"
	set("platform", processPlatform())
	set("arch", goruntime.GOARCH)
	set("type", osType())
	set("release", "")
	set("version", "")
	set("endianness", endian)
	set("eol", eol)
	set("tmpdir", "/tmp")
	set("cpus", goruntime.NumCPU())
	set("totalmem", mem.Sys)
	set("freemem", mem.Sys-mem.Alloc)
	return engine.ObjectValue(obj)
}

// moduleBindingInfo 是 node:module 的绑定：已安装的内置模块清单 + 是否可解析。
func (st *capsState) moduleBindingInfo() moejs.Value {
	r := st.rt.Realm()
	obj := r.NewObject()
	names := []string{}
	policy := st.policy
	if policy != nil {
		names = policy.Installed()
	}
	if gv, err := r.FromGo(names); err == nil {
		_ = obj.SetProp(r, r.KeyFromGoString("builtins"), gv)
	}
	isInstalled := st.rt.Function("module.isInstalled", 1, func(_ *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		spec := moejs.Arg(args, 0).String()
		if st.policy == nil {
			return engine.Bool(false), nil
		}
		return engine.Bool(st.policy.Allow(spec) == nil), nil
	})
	_ = obj.SetProp(r, r.KeyFromGoString("isInstalled"), isInstalled)
	return engine.ObjectValue(obj)
}

// osType 把 GOOS 映射成 Node 的 os.type()。
func osType() string {
	switch goruntime.GOOS {
	case "windows":
		return "Windows_NT"
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	default:
		return strings.Title(goruntime.GOOS) //nolint:staticcheck // 仅用于展示
	}
}

// builtinBinding 汇总「需要宿主状态的模块绑定」：console/os/module 之外交给 nodemods。
func (st *capsState) builtinBinding(name string) (moejs.Value, bool) {
	switch strings.TrimPrefix(name, "node:") {
	case "console":
		return st.consoleBinding(), true
	case "os":
		return st.osBinding(), true
	case "module":
		return st.moduleBindingInfo(), true
	}
	return moejs.Value{}, false
}
