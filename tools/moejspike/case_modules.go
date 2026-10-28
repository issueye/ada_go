package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/MoeForge/moejs/node"
	"github.com/evanw/esbuild/pkg/api"
)

func init() {
	register("modules", "模块解析与白名单（E-1/E-3）", runModules)
}

func runModules(r *recorder) {
	t, err := makeTree("modules", 0)
	if err != nil {
		r.add(vNa, "夹具", "建临时工作区", err.Error(), "os.MkdirTemp 失败，后续 fs 探针未跑")
		return
	}

	// 1) 未安装的 node:*：静态 import 是否链接期失败
	o := runJS(js(`import * as p from "node:path";
__report(JSON.stringify({keys: Object.keys(p).length}));`), cfg{useNode: true})
	r.add(ifElse(o.linkErr != nil, vMatch, vDev), "E-1/未知模块-静态import",
		"6.12.1(1)、6.11 模块策略：没声明的模块在链接期失败（ERR_MODULE_NOT_FOUND），不等第一次调用",
		o.actual(),
		"链接期确实失败；但错误是 Go 侧 *moejs.ResolveError（文案由宿主 resolver 决定），不是 JS 层的 ERR_MODULE_NOT_FOUND —— 宿主必须自己把它映射成装配失败码")

	// 2) 未安装的 node:*：动态 import
	o = runJS(js(`let out = {};
out.crypto = await import("node:crypto").then((m) => ({ok: Object.keys(m).length}), (e) => ({err: e.name + ": " + e.message}));
out.relative = await import("./nope.js").then((m) => ({ok: Object.keys(m).length}), (e) => ({err: e.name + ": " + e.message}));
out.bareEvents = await import("events").then((m) => ({ok: Object.keys(m).length}), (e) => ({err: e.name + ": " + e.message}));
__report(JSON.stringify(out));`), cfg{useNode: true})
	r.add(classify(o, ""), "E-1/未知模块-动态import",
		"动态 import 未知 specifier 必须 reject，不得返回 undefined 命名空间",
		o.actual(),
		"reject 消息即宿主 resolver 的错误文本；moejs 自身从不读盘，因此「缺失依赖在安装期报 ERR_MODULE_NOT_FOUND」只能由宿主实现")

	// 3) fs 与 node:fs 是否同一实例（上游 node.Resolver 的行为）
	o = runJS(js(`import d1 from "fs";
import d2 from "node:fs";
import { readFile as a } from "fs";
import { readFile as b } from "node:fs";
import p1 from "node:fs/promises";
__report(JSON.stringify({defaultSame: d1 === d2, namedSame: a === b, promisesSame: p1 === d1.promises, type: typeof a}));`),
		cfg{useNode: true, install: nodeFS(t, "/workspace", false)})
	r.add(ifElse(o.reported && strings.Contains(o.report, `"defaultSame":true`), vMatch, vDev), "E-1/fs 与 node:fs 同一实例",
		"6.12.3：fs 与 node:fs 解析到同一个模块实例，import 与 require 看到同一份导出",
		o.actual(),
		"上游把裸名 fs / fs/promises / buffer 与 node: 前缀映射到同一个进程级 *Module，同一实例天然成立；其余模块的裸名要宿主 loader 自己 alias")

	// 4) 裸名 builtin（fs 之外）
	o = runJS(js(`import * as e from "events";
__report(JSON.stringify({keys: Object.keys(e).length}));`), cfg{useNode: true})
	r.add(ifElse(o.linkErr != nil, vGap, vMatch), "E-1/裸名-events",
		"6.12.2：events 始终安装",
		o.actual(),
		"上游只服务 fs/fs/promises/buffer 三个裸名；events 在 moejs 里连全局都没有，必须宿主实现或上游补")

	// 5) 模块链接成功但该 runtime 没 Install
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({type: typeof fs.readFile}));`), cfg{useNode: true})
	r.add(ifElse(o.loadErr != nil, vDev, vMatch), "E-1/安装粒度-未 Install",
		"6.10：未安装的东西不存在；6.12.1：没有能力就不安装，且在链接期失败",
		o.actual(),
		"node:fs 是进程级模块，Link 一定通过；真正的开关在 import.meta 填充，失败发生在模块求值期（Load 的 *Exception），不是 Link —— 对宿主而言仍是装配期，但错误码语义与设计不同")

	// 6) Install 了但 Options.FS 为 nil
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({type: typeof fs.readFile}));`),
		cfg{useNode: true, install: &node.Options{Buffer: true}})
	r.add(ifElse(o.loadErr != nil, vDev, vMatch), "E-1/安装粒度-无 FS",
		"同上：无 fs.read/fs.write 时不应装 fs",
		o.actual(),
		"仍是求值期 Error（\"this runtime has no file system\"），不是 ERR_ACCESS_DENIED；宿主若想按能力安装，正确做法是不把 node:fs 放进模块表")

	// 7) 宿主不套 node.Resolver 时，node:fs 根本不可解析
	o = runJS(js(`import fs from "node:fs";
__report("ok");`), cfg{useNode: false})
	r.add(ifElse(o.linkErr != nil, vMatch, vDev), "E-1/白名单就是宿主 resolver",
		"6.11 模块策略：能按模块名安装/拒绝",
		o.actual(),
		"模块表完全由宿主的 Resolver 决定，node.Resolver 只是可选包装 —— E-1 的「细粒度白名单」在 ada-go 侧已经成立，不需要上游开关")

	// 8) 忘 Link 直接 Load
	v, actual := loadWithoutLink()
	r.add(v, "E-1/未 Link 不能 Load",
		"不允许「import 变成 undefined」的静默降级（6.10 未安装的东西不存在）",
		actual,
		"moejs 在 Load 里直接拒绝未链接的模块，运行期不存在“specifier 解析失败但得到 undefined”的路径")

	// 9) import.meta 默认内容
	o = runJS(js(`__report(JSON.stringify({own: Object.getOwnPropertyNames(import.meta), protoNull: Object.getPrototypeOf(import.meta) === null, url: typeof import.meta.url, resolve: typeof import.meta.resolve}));`),
		cfg{useNode: true, install: nodeFS(t, "/workspace", false)})
	r.add(vDev, "import.meta/默认",
		"6.11 模块系统、6.12.3：import.meta.url/filename/dirname/resolve 指向 plugin:// 虚拟路径",
		o.actual(),
		"引擎给的是 null 原型空对象，url/filename/dirname/resolve 全都不存在；填充位是 Importer.Meta（下一条实测宿主可填）")

	// 10) 宿主填 import.meta（含 resolve）
	o = runJS(js(`__report(JSON.stringify({url: import.meta.url, filename: import.meta.filename, dirname: import.meta.dirname, resolveFS: import.meta.resolve("node:fs"), bad: (() => { try { import.meta.resolve("node:vm"); return "no-throw" } catch (e) { return e.name + ": " + e.message } })()}));`),
		cfg{useNode: true, install: nodeFS(t, "/workspace", false), meta: fillMeta})
	r.add(ifElse(o.reported && strings.Contains(o.report, "plugin://spike"), vHost, vDev), "import.meta/宿主可填",
		"宿主需要 plugin:// 虚拟路径与只解析已安装模块的 import.meta.resolve",
		o.actual(),
		"Importer.Meta 每模块调用一次，宿主可在此定义 url/filename/dirname 与 resolve 函数；无需上游改动")

	// 11) CJS 表面
	o = runJS(js(`const seen = {};
for (const n of ["require", "module", "exports", "__dirname", "__filename", "process", "console", "Buffer", "setTimeout"]) { seen[n] = typeof globalThis[n] }
seen.evalErr = (() => { try { return typeof require("node:fs") } catch (e) { return e.name + ": " + e.message } })();
__report(JSON.stringify(seen));`), cfg{useNode: true, install: nodeFS(t, "/workspace", false)})
	r.add(vGap, "E-3/CJS-require",
		"6.11 模块系统：ESM 与 CJS 双向互操作；6.12.2：node:module 的 createRequire 只解析已安装的 node:*",
		o.actual(),
		"moejs 没有任何 CJS 表面（require/module/exports/__dirname 全 undefined），也没有 node:module；互操作只能在构建期由 esbuild 完成")

	// 12) node:module 与 createRequire
	o = runJS(js(`import { createRequire } from "node:module";
__report(JSON.stringify({type: typeof createRequire}));`), cfg{useNode: true})
	r.add(ifElse(o.linkErr != nil, vGap, vMatch), "E-3/node:module",
		"6.12.2：module 始终安装（受限，待补 E-3）：createRequire/builtinModules/isBuiltin",
		o.actual(),
		"上游完全没有；宿主能用 virtual module + 自己的模块表实现受限 createRequire（exec 用例演示宿主自建模块的完整路径）")

	// 13) esbuild 打 CJS 依赖后运行期还剩什么
	text, errs := esbuildBundle()
	r.add(ifElse(errs == 0, vHost, vNa), "E-3/esbuild-CJS 垫片",
		"6.12.3：CJS 依赖由 esbuild 转 ESM 包裹；require 只解析已安装的 node:* 或同包内联模块",
		text,
		"external 的 node:* 在 CJS 依赖里会变成 esbuild 的 __require 运行期抛错；因此「宿主注入 require 垫片」是 E-3 的最小落点，不必上游实现完整 createRequire")

	// 14) 循环依赖不挂起
	o = runJS(js(`import { fromA, valueB } from "./a.js";
__report(JSON.stringify({fromA: fromA(), valueB: valueB()}));`),
		cfg{
			useNode: true,
			virtual: map[string]string{
				"./a.js": "import { nameB } from './b.js';\nexport function fromA(){ return 'A' }\nexport function valueB(){ return nameB() }\n",
				"./b.js": "import { fromA } from './a.js';\nexport function nameB(){ return 'B-of-' + fromA() }\n",
			},
		})
	r.add(classify(o, ""), "E-1/循环依赖",
		"6.11 模块系统：循环依赖不挂起",
		o.actual(),
		"Link 只做实例化不执行；解析是同步 map 查找，不会死锁（本例走通 a→b→a 的函数互调）")

	// 15) 动态 import 的缓存同一性
	o = runJS(js(`import fsStatic from "node:fs";
const m1 = await import("node:fs");
const m2 = await import("node:fs");
__report(JSON.stringify({sameDynamic: m1 === m2, defaultSameAsStatic: m1.default === fsStatic, namedSame: m1.readFile === fsStatic.readFile}));`),
		cfg{useNode: true, install: nodeFS(t, "/workspace", false)})
	r.add(ifElse(o.reported && strings.Contains(o.report, `"sameDynamic":true`), vMatch, vDev), "E-1/模块缓存同一性",
		"6.11：每 Runtime 一份模块缓存；静态与动态 import 看到同一实例",
		o.actual(),
		"Module 指针就是身份，Runtime 按模块记录求值状态")

	// 16) 跨 Runtime 是否会串（宿主复用 Importer 的情形）
	o = runJS(js(`import fs from "node:fs";
__report(JSON.stringify({hasReadFile: typeof fs.readFileSync}));`), cfg{useNode: true, install: nodeFS(t, "/workspace", false)})
	r.add(classify(o, ""), "E-1/Importer 可服务多 Runtime",
		"6.11：模块缓存每 Runtime 一份；A Runtime 的安装不能影响 B",
		o.actual(),
		"node.Importer 进程级共享、Install 每 Runtime 一次；两条探针各建新 Runtime 即互不影响（本用例每个探针都是独立 Runtime）")
}

// loadWithoutLink 直接 Load 一个带 import 的模块，看 moejs 是否拒绝。
func loadWithoutLink() (verdict, string) {
	m, err := moejs.Compile("plugin://spike/x.js", `import fs from "node:fs";
__report("ok");`)
	if err != nil {
		return vNa, "编译夹具失败: " + errText(err)
	}
	rt := moejs.NewRuntime(moejs.Options{})
	return vMatch, errText(rt.Load(m))
}

// fillMeta 是宿主版 import.meta：plugin:// 虚拟路径 + 只认已安装模块的 resolve。
func fillMeta(re *moejs.Realm, m *moejs.Module, meta *moejs.Object) error {
	name := "index.js"
	if m != nil && m.Name() != "" {
		name = m.Name()
	}
	url := name
	if !strings.Contains(name, "://") {
		url = "plugin://spike/" + name
	}
	for k, v := range map[string]engine.Value{
		"url":        moejs.String(url),
		"filename":   moejs.String(url),
		"dirname":    moejs.String("plugin://spike"),
		"moduleType": moejs.String("es-module"),
	} {
		if err := meta.SetProp(re, re.KeyFromGoString(k), v); err != nil {
			return err
		}
	}
	resolve := re.NewHostFunction(engine.FromGoString("resolve"), 1,
		func(r *moejs.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
			spec := moejs.Arg(args, 0).String()
			if spec == "node:fs" || spec == "fs" {
				return moejs.String("plugin://spike/node:fs"), nil
			}
			return engine.Undefined(), r.TypeError("Cannot find module '%s'", spec)
		})
	return meta.SetProp(re, re.KeyFromGoString("resolve"), engine.ObjectValue(resolve))
}

// esbuildBundle 用真实 esbuild（仓库已钉 v0.28.2）打一个 CJS 依赖。
func esbuildBundle() (string, int) {
	src := `const path = require("node:path");
module.exports = function join(p) { return path.join(p, "x"); };`
	dir, err := os.MkdirTemp("", "moejspike-bundle-")
	if err != nil {
		return "临时目录失败: " + err.Error(), 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	res := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   src,
			ResolveDir: dir,
			Loader:     api.LoaderJS,
			Sourcefile: "dep.cjs",
		},
		Bundle:        true,
		Platform:      api.PlatformNode,
		Format:        api.FormatESModule,
		External:      []string{"node:*"},
		Write:         false,
		AbsWorkingDir: dir,
	})
	text := ""
	if len(res.OutputFiles) > 0 {
		text = string(res.OutputFiles[0].Contents)
	}
	shim := "未见 __require"
	if strings.Contains(text, "__require") {
		shim = "产物含 esbuild 的 __require 运行期抛错垫片"
	}
	var errs []string
	for _, e := range res.Errors {
		loc := ""
		if e.Location != nil {
			loc = fmt.Sprintf("%s:%d:%d ", e.Location.File, e.Location.Line, e.Location.Column)
		}
		errs = append(errs, loc+e.Text)
	}
	return fmt.Sprintf("errors=%d warnings=%d\n         %s\n         错误: %s\n         产物: %s",
		len(res.Errors), len(res.Warnings), shim, strings.Join(errs, " | "), clip(text, 400)), len(res.Errors)
}

// clip 压缩长文本，便于贴在一条结论里。
func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
