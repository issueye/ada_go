package main

// pluginbuild 把官方插件的浏览器入口（client.tsx）打成单文件 ESM bundle。
//
// 约束与 docs/20 P1-2 一致：
//   - 不新增 npm 依赖：使用仓库已有的 Go esbuild；
//   - 宿主运行时（ada:render / ada:ui）保持 external，由 shim 从 globalThis 取；
//   - 产物必须通过 CSP 扫描（无 eval/new Function）、512KB 上限与内容指纹；
//   - 支持 --check，CI 用它防止 client.tsx 与 static/client.js 漂移。
//
// 用法：
//   go run ./tools/pluginbuild -dir plugins/official/provider-admin
//   go run ./tools/pluginbuild -dir plugins/official/provider-admin -check
//
// 默认入口 client.tsx、输出 static/client.js；插件 id 取 plugin.json，缺省用目录名。

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	dir := flag.String("dir", ".", "插件目录（含 plugin.json）")
	entry := flag.String("entry", "client.tsx", "浏览器入口（相对插件目录）")
	out := flag.String("out", "static/client.js", "产物路径（相对插件目录）")
	id := flag.String("id", "", "插件 id；缺省读 plugin.json / 目录名")
	check := flag.Bool("check", false, "只校验产物与源码同步，不写文件")
	flag.Parse()

	if err := Run(Options{
		Dir:   *dir,
		Entry: *entry,
		Out:   *out,
		ID:    *id,
		Check: *check,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "pluginbuild:", err)
		os.Exit(1)
	}
}
