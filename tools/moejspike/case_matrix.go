package main

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moejs/webapi"
	hostnode "github.com/adag/ada-go/internal/plugins/host/node"
)

func init() {
	register("matrix", "安装矩阵与全局名逐个实测（对照 internal/plugins/host/node/matrix.go）", runMatrix)
}

// globalProbe 逐个报出全局名是否存在（typeof，不触发任何 getter）。
func globalProbe(names []string) string {
	return js(fmt.Sprintf(`const out = {};
for (const n of %s) { out[n] = typeof globalThis[n] }
__report(JSON.stringify(out));`, jsArray(names)))
}

// runMatrix 把 M0 冻结的矩阵逐条丢给真实 moejs，输出「矩阵声明 vs 实际」。
func runMatrix(r *recorder) {
	t, err := makeTree("matrix", 0)
	if err != nil {
		r.add(vNa, "夹具", "建临时工作区", err.Error(), "matrix 用例未跑")
		return
	}
	ro := nodeFS(t, "/workspace", false)

	var lines []string
	for _, m := range hostnode.Modules() {
		src := js(fmt.Sprintf("import * as ns from %q;\n__report(JSON.stringify({keys: Object.keys(ns).length}));", "node:"+m.Name))
		o := runJS(src, cfg{useNode: true, install: ro})
		state := "EVAL-FAIL"
		switch {
		case o.linkErr != nil:
			state = "LINK-FAIL " + shortErr(o.linkErr)
		case o.reported:
			state = "IMPORT-OK " + o.report
		case o.loadErr != nil:
			state = "EVAL-FAIL " + shortErr(o.loadErr)
		}
		lines = append(lines, fmt.Sprintf("%-16s tier=%-10s gap=%-4s -> %s", m.Name, m.Tier, orDash(m.Gap), clip(state, 150)))
	}
	r.add(vDev, "6.12.2/模块矩阵逐条",
		"矩阵里 TierAlways/TierLoop/TierCapability 的模块都按 moejs 能力可安装",
		strings.Join(lines, "\n         "),
		"只有 fs / fs/promises / buffer 三个链接成功；console/path/url/querystring/string_decoder/events/util/assert/crypto/zlib/stream/web/process/os/module/timers/timers/promises/child_process 在 alpha.11 里全部 LINK-FAIL —— 矩阵的「始终安装」目前全是宿主工作量")

	names := []string{
		"Buffer", "process", "console", "URL", "URLSearchParams", "fetch", "Headers", "Request", "Response",
		"AbortController", "AbortSignal", "EventTarget", "Event", "DOMException", "performance",
		"TextEncoder", "TextDecoder", "structuredClone", "queueMicrotask", "atob", "btoa",
		"ReadableStream", "WritableStream", "TransformStream", "Blob", "File", "FormData",
		"setTimeout", "setInterval", "setImmediate", "crypto", "SharedArrayBuffer", "Atomics", "Worker",
	}
	plain := runJS(globalProbe(names), cfg{})
	r.add(vDev, "6.10/L1 裸 Runtime 全局",
		"6.11：语言与内建覆盖 Node 20 常用面；6.12.2：console/process/Buffer 等由 stdlib 安装",
		plain.actual(),
		"裸 Runtime 只有 ECMAScript 内建 + 引擎自带的 TextEncoder/TextDecoder/structuredClone/queueMicrotask/SharedArrayBuffer/Atomics；Node 的 console/process/Buffer/setTimeout 一律没有")

	full := runJS(globalProbe(names), cfg{useNode: true, install: ro, web: &webapi.Options{}})
	r.add(vDev, "6.10/装齐 node+webapi 的全局面",
		"6.12.2「始终安装」与 Web 数据 API 一行：URL/TextEncoder/atob/btoa/structuredClone/Blob/File/FormData/DOMException/performance/AbortController",
		full.actual(),
		"装上 webapi 后有 fetch/URL/Streams/Abort*/EventTarget/DOMException/performance，但没有 Blob/File/FormData（上游文档写明不提供）；Buffer 由 node 包 Buffer:true 给出；console/process 仍不存在，必须宿主自己 SetGlobal")

	r.add(vDev, "6.11/SharedArrayBuffer 与 Atomics",
		"6.11 二进制与编码一行：插件 Runtime 不提供 SharedArrayBuffer/Atomics",
		plain.actual(),
		"引擎的 coldGlobals 里有 SharedArrayBuffer 与 Atomics（见上面两项），设计里的「不提供」目前没有开关；宿主可用 MutableIntrinsics 删除，或提上游补禁用位")
}

// shortErr 只取错误首段，方便贴表。
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	return clip(strings.ReplaceAll(err.Error(), "\n", " "), 120)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// jsArray 把 Go 字符串切片渲染成 JS 数组字面量。
func jsArray(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	return "[" + strings.Join(q, ",") + "]"
}
