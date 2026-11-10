// Package nodemods 是「宿主自建」的 Node 内置模块：path / events / util / crypto（12 §6.12）。
//
// 它们与上游 moejs 的 node 包无关：上游只提供 fs / fs.promises / buffer，其余「始终安装」
// 的模块都要 ada-go 自己实现（matrix.go 的 ProvidedHost）。本包只携带模块源码与
// 少量 Go 侧原生绑定（crypto 的摘要与随机数），注册与安装由 host 包完成。
//
// 为什么模块体是 JS 而不是 Go 对象：这些模块的形状是 Node 的对象语义（EventEmitter 的
// 原型链、util.inspect 的递归、path 的字符串算法），用 JS 写更贴近契约且可在同一个
// Runtime 里被 import；只有必须落在 Go 标准库上的部分（哈希、随机、常量时间比较）
// 才经 import.meta.binding 暴露。
package nodemods

import (
	"embed"
	"sort"
	"strings"
)

//go:embed src/*.js
var sources embed.FS

// files 是「模块名 → 源码文件」。path 的三个入口共用一份实现：
// path/posix 与 path/win32 是同一套算法上的两个视图（见 variantSource）。
var files = map[string]string{
	"path":           "src/path.js",
	"events":         "src/events.js",
	"util":           "src/util.js",
	"crypto":         "src/crypto.js",
	"zlib":           "src/zlib.js",
	"console":        "src/console.js",
	"os":             "src/os.js",
	"module":         "src/module.js",
	"timers":         "src/timers.js",
	"url":            "src/url.js",
	"querystring":    "src/querystring.js",
	"string_decoder": "src/string_decoder.js",
	"assert":         "src/assert.js",
	"stream/web":     "src/stream_web.js",
}

// Modules 返回已实现的模块名（排序后，便于与 matrix 对账）。
func Modules() []string {
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	out = append(out, "path/posix", "path/win32")
	sort.Strings(out)
	return out
}

// Source 返回模块的 ESM 源码；第二个返回值为 false 表示这个模块还没实现
// （调用方据此给出「宿主尚未提供实现」的链接期错误，而不是假兼容）。
func Source(name string) (string, bool) {
	name = strings.TrimPrefix(name, "node:")
	switch name {
	case "path/posix":
		return variantSource("posix")
	case "path/win32":
		return variantSource("win32")
	}
	return rawSource(name)
}

// variantSource 生成 node:path/posix 与 node:path/win32：把 path.js 的默认导出
// 换成对应视图，并补上 Node 在这里也会给的 sep/delimiter。
//
// 这样两个入口与 path 共用同一份算法实现（模块各自编译一次、互不 import），
// 也避免了「只 import node:path/posix 时还要先加载 node:path」的顺序依赖。
// 已知偏差：这两个模块会多出 posix/win32 两个命名导出（Node 没有）。
func variantSource(which string) (string, bool) {
	src, ok := rawSource("path")
	if !ok {
		return "", false
	}
	out := strings.Replace(src, "export default path;", "export default path."+which+";", 1)
	if out == src {
		// 源码形状变了（默认导出那行被改），宁可报「未实现」也不要给出错误视图。
		return "", false
	}
	out += "\nexport const sep = path." + which + ".sep;\n"
	out += "export const delimiter = path." + which + ".delimiter;\n"
	return out, true
}

// rawSource 读某个已实现模块的源码（不做 variant 展开）。
func rawSource(name string) (string, bool) {
	file, ok := files[name]
	if !ok {
		return "", false
	}
	src, err := sources.ReadFile(file)
	if err != nil {
		return "", false
	}
	return string(src), true
}
