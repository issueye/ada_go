// Command moejspike 是 M1-D 的前置尖刺：在 moejs v0.1.0-alpha.11 上逐条实测
// 设计 6.11/6.12/6.13 要求的行为，输出「设计期望 / 实际行为 / 差距」三元组。
//
// 用法：
//
//	go run ./tools/moejspike -list
//	go run ./tools/moejspike -case=modules
//	go run ./tools/moejspike -case=all
//
// 一次性诊断工具，不是生产代码，也不被任何生产包引用（设计 6.10 的基座边界）。
// 每条结论都必须来自这里或 internal/conformance/nodecompat 的真实运行输出。
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// verdict 是一条探针的判定。
type verdict string

const (
	vMatch verdict = "符合"   // 与设计一致，moejs 现在就能兑现
	vDev   verdict = "偏差"   // 行为存在但与设计不同（要写进偏差表）
	vGap   verdict = "缺口"   // moejs 没有，ada-go 也无法绕过（需上游或标记不可用）
	vHost  verdict = "宿主可做" // moejs 不提供，但宿主能用真实 API 实现（窄适配）
	vNa    verdict = "未验证"  // 平台/权限原因没跑成，不作为事实
)

// finding 是一条探针结论。
type finding struct {
	ID     string
	Want   string
	Actual string
	V      verdict
	Note   string
}

func (f finding) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s\n", f.V, f.ID)
	fmt.Fprintf(&b, "    期望: %s\n", oneLine(f.Want))
	fmt.Fprintf(&b, "    实际: %s\n", indent(f.Actual))
	if f.Note != "" {
		fmt.Fprintf(&b, "    判定: %s\n", oneLine(f.Note))
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n          ")
}

// recorder 收集一个用例的全部结论。
type recorder struct{ items []finding }

func (r *recorder) add(v verdict, id, want, actual, note string) {
	r.items = append(r.items, finding{V: v, ID: id, Want: want, Actual: actual, Note: note})
}

// classify 用 outcome 的主要失败面给判定：链接/加载失败按设计算「符合」，
// 跑出意外成功或空结果才算偏差。
func classify(o *outcome, wantKind string) verdict {
	switch wantKind {
	case "linkErr":
		if o.linkErr != nil {
			return vMatch
		}
		return vDev
	case "loadErr":
		if o.linkErr != nil {
			return vMatch
		}
		return vDev
	default:
		if o.compileErr != nil || o.failed() || o.setupErr != nil {
			return vDev
		}
		return vMatch
	}
}

func ifElse(cond bool, a, b verdict) verdict {
	if cond {
		return a
	}
	return b
}

// probe 是一个用例。
type probe struct {
	Name string
	Desc string
	Run  func(*recorder)
}

var probes = map[string]probe{}

func register(name, desc string, run func(*recorder)) {
	probes[name] = probe{Name: name, Desc: desc, Run: run}
}

func main() {
	caseFlag := flag.String("case", "all", "探针用例名，或 all / 逗号分隔；-list 列出全部")
	list := flag.Bool("list", false, "列出全部用例名")
	flag.Parse()

	if *list {
		names := make([]string, 0, len(probes))
		for n := range probes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("%-12s %s\n", n, probes[n].Desc)
		}
		return
	}

	selected := strings.Split(*caseFlag, ",")
	if *caseFlag == "all" {
		selected = selected[:0]
		names := make([]string, 0, len(probes))
		for n := range probes {
			names = append(names, n)
		}
		sort.Strings(names)
		selected = names
	}

	counts := map[verdict]int{}
	for _, name := range selected {
		p, ok := probes[strings.TrimSpace(name)]
		if !ok {
			fmt.Fprintf(os.Stderr, "未知用例 %q；可用：见 -list\n", name)
			os.Exit(2)
		}
		rec := &recorder{}
		fmt.Printf("\n=== case %s — %s ===\n", p.Name, p.Desc)
		p.Run(rec)
		for _, f := range rec.items {
			fmt.Println(f.String())
			counts[f.V]++
		}
	}
	fmt.Printf("\n=== 汇总: 符合 %d · 宿主可做 %d · 偏差 %d · 缺口 %d · 未验证 %d ===\n",
		counts[vMatch], counts[vHost], counts[vDev], counts[vGap], counts[vNa])
}

// prelude 是每条 JS 探针共用的取样助手：把错误对象的可比较字段一次性抽干。
const prelude = `
const __shape = (e) => (e === null || e === undefined) ? String(e) : ({
  name: e.name, message: e.message, code: e.code, errno: e.errno, syscall: e.syscall,
  path: e.path, dest: e.dest, own: Object.getOwnPropertyNames(e),
  stack: String(e.stack).split("\n").slice(0,3).join(" | "),
});
const __grab = (fn) => { try { const v = fn(); return v && v.then ? v.catch((e) => ({thrown: __shape(e)})) : {ok: v} } catch (e) { return {thrown: __shape(e)} } };
`

// js 把 prelude 与探针源码拼起来。
func js(body string) string { return prelude + "\n" + body }
