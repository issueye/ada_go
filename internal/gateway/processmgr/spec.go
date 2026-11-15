package processmgr

import (
	"os"
	"path/filepath"
	"strings"
)

func normalizeSpec(owner string, spec ProcessSpec) (ProcessSpec, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程缺少 name")
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程名 %q 不能包含路径分隔符", name)
	}
	entry := strings.TrimSpace(spec.Entry)
	if entry == "" {
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 缺少 entry", name)
	}
	out := spec
	out.Owner = owner
	out.Name = name
	out.Entry = entry

	scope := out.Scope
	if scope == "" {
		scope = ScopeGlobal
	}
	switch scope {
	case ScopeGlobal, ScopeWorkspace:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 scope %q 未知", name, out.Scope)
	}
	out.Scope = scope

	workspace := strings.TrimSpace(out.Workspace)
	if scope == ScopeGlobal {
		if workspace != "" {
			return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 是 global scope，不能指定 workspace", name)
		}
	} else {
		if workspace == "" {
			return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 是 workspace scope，缺少 workspace", name)
		}
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return ProcessSpec{}, wrapError(CodeInvalidSpec, err, "进程 %s 的 workspace %q 非法", name, workspace)
		}
		info, statErr := os.Stat(abs)
		if statErr != nil || !info.IsDir() {
			return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 workspace %q 不是可访问目录", name, abs)
		}
		out.Workspace = abs
	}

	rt := out.Runtime
	if rt == "" {
		rt = RuntimeBinary
	}
	switch rt {
	case RuntimeBinary, RuntimeNode:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 runtime %q 未知（只有 binary/node）", name, rt)
	}
	out.Runtime = rt

	mode := out.StartMode
	if mode == "" {
		mode = StartLazy
	}
	switch mode {
	case StartLazy, StartOnEnable:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 startMode %q 未知", name, out.StartMode)
	}
	out.StartMode = mode

	restart := out.Restart
	if restart == "" {
		restart = RestartNo
	}
	switch restart {
	case RestartNo, RestartOnFailure, RestartAlways:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 restart %q 未知", name, out.Restart)
	}
	out.Restart = restart
	if out.StopTimeout <= 0 {
		out.StopTimeout = DefaultStopTimeout
	}
	if len(out.Args) == 0 {
		out.Args = nil
	}
	if len(out.Env) == 0 {
		out.Env = nil
	}
	return out, nil
}
