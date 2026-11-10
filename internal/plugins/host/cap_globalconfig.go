package host

// ctx.config —— 插件配置（capability: global.config，仅 verified/builtin）。
//
// 两个作用域：
//   - ctx.config          机器级：<ConfigDir>/<namespace>.json 或 ~/.adago/config/；
//   - ctx.config.workspace 工作区级：<workspace>/.adago/config/<namespace>.json。
//
// 存储边界在 internal/globalconfig：固定路径、按插件 id 命名空间、0600 原子写、
// 跨进程锁与限额；脚本拿不到路径，只能读写自己的命名空间（09 §9.5、05 §6.2.1）。

import (
	"encoding/json"
	"fmt"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/globalconfig"
)

// resolveConfig 把同步结果包成已结算的 Promise，形状与 ctx.state/fs 一致。
func resolveConfig(st *capsState, v moejs.Value) moejs.Value {
	promise, settle := st.loop.NewPromise()
	settle(func(*mojsRuntime) (moejs.Value, error) { return v, nil })
	return promise
}

func notifyConfigChange(hc Context, namespace string) {
	if hc.OnConfigChange != nil {
		hc.OnConfigChange(namespace)
	}
}

// addGlobalConfigContextMember 给 ToolContext 补 ctx.config（capability: global.config）。
func addGlobalConfigContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil || false {
		return nil
	}
	ns := st.caps.ConfigNamespace
	if ns == "" {
		ns = st.caps.PluginID
	}
	var (
		store *globalconfig.Store
		err   error
	)
	if hc.ConfigDir != "" {
		store, err = globalconfig.OpenAt(hc.ConfigDir, ns)
	} else {
		store, err = globalconfig.Open(ns)
	}
	if err != nil {
		return err
	}
	obj := buildConfigObject(st, hc, store, "ctx.config", ns)
	if hc.Workspace != "" {
		wsStore, werr := globalconfig.OpenWorkspace(hc.Workspace, ns)
		if werr != nil {
			return werr
		}
		ws := buildConfigObject(st, hc, wsStore, "ctx.config.workspace", ns)
		r := st.rt.Realm()
		if err := obj.AsObject().SetProp(r, r.KeyFromGoString("workspace"), ws); err != nil {
			return err
		}
	}
	return put("config", obj)
}

// buildConfigObject 为某个作用域构造 get/set/delete/list；label 只用于错误文案。
func buildConfigObject(st *capsState, hc Context, store *globalconfig.Store, label, ns string) moejs.Value {
	r := st.rt.Realm()
	obj := r.NewObject()

	_ = obj.SetProp(r, r.KeyFromGoString("path"), st.rt.Function(label+".path", 0, func(_ *engine.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		return st.rt.FromGo(store.Path())
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("get"), st.rt.Function(label+".get", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := moejs.Arg(args, 0).String()
		raw, found, err := store.Get(key)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, label+".get: "+err.Error(), nil)
		}
		st.audit("config", "get", key, len(raw), "ok", map[string]any{"found": found, "scope": label})
		if !found {
			return resolveConfig(st, moejs.Null()), nil
		}
		v, err := st.rt.ParseJSON(raw)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, label+".get: 值不是合法 JSON", nil)
		}
		return resolveConfig(st, v), nil
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("set"), st.rt.Function(label+".set", 2, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := moejs.Arg(args, 0).String()
		raw, err := st.rt.AppendJSON(nil, moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, label+".set: 值不可 JSON 序列化", nil)
		}
		if err := store.Set(key, raw); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, label+".set: "+err.Error(), nil)
		}
		st.audit("config", "set", key, len(raw), "ok", map[string]any{"scope": label})
		notifyConfigChange(hc, ns)
		return resolveConfig(st, moejs.Undefined()), nil
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("delete"), st.rt.Function(label+".delete", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		key := moejs.Arg(args, 0).String()
		removed, err := store.Delete(key)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, label+".delete: "+err.Error(), nil)
		}
		st.audit("config", "delete", key, 0, "ok", map[string]any{"removed": removed, "scope": label})
		notifyConfigChange(hc, ns)
		v, err := st.rt.FromGo(removed)
		if err != nil {
			return moejs.Undefined(), err
		}
		return resolveConfig(st, v), nil
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("list"), st.rt.Function(label+".list", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		prefix := moejs.Arg(args, 0).String()
		entries, err := store.List(prefix)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, label+".list: "+err.Error(), nil)
		}
		out := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			var value any
			if err := json.Unmarshal(e.Value, &value); err != nil {
				return moejs.Undefined(), throwCoded(rr, codeOutOfRange, fmt.Sprintf("%s.list: key %q 的值损坏", label, e.Key), nil)
			}
			out = append(out, map[string]any{"key": e.Key, "value": value})
		}
		st.audit("config", "list", prefix, len(entries), "ok", map[string]any{"scope": label})
		v, err := st.rt.FromGo(out)
		if err != nil {
			return moejs.Undefined(), err
		}
		return resolveConfig(st, v), nil
	}))
	return engine.ObjectValue(obj)
}
