package pluginapi

// 块渲染宿主测试：TSX → VNode JSON 的管线行为（7.3）。
//
// 覆盖：文本/嵌套/组件/Fragment/条件与列表、编译缓存、块档超时中断、
// 结果上限、失败必须报错（语法错误、缺默认导出、未知 import、Promise 返回）。
// 页面/插槽渲染已随 SSR 移除；RenderBlockTemplate（Markdown 动态块）是唯一出口。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testRenderer() *Renderer {
	return NewRenderer(WithComponentsModule(ComponentModuleSource([]ComponentPair{
		{Export: "Icon", Canonical: "Icon"},
		{Export: "Todo", Canonical: "TodoList"},
	})))
}

func mustRender(t *testing.T, r *Renderer, src, props string) string {
	t.Helper()
	raw, err := r.RenderBlockTemplate(src, json.RawMessage(props))
	if err != nil {
		t.Fatalf("RenderBlockTemplate: %v", err)
	}
	return string(raw)
}

func TestRenderBlockTextAndNesting(t *testing.T) {
	r := testRenderer()
	got := mustRender(t, r, `
		export default function (props) {
			return (
				<details class="tp-card" data-state="collapsed">
					<summary>任务规划 {props.done + "/" + props.total}</summary>
					<ul>
						<li class="is-done">第一步</li>
						{props.extra ? <li>附加</li> : null}
					</ul>
				</details>
			);
		}`, `{"done":3,"total":12,"extra":true}`)

	// JSON 字段名与 spec/vnode.schema.json 对齐；文本一律 #text 节点。
	for _, want := range []string{
		`{"t":"details"`,
		`"p":{"class":"tp-card","data-state":"collapsed"}`,
		`{"t":"#text","v":"任务规划 3/12"}`,
		`{"t":"li","p":{"class":"is-done"},"c":[{"t":"#text","v":"第一步"}]}`,
		`{"t":"#text","v":"附加"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("VNode 缺少 %s\n--- got ---\n%s", want, got)
		}
	}
}

func TestRenderBlockComponentsAndFragment(t *testing.T) {
	r := testRenderer()
	got := mustRender(t, r, `
		import { Icon, Todo } from "ssr:components";
		export default function (props) {
			return (
				<div class="wrap">
					<Icon name="i-list" size={12} />
					<>
						<Todo steps={props.steps} notes={props.notes} />
						<span>尾部</span>
					</>
				</div>
			);
		}`, `{"steps":[{"step":"a","status":"done"}],"notes":"n"}`)

	for _, want := range []string{
		`{"t":"$C","n":"Icon","p":{"name":"i-list","size":12},"c":[]}`,
		`{"t":"$C","n":"TodoList"`,
		`{"t":"#text","v":"尾部"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("VNode 缺少 %s\n--- got ---\n%s", want, got)
		}
	}
}

func TestRenderBlockNullResult(t *testing.T) {
	r := testRenderer()
	got := mustRender(t, r, `export default function (props) { return props.hide ? null : <p>显示</p>; }`, `{"hide":true}`)
	if got != "null" {
		t.Fatalf("空块应返回字面量 null，got %s", got)
	}
}

func TestRenderBlockCacheHit(t *testing.T) {
	r := testRenderer()
	src := `export default function (props) { return <p>{props.n}</p>; }`
	first := mustRender(t, r, src, `{"n":1}`)
	r.mu.Lock()
	entries := len(r.cache)
	r.mu.Unlock()
	if entries != 1 {
		t.Fatalf("首次渲染后缓存条目 = %d, want 1", entries)
	}
	second := mustRender(t, r, src, `{"n":2}`)
	r.mu.Lock()
	entries = len(r.cache)
	r.mu.Unlock()
	if entries != 1 || first == second {
		t.Fatalf("同源码第二次渲染应命中缓存（条目 %d）且结果随 props 变化", entries)
	}
}

// TestRenderBlockTemplateBudget 钉住块档预算：Markdown 动态块的
// renderTimeoutMs 走这里（默认值 = DefaultBlockRenderTimeout，可被装配方覆盖）。
func TestRenderBlockTemplateBudget(t *testing.T) {
	r := NewRenderer(WithBlockRenderBudget(30 * time.Millisecond))
	_, err := r.RenderBlockTemplate(`export default function () { for (;;) {} }`, nil)
	if err == nil || !strings.Contains(err.Error(), "超出预算") {
		t.Fatalf("块档超时应生效，got %v", err)
	}
}

func TestRenderBlockSizeLimit(t *testing.T) {
	r := testRenderer()
	src := `export default function () {
		const big = "#".repeat(300000);
		return <p>{big}</p>;
	}`
	if _, err := r.RenderBlockTemplate(src, nil); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("超大结果应被拒绝，got %v", err)
	}
}

func TestRenderBlockFailures(t *testing.T) {
	r := testRenderer()
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"语法错误", `export default function () { return <p>`, "TSX 编译失败"},
		{"缺默认导出", `export function Slot() { return <p>x</p>; }`, "export default"},
		{"未知 import", "import x from './side';\nexport default function () { return <p>{x}</p>; }", "链接失败"},
		{"未知组件", "import { Nope } from \"ssr:components\";\nexport default function () { return <Nope />; }", "链接失败"},
		{"函数组件不在白名单", `const F = () => <p>x</p>;
export default function () { return <F />; }`, "__ada_component"},
		{"返回 Promise", `export default async function () { return <p>x</p>; }`, "Promise"},
		{"属性带函数", `export default function () { return <p onClick={() => 1}>x</p>; }`, "函数"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.RenderBlockTemplate(tc.src, nil)
			if err == nil {
				t.Fatalf("%s 必须失败（7.3：失败必须返回错误）", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 %q 缺少 %q", err, tc.want)
			}
		})
	}
}

func TestComponentModuleSourceExports(t *testing.T) {
	src := ComponentModuleSource([]ComponentPair{{Export: "Todo", Canonical: "TodoList"}})
	if !strings.Contains(src, `export const Todo = __adaDef("TodoList");`) {
		t.Fatalf("组件桥缺 Todo→TodoList 导出：\n%s", src)
	}
	if !strings.Contains(src, "globalThis.jsx") || !strings.Contains(src, "globalThis.Fragment") {
		t.Fatalf("组件桥必须预置全局 jsx 与 Fragment")
	}
}
