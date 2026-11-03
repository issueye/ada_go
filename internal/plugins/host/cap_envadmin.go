package host

// ctx.env —— 环境变量模块的管理面（capability: env.admin，仅 builtin）。
//
// 只通过 internal/envstore 读写两份固定文件：
//   - global：~/.adago/env.json（ADAGO_HOME 优先）
//   - workspace：<工作区根>/.adag/env.json
// 脚本不能传路径；键值校验、大小上限、0600 原子写都在 envstore 里，宿主只做
// 能力门禁、scope 解析与审计（只记键名与路径，不记值）。

import (
	"fmt"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/envstore"
	"github.com/adag/ada-go/internal/plugins"
)

// envAdminPath 把脚本侧 scope 解析成固定路径。
func envAdminPath(st *capsState, scope string) (string, error) {
	switch scope {
	case "global":
		return envstore.GlobalPath()
	case "workspace":
		return envstore.WorkspacePath(st.caps.WorkspaceRoot)
	default:
		return "", fmt.Errorf(`scope 只能是 "global" 或 "workspace"（收到 %q）`, scope)
	}
}

// envAdminView 是 ctx.env.list() 的返回：两层原文 + 合并结果 + 警告。
func envAdminView(st *capsState) map[string]any {
	warnings := []string{}
	loadLayer := func(scope string) (string, map[string]string) {
		path, err := envAdminPath(st, scope)
		if err != nil {
			warnings = append(warnings, scope+": "+err.Error())
			return "", map[string]string{}
		}
		vars, layerWarns, err := envstore.LoadFile(path)
		if err != nil {
			warnings = append(warnings, scope+": "+err.Error())
			return path, map[string]string{}
		}
		warnings = append(warnings, layerWarns...)
		return path, vars
	}
	globalPath, globalVars := loadLayer("global")
	workspacePath, workspaceVars := loadLayer("workspace")
	merged := make(map[string]string, len(globalVars)+len(workspaceVars))
	for key, value := range globalVars {
		merged[key] = value
	}
	for key, value := range workspaceVars {
		merged[key] = value
	}
	return map[string]any{
		"global": map[string]any{
			"path": globalPath,
			"vars": globalVars,
		},
		"workspace": map[string]any{
			"path": workspacePath,
			"vars": workspaceVars,
		},
		"merged":   merged,
		"warnings": warnings,
	}
}

// addEnvAdminContextMember 给 ToolContext 补 ctx.env（capability: env.admin）。
func addEnvAdminContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil || !st.caps.Has(plugins.CapEnvAdmin) {
		return nil
	}
	r := st.rt.Realm()
	obj := r.NewObject()

	_ = obj.SetProp(r, r.KeyFromGoString("list"), st.rt.Function("env.list", 0, func(rr *engine.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		return st.rt.FromGo(envAdminView(st))
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("set"), st.rt.Function("env.set", 3, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		scope := moejs.Arg(args, 0).String()
		key := moejs.Arg(args, 1).String()
		value := moejs.Arg(args, 2).String()
		path, err := envAdminPath(st, scope)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.env.set: "+err.Error(), nil)
		}
		if err := envstore.ValidateKey(key); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.env.set: "+err.Error(), nil)
		}
		if err := envstore.ValidateValue(value); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.env.set: "+err.Error(), nil)
		}
		if _, err := envstore.Set(path, key, value); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.env.set: "+err.Error(), nil)
		}
		st.audit("env", "set", key, len(value), "ok", map[string]any{"scope": scope, "path": baseForLog(path)})
		return st.rt.FromGo(map[string]any{"scope": scope, "path": path, "key": key, "changed": true})
	}))

	_ = obj.SetProp(r, r.KeyFromGoString("remove"), st.rt.Function("env.remove", 2, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		scope := moejs.Arg(args, 0).String()
		key := moejs.Arg(args, 1).String()
		path, err := envAdminPath(st, scope)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.env.remove: "+err.Error(), nil)
		}
		if err := envstore.ValidateKey(key); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.env.remove: "+err.Error(), nil)
		}
		_, removed, err := envstore.Unset(path, key)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.env.remove: "+err.Error(), nil)
		}
		st.audit("env", "remove", key, 0, "ok", map[string]any{"scope": scope, "path": baseForLog(path), "removed": removed})
		return st.rt.FromGo(map[string]any{"scope": scope, "path": path, "key": key, "removed": removed})
	}))

	return put("env", engine.ObjectValue(obj))
}
