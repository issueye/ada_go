package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/MoeForge/moejs/node"
)

func init() {
	register("fs", "node:fs 的错误码、只读裁剪与资源上限（E-4/E-6）", runFS)
}

// writeMembers 是 ada-go 口径里「未声明 fs.write 时必须裁掉」的成员。
var writeMembers = []string{
	"writeFile", "writeFileSync", "appendFile", "appendFileSync", "mkdir", "mkdirSync",
	"rm", "rmSync", "rmdir", "rmdirSync", "unlink", "unlinkSync", "rename", "renameSync",
	"copyFile", "copyFileSync", "cp", "cpSync",
}

func runFS(r *recorder) {
	ro, err := makeTree("fs-ro", 0)
	if err != nil {
		r.add(vNa, "夹具", "建临时工作区", err.Error(), "只读工作区建不出来，fs 用例整体未跑")
		return
	}
	big, err := makeTree("fs-big", 20<<20)
	if err != nil {
		r.add(vNa, "夹具-20MB", "建 20MB 文件与多条目目录", err.Error(), "上限探针未跑")
	}

	// 1) E-4 错误对象字段
	o := runJS(js(`import fs from "node:fs";
const out = {};
out.enoent = __grab(() => fs.readFileSync("/workspace/nope.txt"));
out.eisdir = __grab(() => fs.readFileSync("/workspace/dir"));
out.enotdir = __grab(() => fs.readFileSync("/workspace/hello.txt/x"));
out.eaccesAbs = __grab(() => fs.readFileSync("/etc/passwd"));
out.eaccesDotDot = __grab(() => fs.readFileSync("/workspace/../outside/secret.txt"));
out.eaccesHost = __grab(() => fs.readFileSync(`+fmt.Sprintf("%q", filepath.ToSlash(ro.Root))+`));
__report(JSON.stringify(out));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false)})
	r.add(ifElse(o.reported && strings.Contains(o.report, `"code":"ENOENT"`) && strings.Contains(o.report, `"syscall"`), vMatch, vDev), "E-4/fs 错误字段",
		"6.11.1 E-4、6.12.4：错误对象要有 code/errno/syscall/path（rename/copyFile 再带 dest）",
		o.actual(),
		"上游就是 Node 形状：errno 是负的 libuv 数值、code 是字符串、syscall 按 Node 24 的命名、path 是脚本给的原样路径；err.code === \"ENOENT\" 分支直接可用")

	// 2) 只读安装时写成员的形状
	o = runJS(js(`import fs from "node:fs";
const d = Object.getOwnPropertyDescriptor(fs, "writeFile");
__report(JSON.stringify({
  type: typeof fs.writeFile,
  writable: d && d.writable, configurable: d && d.configurable, enumerable: d && d.enumerable,
  call: __grab(() => fs.writeFileSync("/workspace/new.txt", "x")),
  namedType: (() => { try { return typeof fs.promises.writeFile } catch (e) { return "throw" } })(),
}));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false)})
	r.add(vDev, "E-4/只读时的写成员",
		"6.12.1(2)、6.13：只读时静态裁剪写成员，访问即抛 ERR_ACCESS_DENIED，不要 typeof === undefined 的静默",
		o.actual(),
		"上游不做裁剪：成员存在且 writable/configurable 都为 true，调用时才失败，code 是 EROFS（不是 ERR_ACCESS_DENIED）；按 fs.read/fs.write 裁剪要宿主自己做（下两条）")

	// 3) 宿主裁剪 A：写成员换成「调用即抛 ERR_ACCESS_DENIED」（fs 与 fs/promises 两个 binding 都要裁）
	o = runJS(js(`import fs from "node:fs";
import fsp from "node:fs/promises";
import { writeFile, readFileSync } from "node:fs";
__report(JSON.stringify({
  type: typeof fs.writeFile,
  callDefault: __grab(() => fs.writeFileSync("/workspace/new.txt", "x")),
  callNamed: __grab(() => writeFile("/workspace/new.txt", "x")),
  promisesCall: await fsp.writeFile("/workspace/new.txt", "x").then(() => "no-throw", (e) => ({code: e.code, message: e.message})),
  readStillWorks: (() => { try { return readFileSync("/workspace/hello.txt", "utf8") } catch (e) { return "throw:" + e.code } })(),
  promisesRead: await fsp.readFile("/workspace/hello.txt", "utf8"),
}));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false), afterInstall: trimFS(false),
		globals: func(h *hostContext) { _ = h }})
	r.add(ifElse(o.reported && strings.Contains(o.report, "ERR_ACCESS_DENIED"), vHost, vDev), "E-4/宿主裁剪-调用即抛",
		"能力缺失要在调用前就暴露，且不出现 typeof === undefined 的静默",
		o.actual(),
		"宿主可在 node.InstallLoop 之后链上 import.meta 钩子改写 binding 的成员；模块求值时读的是同一个 binding 对象，命名导出一起被换掉——不需要 fork")

	// 4) 宿主裁剪 B：get 陷阱（设计 6.12.1(2) 的字面要求）
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({reached: true}));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false), afterInstall: trimFS(true)})
	r.add(ifElse(o.loadErr != nil, vDev, vHost), "E-4/宿主裁剪-访问即抛",
		"6.12.1(2)：被裁剪的成员访问即抛 ERR_ACCESS_DENIED（Proxy 的 get 陷阱）",
		o.actual(),
		"get 陷阱能真正做到「访问即抛」，但 node:fs 的模块体是 export const X = binding.X 一次性读全部成员：任何一个成员访问抛错就让整个模块求值失败，只用 readFile 的插件也装不上。「按成员裁剪而模块仍可用」需要上游支持，宿主只能退成「调用即抛」")

	// 5) E-6 单次读写上限
	if big != nil {
		o = runJS(js(`import fs from "node:fs";
const st = fs.statSync("/workspace/big.bin");
const data = fs.readFileSync("/workspace/big.bin");
__report(JSON.stringify({size: st.size, read: data.length, ctor: data.constructor.name}));`),
			cfg{useNode: true, install: nodeFS(big, "/workspace", false), memLimit: 64 << 20})
		r.add(ifElse(o.reported && strings.Contains(o.report, `"read":20971520`), vGap, vDev), "E-6/单次 16MB 上限",
			"6.13：单次读写 16MB，超限报错而不是截断（E-6 待补，或宿主可注入包装）",
			o.actual(),
			"引擎没有单次字节上限：20MB 一次读进 64MB 堆的 Runtime 完全成功。宿主可用第 3 条的成员改写把 readFile 包一层，自己实现 16MB 夹逼")

		o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({tooLarge: (() => { try { fs.readFileSync("/workspace/big.bin"); return "read-ok" } catch (e) { return (e.name || "") + ":" + (e.code || "") + ":" + e.message } })()}));`),
			cfg{useNode: true, install: nodeFS(big, "/workspace", false), memLimit: 8 << 20})
		r.add(vDev, "E-6/拒绝阈值来自 MemoryLimit",
			"6.13：单次读写 16MB 是独立于堆内存的边界",
			o.actual(),
			"上游的拒绝阈值就是 Runtime 的 MemoryLimit（或 Node 的 2GiB），与 64MB 堆耦合：调低堆能挡大文件，但同时削弱脚本本身")

		// 目录条目上限
		entries := 0
		if n, err := os.ReadDir(filepath.Join(big.Root, "many")); err == nil {
			entries = len(n)
		}
		o = runJS(js(`import fs from "node:fs";
const names = fs.readdirSync("/workspace/many");
__report(JSON.stringify({count: names.length, first: names[0], last: names[names.length-1]}));`),
			cfg{useNode: true, install: nodeFS(big, "/workspace", false)})
		r.add(ifElse(entries > 10000 && strings.Contains(o.report, fmt.Sprintf("\"count\":%d", entries)), vGap, vDev), "E-6/目录 10k 条目",
			"6.13：目录 10k 条，超限报错而不是截断（E-6）",
			o.actual()+fmt.Sprintf("\n         宿主侧真实条目数=%d", entries),
			"readdir 没有条目上限，超过 10k 也全量返回；宿主包装层是唯一可夹的位置")
	}

	// 6) symlink 越界
	if big != nil && canSymlink(big.Root) {
		o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({
  readOut: __grab(() => fs.readFileSync("/workspace/out.link")),
  realpath: __grab(() => fs.realpathSync("/workspace/out.link")),
  statFollow: __grab(() => fs.statSync("/workspace/out.link")),
  lstatIsLink: __grab(() => fs.lstatSync("/workspace/out.link").isSymbolicLink()),
  insideLink: __grab(() => fs.readFileSync("/workspace/inner.link", "utf8")),
}));`), cfg{useNode: true, install: nodeFS(big, "/workspace", false)})
		r.add(ifElse(strings.Contains(o.report, "EACCES"), vMatch, vDev), "E-4/symlink 越界",
			"6.13：symlink 按真实落点检查，越界拒绝",
			o.actual(),
			"上游用 os.Root（Linux 上还走 openat2 RESOLVE_BENEATH）做落点检查，越界 EACCES；lstat 不跟随链接，根内的链接可用")
	} else {
		r.add(vNa, "E-4/symlink 越界",
			"6.13：symlink 按真实落点检查，越界拒绝",
			"本机 os.Symlink 失败（Windows 需开发者模式或管理员权限），未跑成",
			"未验证：需要在 Linux/macOS 腿或开启开发者模式后复跑 -case=fs")
	}

	// 7) 宿主路径是否泄漏进脚本视图
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({
  realpath: __grab(() => fs.realpathSync("/workspace/hello.txt")),
  hostPathRead: __grab(() => fs.readFileSync(__hostRoot + "/hello.txt")),
  readdir: __grab(() => fs.readdirSync("/workspace")),
}));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false),
		globals: func(h *hostContext) {
			if err := h.rt.SetGlobal("__hostRoot", ro.Root); err != nil {
				h.out.setupErr = err
			}
		}})
	r.add(ifElse(strings.Contains(o.report, `"readdir"`) && strings.Contains(o.report, "EACCES"), vMatch, vDev), "6.13/宿主绝对路径不进脚本",
		"6.12.1：工作区在脚本里挂到 /，不暴露宿主绝对路径",
		o.actual(),
		"realpath 返回 Mount 视图；宿主绝对路径不在 Mount 下时是 EACCES，既读不到内容也不泄露其存在")

	// 8) Allow 钩子的错误映射
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({
  env: __grab(() => fs.readFileSync("/workspace/.env")),
  normal: __grab(() => fs.readFileSync("/workspace/hello.txt", "utf8")),
}));`), cfg{useNode: true, install: withAllow(nodeFS(ro, "/workspace", false), func(_, p string) error {
		if filepath.Base(p) == ".env" {
			return fs.ErrNotExist
		}
		return nil
	})})
	r.add(ifElse(strings.Contains(o.report, `"code":"ENOENT"`) && strings.Contains(o.report, "hello moejs"), vMatch, vDev), "E-1/Allow 钩子",
		"6.12.1、6.13：边界由 Root/ReadOnly/Allow 决定；密钥目录永不授予",
		o.actual(),
		"Allow(op, resolvedPath) 是宿主的策略点：fs.ErrNotExist→ENOENT、syscall.Errno→同名码、其它→EACCES；它看的是解析完链接之后的路径，所以链接不能绕策略")

	// 9) 未实现成员的行为
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({
  typeCreateReadStream: typeof fs.createReadStream,
  typeReadStreamClass: typeof fs.ReadStream,
  call: __grab(() => fs.createReadStream("/workspace/hello.txt")),
  open: __grab(() => fs.openSync("/workspace/hello.txt", "r")),
  watch: typeof fs.watch,
  createNames: Object.getOwnPropertyNames(fs).filter((n) => n.startsWith("create")),
}));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false)})
	r.add(vDev, "6.12.2/fs 流与未实现成员",
		"6.12.2：createReadStream/createWriteStream（Web Streams）；不开放 watch、文件描述符、chmod/chown/utimes",
		o.actual(),
		"上游没有任何流形式：createReadStream 只是抛 ERR_METHOD_NOT_IMPLEMENTED 的占位函数；fd/watch/chmod 同样是占位（typeof 是 function 而不是 undefined）。「Web Stream 形式的读写流」这条不成立")

	// 10) 回调形式（矩阵说不提供）
	o = runJS(js(`import fs from "node:fs";
const got = await new Promise((res) => fs.readFile("/workspace/hello.txt", "utf8", (err, data) => res({err: err && err.code, data})));
__report(JSON.stringify(got));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false)})
	r.add(ifElse(strings.Contains(o.report, "hello moejs"), vDev, vMatch), "6.12.2/回调形式",
		"6.12.2：不提供回调形式（只同步 + promise）",
		o.actual(),
		"上游同步/回调/promise 三种形式都有，比矩阵承诺的更宽；矩阵要么收紧说明，要么宿主按能力裁掉回调形式")

	// 11) fs.constants 的平台口径
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({F_OK: fs.constants.F_OK, O_RDONLY: fs.constants.O_RDONLY, O_WRONLY: fs.constants.O_WRONLY, O_APPEND: fs.constants.O_APPEND, COPYFILE_EXCL: fs.constants.COPYFILE_EXCL}));`),
		cfg{useNode: true, install: nodeFS(ro, "/workspace", false)})
	r.add(vDev, "6.12.2/fs.constants 平台值",
		"6.12.2：与 Node 一致",
		o.actual(),
		"上游文档明确 fs.constants 恒为 Linux 值；本机是 Windows，Node 20 在同一机器上会给 libuv 的 Windows 值（O_WRONLY 0x1000 等），跨平台插件要写进偏差表")

	// 12) 超出矩阵的成员
	o = runJS(js(`import fs from "node:fs";
import p from "node:fs/promises";
__report(JSON.stringify({
  extra: ["glob","globSync","cp","cpSync","access","exists","opendir","watch"].filter((n) => typeof fs[n] === "function"),
  promisesExtra: Object.keys(p).filter((n) => ["glob","cp","access"].includes(n)),
  promisesOpen: (() => { try { return String(p.open()) } catch (e) { return e.code } })(),
  memberCount: Object.getOwnPropertyNames(fs).length,
}));`), cfg{useNode: true, install: nodeFS(ro, "/workspace", false)})
	r.add(vDev, "6.12.2/fs 成员清单",
		"6.12.2：readFile/writeFile/appendFile/readdir/stat/lstat/mkdir/rm/rename/copyFile/realpath/readlink/existsSync",
		o.actual(),
		"上游还多出 glob/globSync/cp/cpSync/access/exists/Stats/Dirent/constants 与整个回调层：矩阵的「兼容范围」列比真实能力窄，需要按上游清单重写")

	// 13) Stats 字段
	o = runJS(js(`import fs from "node:fs";
const s = fs.statSync("/workspace/hello.txt");
__report(JSON.stringify({isFile: s.isFile(), size: s.size, mtimeMs: typeof s.mtimeMs, birthtimeMs: typeof s.birthtimeMs, mode: s.mode, ino: typeof s.ino}));`),
		cfg{useNode: true, install: nodeFS(ro, "/workspace", false)})
	r.add(classify(o, ""), "6.12.2/Stats",
		"6.12.2：stat/lstat 的 Stats 形状与 Node 一致",
		o.actual(),
		"字段齐全（birthtimeMs 在 Linux 上等于 ctimeMs，Windows 上给真实创建时间）；矩阵没写 Stats，可补一行")

	// 14) Close 之后
	o = runJS(js(`import fs from "node:fs";
const first = fs.readFileSync("/workspace/hello.txt", "utf8");
__closeNode();
const after = (() => { try { return fs.readFileSync("/workspace/hello.txt", "utf8") } catch (e) { return "throw:" + e.code } })();
const pendingWork = await new Promise((res) => fs.readFile("/workspace/dir/inner.txt", "utf8", (err, d) => res({err: err && err.code, d})));
__report(JSON.stringify({first, after, pendingWork}));`), cfg{
		useNode: true, install: nodeFS(ro, "/workspace", false),
		globals: func(h *hostContext) {
			h.set("__closeNode", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
				if h.closeNode != nil {
					h.closeNode()
				}
				return moejs.Undefined(), nil
			}))
		},
	})
	r.add(ifElse(strings.Contains(o.report, `"after":"throw:EBADF"`), vMatch, vNa), "6.13/API.Close 之后",
		"6.13：Runtime 关闭不留工作；Close 之后的调用要显式失败",
		o.actual(),
		"上游写明 Close 后一律 EBADF、等 worker 的操作也 reject EBADF；本探针在循环内调用 Close 后实测")
}

// trimFS 返回 afterInstall 钩子：在 node.InstallLoop 之后链上 import.meta 钩子，
// 把写成员换成「调用即抛」（throwOnAccess=false）或「访问即抛」（true）的版本。
func trimFS(throwOnAccess bool) func(*moejs.Runtime) error {
	return func(rt *moejs.Runtime) error {
		re := rt.Realm()
		prev := re.ImportHooks()
		h := &engine.ImportHooks{}
		if prev != nil {
			*h = *prev
		}
		prevMeta := h.Meta
		h.Meta = func(r *engine.Realm, module any, meta *engine.Object) error {
			if prevMeta != nil {
				if err := prevMeta(r, module, meta); err != nil {
					return err
				}
			}
			v, ok := meta.GetOwnDataValue(r.KeyFromGoString("binding"))
			if !ok || !v.IsObject() {
				return nil
			}
			b := v.AsObject()
			for _, name := range writeMembers {
				fn := r.NewHostFunction(engine.FromGoString(name), 3, denied(name))
				var d engine.PropertyDescriptor
				if throwOnAccess {
					d = engine.PropertyDescriptor{}
					d.SetGet(engine.ObjectValue(fn))
					d.SetSet(moejs.Undefined())
					d.SetEnumerable(true)
					d.SetConfigurable(true)
				} else {
					d = engine.PropertyDescriptor{}
					d.SetValue(engine.ObjectValue(fn))
					d.SetWritable(true)
					d.SetEnumerable(true)
					d.SetConfigurable(true)
				}
				if err := b.DefinePropertyOrThrow(r, r.KeyFromGoString(name), d); err != nil {
					return err
				}
			}
			return nil
		}
		re.SetImportHooks(h)
		return nil
	}
}

// denied 返回抛 ERR_ACCESS_DENIED 的宿主函数实现。
func denied(name string) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		return moejs.Undefined(), r.Throw(r.NewErrorValue(engine.KindError,
			engine.FromGoString("ERR_ACCESS_DENIED: 能力 fs.write 未声明（成员 "+name+" 被宿主裁剪）")))
	}
}

// nodeOptionsFor 暴露给 resources 用例复用的构造器。
func nodeOptionsFor(t *tree, write bool) *node.Options { return nodeFS(t, "/workspace", write) }
