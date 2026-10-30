package host

// 本文件是 node:fs / node:fs/promises 的宿主守卫与 ctx.fs 的落点（12 §6.12.1(2)、6.13）。
//
// 分工（D-7 实测结论，见 docs/decisions.md）：
//   - 根、只读、错误码形状由上游 moejs node 包的 FSOptions{Root, Mount, Write, Allow} 执行；
//   - 宿主在这里补三件上游没有的事：被裁剪成员的「调用即抛」ERR_ACCESS_DENIED、
//     16MB/10k 上限（E-6，上游只有 Runtime 的 MemoryLimit 兜底）、每次受控访问的审计。
//
// 一处有意偏差：被裁剪成员不能做成「访问即抛」（Proxy 的 get 陷阱）。上游模块体是
// `export const writeFile = binding.writeFile` 一次性读全部成员，任一成员在访问时抛错
// 会让整个 node:fs 求值失败，只读 readFile 的插件也装不上。因此退成「调用即抛」：
// 成员存在、调用即失败、错误带 capability 字段，不出现 typeof === undefined 的静默差异。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	mojsnode "github.com/MoeForge/moejs/node"

	"github.com/adag/ada-go/internal/plugins"
)

// fsGuard 是一个 Runtime 的文件系统边界：同一份 Root/Mount/Write 也交给上游。
type fsGuard struct {
	st       *capsState
	root     string // 真实工作区根（绝对路径）
	mount    string // 脚本视图里的挂载点（默认 /workspace）
	write    bool
	promises bool // 本次包装的是否是 fs/promises 模块
	limits   Limits
}

func init() {
	// ctx.fs / ctx.exec 的成员由能力安装方补（agentapi.go 只装身份与通用桥）。
	InstallContextMembers = installContextMembers
}

func newFSGuard(st *capsState) (*fsGuard, error) {
	root := ""
	if st.caps != nil {
		root = st.caps.WorkspaceRoot
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("host: fs 能力需要工作区根（CapabilitySet.WorkspaceRoot 为空）")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("host: 工作区根 %q 无法解析: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("host: 工作区根 %q 不可访问: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("host: 工作区根 %q 不是目录", abs)
	}
	mount := "/workspace"
	if st.caps != nil && st.caps.Mount != "" {
		mount = st.caps.Mount
	}
	return &fsGuard{
		st: st, root: abs, mount: mount,
		write:  st.caps != nil && st.caps.Has(plugins.CapFSWrite),
		limits: st.limits,
	}, nil
}

// options 交给上游 node 包：根、挂载点、只读、逐次访问审批。
func (g *fsGuard) options() *mojsnode.FSOptions {
	return &mojsnode.FSOptions{
		Root:    g.root,
		Mount:   g.mount,
		Cwd:     g.mount,
		Write:   g.write,
		Workers: 4,
		Allow: func(op, p string) error {
			// p 是上游解析过符号链接后的绝对虚拟路径：越界在这里已经不可能，
			// 这里只做路径长度上限与审计（09 §9.5、12 §6.12.1(3)）。
			if n := len(p); n > g.limits.MaxPathBytes {
				return syscall.ENAMETOOLONG
			}
			result := "ok"
			if op == "write" && !g.write {
				result = codeAccessDenied
			}
			g.st.audit("fs", op, p, 0, result, map[string]any{"mount": g.mount})
			return nil
		},
	}
}

// realPath 把脚本给的虚拟路径映射到工作区内的真实路径；越界一律拒绝。
//
// 相对路径按挂载点解析（与上游 Cwd=Mount 一致）；".." 在拼接前就被 path.Clean
// 消掉，拼接后再按前缀复核一次，防止符号链接之外的第二条逃逸路径。
func (g *fsGuard) realPath(virtual string) (string, error) {
	v := strings.ReplaceAll(strings.TrimSpace(virtual), "\\", "/")
	if v == "" {
		return "", throwCodedPath(g, fmt.Errorf("路径为空"))
	}
	var clean string
	if strings.HasPrefix(v, "/") {
		// 绝对虚拟路径必须落在挂载点下：/workspace/../etc 这种不重解释，直接拒绝。
		clean = path.Clean(v)
		if clean == "/" {
			clean = g.mount
		}
		if clean != g.mount && !strings.HasPrefix(clean, g.mount+"/") {
			return "", throwCodedPath(g, fmt.Errorf("路径 %q 越出工作区根（挂载点 %s）", baseForLog(v), g.mount))
		}
	} else {
		// 相对路径按挂载点解析（与上游 Cwd=Mount 一致）；".." 最多夹到挂载点。
		clean = path.Join(g.mount, path.Clean("/"+v))
		if clean != g.mount && !strings.HasPrefix(clean, g.mount+"/") {
			return "", throwCodedPath(g, fmt.Errorf("路径 %q 越出工作区根（挂载点 %s）", baseForLog(v), g.mount))
		}
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(clean, g.mount), "/")
	real := filepath.Join(g.root, filepath.FromSlash(rel))
	abs, err := filepath.Abs(real)
	if err != nil {
		return "", throwCodedPath(g, err)
	}
	if abs != g.root && !strings.HasPrefix(abs, g.root+string(filepath.Separator)) {
		return "", throwCodedPath(g, fmt.Errorf("路径 %q 越出工作区根", baseForLog(virtual)))
	}
	return abs, nil
}

func throwCodedPath(g *fsGuard, err error) error {
	return &policyError{Code: codeAccessDenied, Message: err.Error(), Fields: map[string]any{"capability": "fs.read"}}
}

// checkpointMetaOf 从当前调用上下文取检查点归属（M5）。
// Tool 由检查点账本（Executor）记录，这里不再重复传。
func checkpointMetaOf(hc Context) CheckpointMeta {
	return CheckpointMeta{ThreadID: hc.ThreadID, RunID: hc.RunID, CallID: hc.CallID}
}

// fsRealPath 是 capsState 上的便捷入口（exec 的 cwd 也要过同一份边界）。
//
// 没有 fs 能力时仍要能解析 cwd：用一个只做路径映射的 guard（同一份根与挂载点、
// 同一套越界规则），但它不装 fs 模块，也不给 ctx.fs。
func (st *capsState) fsRealPath(virtual string) (string, error) {
	if st.fs != nil {
		return st.fs.realPath(virtual)
	}
	root := ""
	mount := DefaultMount
	if st.caps != nil {
		root = st.caps.WorkspaceRoot
		if st.caps.Mount != "" {
			mount = st.caps.Mount
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", throwCodedPath(nil, err)
	}
	return (&fsGuard{root: abs, mount: mount, limits: st.limits}).realPath(virtual)
}

// fsWriteMembers 是被裁剪的写成员（12 §6.12.2「fs 偏差」列：v1 不开放的写面）。
var fsWriteMembers = []string{
	"writeFile", "appendFile", "mkdir", "rm", "rmdir", "unlink", "rename", "copyFile", "cp",
	"truncate", "chmod", "chown", "utimes", "lutimes", "symlink", "link", "writev", "open",
	"writeFileSync", "appendFileSync", "mkdirSync", "rmSync", "rmdirSync", "unlinkSync",
	"renameSync", "copyFileSync", "cpSync", "truncateSync", "chmodSync", "chownSync",
	"utimesSync", "lutimesSync", "symlinkSync", "linkSync", "writevSync", "openSync",
	"promises.writeFile", "promises.appendFile", "promises.mkdir", "promises.rm", "promises.rmdir",
	"promises.unlink", "promises.rename", "promises.copyFile", "promises.cp", "promises.truncate",
	"promises.chmod", "promises.chown", "promises.utimes", "promises.lutimes", "promises.symlink",
	"promises.link", "promises.open",
}

// fsReadMembers 是需要夹上限的读成员（E-6：上游没有 16MB/10k）。
var fsReadMembers = []string{
	"readFile", "readFileSync", "readdir", "readdirSync",
	"promises.readFile", "promises.readdir",
}

// wrapBindings 按能力裁剪上游 node:fs 的成员并加上限（caps.go fillMeta 调用）。
//
// binding 是上游 node 包的 import.meta.binding：成员就是 fs 的函数本身。
func (g *fsGuard) wrapBindings(r *engine.Realm, binding *engine.Object, promises bool) error {
	if binding == nil {
		return nil
	}
	g.promises = promises
	for _, name := range fsWriteMembers {
		if promises && !strings.HasPrefix(name, "promises.") {
			continue
		}
		if !promises && strings.HasPrefix(name, "promises.") {
			continue
		}
		key := r.KeyFromGoString(name)
		if !binding.HasOwnProperty(key) {
			continue
		}
		if g.write {
			// 有 fs.write：保留上游实现，只加上限（写超限报 ERR_FS_FILE_TOO_LARGE）。
			if err := g.wrapLimit(r, binding, key, name); err != nil {
				return err
			}
			continue
		}
		if err := g.denyMember(r, binding, key, name); err != nil {
			return err
		}
	}
	for _, name := range fsReadMembers {
		if promises && !strings.HasPrefix(name, "promises.") {
			continue
		}
		if !promises && strings.HasPrefix(name, "promises.") {
			continue
		}
		key := r.KeyFromGoString(name)
		if !binding.HasOwnProperty(key) {
			continue
		}
		if err := g.wrapLimit(r, binding, key, name); err != nil {
			return err
		}
	}
	return nil
}

// denyMember 把被裁剪的成员换成「调用即抛 ERR_ACCESS_DENIED」。
func (g *fsGuard) denyMember(r *engine.Realm, binding *engine.Object, key engine.PropertyKey, name string) error {
	fn := r.NewHostFunction(engine.FromGoString(name), 3, func(_ *engine.Realm, _ engine.Value, _ []engine.Value) (moejs.Value, error) {
		g.st.audit("fs", name, "", 0, codeAccessDenied, nil)
		return moejs.Undefined(), throwCoded(r, codeAccessDenied,
			fmt.Sprintf("fs.%s 需要能力 fs.write，清单未声明（12 §6.12.2；成员存在但调用即失败）", name),
			map[string]any{"capability": string(plugins.CapFSWrite), "method": name})
	})
	return binding.DefinePropertyOrThrow(r, key, engine.DataDescriptor(engine.ObjectValue(fn), 1|2|4))
}

// wrapLimit 给成员包一层 16MB/10k 上限（E-6）。
func (g *fsGuard) wrapLimit(r *engine.Realm, binding *engine.Object, key engine.PropertyKey, name string) error {
	orig, ok := binding.GetOwnDataValue(key)
	if !ok || !engine.IsCallable(orig) {
		return nil
	}
	base := name
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[i+1:]
	}
	isWrite := strings.HasPrefix(base, "write") || strings.HasPrefix(base, "append")
	isReaddir := strings.HasPrefix(base, "readdir")
	wrapped := r.NewHostFunction(engine.FromGoString(name), 3, func(rr *engine.Realm, this engine.Value, args []engine.Value) (moejs.Value, error) {
		switch {
		case isWrite:
			if data := moejs.Arg(args, 1); len(data.String()) > g.limits.MaxFileBytes && data.IsString() {
				return moejs.Undefined(), throwCoded(rr, codeFileTooLarge,
					fmt.Sprintf("%s 的单次写入上限是 %d 字节", name, g.limits.MaxFileBytes),
					map[string]any{"limit": g.limits.MaxFileBytes})
			}
			// 检查点在覆盖之前登记（M5）：node:fs 的 writeFile/appendFile 与 ctx.fs.writeText
			// 走同一份持久化账目。CallID 为空（钩子/装配期）由回调忽略。
			if hc := g.st.currentContext(); hc.Checkpoint != nil {
				hc.Checkpoint(checkpointMetaOf(hc), moejs.Arg(args, 0).String())
			}
		case strings.HasPrefix(base, "read") && !isReaddir:
			if err := g.checkFileSize(moejs.Arg(args, 0).String()); err != nil {
				return moejs.Undefined(), err
			}
		}
		res, err := rr.Call(orig, this, args)
		if err != nil {
			return moejs.Undefined(), err
		}
		if isReaddir {
			return g.limitEntries(rr, res, name)
		}
		return res, nil
	})
	return binding.DefinePropertyOrThrow(r, key, engine.DataDescriptor(engine.ObjectValue(wrapped), 1|2|4))
}

// checkFileSize 在读取前按 stat 拦下超过 16MB 的文件（比读进堆再判更安全）。
func (g *fsGuard) checkFileSize(virtual string) error {
	real, err := g.realPath(virtual)
	if err != nil {
		return err
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil // 不存在交给上游报 ENOENT
	}
	if info.IsDir() {
		return nil // 目录交给上游报 EISDIR
	}
	if info.Size() > int64(g.limits.MaxFileBytes) {
		return &policyError{
			Code:    codeFileTooLarge,
			Message: fmt.Sprintf("文件 %s 有 %d 字节，超过单次读取上限 %d", baseForLog(virtual), info.Size(), g.limits.MaxFileBytes),
			Fields:  map[string]any{"size": info.Size(), "limit": g.limits.MaxFileBytes},
		}
	}
	return nil
}

// limitEntries 对 readdir 的结果做条目数上限；Promise 结果用 then 链上同一检查。
func (g *fsGuard) limitEntries(r *engine.Realm, res moejs.Value, name string) (moejs.Value, error) {
	check := func(v moejs.Value) error {
		if !v.IsObject() {
			return nil
		}
		obj := v.AsObject()
		n := 0
		for ; ; n++ {
			if _, err := obj.GetIndex(r, uint32(n)); err != nil {
				break
			}
			if n > g.limits.MaxDirEntries {
				return &policyError{
					Code:    codeOutOfRange,
					Message: fmt.Sprintf("%s 的条目数超过上限 %d", name, g.limits.MaxDirEntries),
					Fields:  map[string]any{"limit": g.limits.MaxDirEntries},
				}
			}
		}
		return nil
	}
	if err := check(res); err != nil {
		return moejs.Undefined(), err
	}
	then, err := r.GetMethod(res, r.KeyFromGoString("then"))
	if err != nil || !engine.IsCallable(then) {
		return res, nil
	}
	onOk := r.NewHostFunction(engine.FromGoString("__ada_readdir"), 1, func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		v := moejs.Arg(args, 0)
		if err := check(v); err != nil {
			return moejs.Undefined(), err
		}
		return v, nil
	})
	if _, err := r.Call(then, res, []engine.Value{engine.ObjectValue(onOk)}); err != nil {
		return moejs.Undefined(), err
	}
	return res, nil
}

// installContextMembers 给 ToolContext 补 fs / exec（05 §6.3：按能力安装）。
func installContextMembers(rt *mojsRuntime, put func(name string, v moejs.Value) error, kind callKind, caps *CapabilitySet, hc Context) error {
	st, ok := stateFor(rt)
	if !ok {
		return nil
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if caps != nil && (caps.Has(plugins.CapFSRead) || caps.Has(plugins.CapFSWrite)) {
		keep(put("fs", ctxFSObject(st, hc)))
	}
	if err := addExecContextMember(st, hc, put); err != nil {
		keep(err)
	}
	return firstErr
}

// ctxFSObject 是 ctx.fs：五个成员（readText/writeText/list/stat/exists）。
//
// 每个成员都是「预绑 op」的原生函数，形状与 05 §6.3 的 ToolContext.fs 一致；
// 未声明能力时整个属性不出现（不是 undefined 占位）。
func ctxFSObject(st *capsState, hc Context) moejs.Value {
	r := st.rt.Realm()
	base := ctxFSMember(st, hc)
	obj := r.NewObject()
	for _, op := range []string{"readText", "writeText", "list", "stat", "exists"} {
		name := op
		fn := st.rt.Function("fs."+name, 2, func(rr *engine.Realm, this engine.Value, args []engine.Value) (moejs.Value, error) {
			all := make([]engine.Value, 0, len(args)+1)
			all = append(all, moejs.String(name))
			all = append(all, args...)
			return base(rr, this, all)
		})
		_ = obj.SetProp(r, r.KeyFromGoString(name), fn)
	}
	return engine.ObjectValue(obj)
}

// ctxFSMember 是 ctx.fs：readText/writeText/list/stat/exists，全部返回 Promise。
func ctxFSMember(st *capsState, hc Context) moejs.NativeFunc {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		op := moejs.Arg(args, 0).String()
		virtual := moejs.Arg(args, 1).String()
		guard := st.fs
		if guard == nil {
			return moejs.Undefined(), throwCoded(st.rt.Realm(), codeAccessDenied, "该 Runtime 没有安装 fs 边界", nil)
		}
		base := hc.Signal
		if base == nil {
			base = context.Background()
		}
		promise, settle := st.loop.NewPromise()
		go func() {
			settle(func(rt *mojsRuntime) (moejs.Value, error) {
				r := rt.Realm()
				real, err := guard.realPath(virtual)
				if err != nil {
					return moejs.Undefined(), err
				}
				switch op {
				case "readText":
					limit := guard.limits.MaxFileBytes
					if len(args) > 2 && moejs.Arg(args, 2).IsObject() {
						if v, err := rt.Get(moejs.Arg(args, 2), "maxBytes"); err == nil {
							if n, ok := jsInt(v); ok && n > 0 && int(n) < limit {
								limit = int(n)
							}
						}
					}
					data, err := os.ReadFile(real)
					if err != nil {
						return moejs.Undefined(), err
					}
					if len(data) > limit {
						return moejs.Undefined(), throwCoded(r, codeFileTooLarge,
							fmt.Sprintf("文件 %s 有 %d 字节，超过上限 %d", baseForLog(virtual), len(data), limit),
							map[string]any{"size": len(data), "limit": limit})
					}
					guard.st.audit("fs", "readText", virtual, len(data), "ok", nil)
					return moejs.String(string(data)), nil
				case "writeText":
					if !guard.write {
						return moejs.Undefined(), throwCoded(r, codeAccessDenied,
							"ctx.fs.writeText 需要能力 fs.write", map[string]any{"capability": string(plugins.CapFSWrite)})
					}
					text := moejs.Arg(args, 2).String()
					if len(text) > guard.limits.MaxFileBytes {
						return moejs.Undefined(), throwCoded(r, codeFileTooLarge,
							fmt.Sprintf("单次写入上限是 %d 字节", guard.limits.MaxFileBytes),
							map[string]any{"limit": guard.limits.MaxFileBytes})
					}
					// 检查点在覆盖之前登记（M5）：备份的是「被覆盖前的内容」。
					// CallID 为空说明不在 Rollback 工具调用里，回调自己忽略。
					if hc.Checkpoint != nil {
						hc.Checkpoint(checkpointMetaOf(hc), virtual)
					}
					// 父目录由宿主补建（如 .adag/todo/）；real 已过工作区根校验。
					if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
						return moejs.Undefined(), err
					}
					if err := os.WriteFile(real, []byte(text), 0o644); err != nil {
						return moejs.Undefined(), err
					}
					guard.st.audit("fs", "writeText", virtual, len(text), "ok", nil)
					return rt.FromGo(map[string]any{"bytes": len(text)})
				case "list":
					entries, err := os.ReadDir(real)
					if err != nil {
						return moejs.Undefined(), err
					}
					if len(entries) > guard.limits.MaxDirEntries {
						return moejs.Undefined(), throwCoded(r, codeOutOfRange,
							fmt.Sprintf("目录条目数 %d 超过上限 %d", len(entries), guard.limits.MaxDirEntries),
							map[string]any{"limit": guard.limits.MaxDirEntries})
					}
					out := make([]map[string]any, 0, len(entries))
					for _, entry := range entries {
						info, ierr := entry.Info()
						size := int64(0)
						if ierr == nil {
							size = info.Size()
						}
						out = append(out, map[string]any{"name": entry.Name(), "dir": entry.IsDir(), "size": size})
					}
					guard.st.audit("fs", "list", virtual, len(entries), "ok", nil)
					return rt.FromGo(out)
				case "stat":
					info, err := os.Stat(real)
					if err != nil {
						return moejs.Undefined(), err
					}
					return rt.FromGo(map[string]any{
						"size": info.Size(), "mtime": info.ModTime().UTC().Format(timeRFC3339), "dir": info.IsDir(),
					})
				case "exists":
					if _, err := os.Stat(real); err != nil {
						return engine.Bool(false), nil
					}
					return engine.Bool(true), nil
				default:
					return moejs.Undefined(), r.TypeError("ctx.fs 没有成员 %q（只有 readText/writeText/list/stat/exists）", op)
				}
			})
		}()
		_ = base
		return promise, nil
	}
}

// timeRFC3339 与 time.RFC3339 同值；提出来是为了让上面的 map 字面量不被格式化拆行。
const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

// promises 记录本次包装的是 fs 还是 fs/promises（诊断用）。
func (g *fsGuard) isPromises() bool { return g.promises }
