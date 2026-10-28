package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
)

// TestVNodeValidator 用真实校验器 internal/gateway/frontend.ValidateNode 跑正/负样例
// （07 §7.3 的白名单与限值；spec/vnode.schema.json 是结构层，运行期限制由 Go 侧执行）。
//
// 正样例是 docs/13-appendix.md 附录 A.3 任务卡的等价 VNode；
// 负样例逐个覆盖：onClick / style / dangerouslySetInnerHTML / javascript: href /
// 未知标签 svg 与 img / 未知组件名 / 深度 >64 / 节点 >2000 / 单串 >32KB。
func TestVNodeValidator(t *testing.T) {
	t.Parallel()

	t.Run("附录A.3任务卡的元素结构通过", func(t *testing.T) {
		t.Parallel()
		// details.tp-card[data-state] > summary > (Icon, span 标题, span 计数)
		//                        > div.tp-card__body > ul > li*3
		// 计划列表在这里用通用元素表达（A.3 的 <Todo> 组件渲染产物就是这个结构），
		// 组件 props 的白名单分歧在下一个子测试里单独钉住。
		root := el("details", map[string]any{
			"class": "tp-card", "data-state": "collapsed",
		},
			el("summary", map[string]any{"class": "tp-card__summary"},
				comp("Icon", map[string]any{"name": "i-list"}),
				el("span", map[string]any{"class": "tp-card__title"}, text("任务规划")),
				el("span", map[string]any{"class": "tp-card__count"}, text("3/5")),
			),
			el("div", map[string]any{"class": "tp-card__body"},
				el("ul", map[string]any{"class": "tp-card__list"},
					el("li", nil, text("M0 契约冻结")),
					el("li", nil, text("M0.5 测试基础设施")),
					el("li", nil, text("M1 无头内核")),
				),
			),
		)
		mustNoErr(t, "ValidateNode(任务卡)", frontend.ValidateNode(root))
		// 同一棵树的 JSON 形态也必须能解码后再通过（渲染链的真实入口是 JSON）。
		raw, err := json.Marshal(root)
		mustNoErr(t, "marshal", err)
		var decoded frontend.Node
		mustNoErr(t, "unmarshal", json.Unmarshal(raw, &decoded))
		mustNoErr(t, "ValidateNode(解码后的任务卡)", frontend.ValidateNode(decoded))
		if len(raw) > frontend.MaxOutputBytes {
			t.Errorf("任务卡序列化 %d 字节，超过输出上限 %d", len(raw), frontend.MaxOutputBytes)
		}
		mustNoErr(t, "CheckOutputSize", frontend.CheckOutputSize(raw))
	})

	t.Run("契约缺口_组件props名受元素属性白名单约束", func(t *testing.T) {
		t.Parallel()
		// spec/vnode.schema.json 的 $defs.componentProps 只做「安全底线」：
		// 禁止 on*/style/dangerouslySetInnerHTML，其余键交给各组件的 PropsSchema。
		// 但 frontend.validate 把元素属性白名单也套在 $C 的 props 上，于是
		// A.3 里合法的 steps/notes/size 目前会被拒。这里把现状钉死：
		// 报出的必须正是那个 props 名；M4 落地组件库时必须二选一（放宽 $C props
		// 校验，或把这些键纳入组件级白名单），届时本用例会失败并要求更新。
		for _, prop := range []string{"steps", "notes", "size"} {
			n := comp("TodoList", map[string]any{prop: "x"})
			err := frontend.ValidateNode(n)
			mustErrContains(t, "$C props "+prop, err, prop)
			mustErrContains(t, "$C props "+prop+" 的拒绝理由", err, "不在白名单里")
		}
		t.Log("缺口：frontend 的 $C props 校验与 vnode.schema.json 的 componentProps 语义不一致")
	})

	t.Run("事件属性被拒", func(t *testing.T) {
		t.Parallel()
		for _, attr := range []string{"onClick", "oninput", "onChange"} {
			n := el("button", map[string]any{attr: "doSomething()"})
			mustErrContains(t, "属性 "+attr, frontend.ValidateNode(n), attr)
		}
	})

	t.Run("内联样式被拒", func(t *testing.T) {
		t.Parallel()
		n := el("div", map[string]any{"style": "color: red"})
		mustErrContains(t, "style", frontend.ValidateNode(n), "style")
		mustErrContains(t, "style 的理由", frontend.ValidateNode(n), "内联 style")
	})

	t.Run("dangerouslySetInnerHTML被拒", func(t *testing.T) {
		t.Parallel()
		n := el("div", map[string]any{"dangerouslySetInnerHTML": map[string]any{"__html": "<b>x</b>"}})
		mustErrContains(t, "dangerouslySetInnerHTML", frontend.ValidateNode(n), "dangerouslySetInnerHTML")
	})

	t.Run("危险协议被拒", func(t *testing.T) {
		t.Parallel()
		cases := []struct{ attr, value, want string }{
			{"href", "javascript:alert(1)", "禁止的协议"},
			{"href", "JaVaScRiPt:alert(1)", "禁止的协议"},
			{"href", " javascript:alert(1)", "禁止的协议"},
			{"href", "data:text/html,<script>alert(1)</script>", "禁止的协议"},
			{"href", "vbscript:msgbox", "禁止的协议"},
			{"action", "javascript:x", "禁止的协议"},
		}
		for _, c := range cases {
			tag := "a"
			if c.attr == "action" {
				tag = "form"
			}
			n := el(tag, map[string]any{c.attr: c.value})
			mustErrContains(t, c.attr+"="+c.value, frontend.ValidateNode(n), c.want)
		}
		// 允许的链接面（09 §9.3）：相对路径、锚点、http(s)、mailto。
		for _, ok := range []string{"/ext/task-plan/route/x", "#top", "./rel", "../up",
			"https://example.com/a", "http://127.0.0.1:52353/", "mailto:a@b.c"} {
			mustNoErr(t, "允许链接 "+ok, frontend.ValidateNode(el("a", map[string]any{"href": ok})))
		}
	})

	t.Run("未知标签被拒", func(t *testing.T) {
		t.Parallel()
		for _, tag := range []string{"svg", "img", "script", "iframe", "object", "embed", "link", "style"} {
			err := frontend.ValidateNode(el(tag, nil))
			mustErrContains(t, "标签 "+tag, err, tag)
			if isAllowedTag(tag) {
				t.Errorf("%s 竟在白名单里", tag)
			}
		}
		for _, tag := range []string{"div", "details", "summary", "a", "form", "button", "table", "input"} {
			if !isAllowedTag(tag) {
				t.Errorf("%s 应在标签白名单里（7.3/7.8）", tag)
			}
		}
	})

	t.Run("未知组件名被拒", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"AdaModal", "Wizard", "TaskPlanCard", ""} {
			err := frontend.ValidateNode(comp(name, nil))
			mustErrContains(t, "组件 "+name, err, "组件名")
		}
		for _, name := range []string{"Button", "Icon", "TodoList", "Markdown", "Notice"} {
			_, ok := frontend.ResolveComponent(name)
			mustTrue(t, "组件白名单 "+name, ok)
		}
		// TSX 名 Todo 通过别名解析到 Go 组件 TodoList（7.7.3）。
		got, ok := frontend.ResolveComponent("Todo")
		mustTrue(t, "别名 Todo 可解析", ok)
		mustEqual(t, "别名目标", "TodoList", got)
	})

	t.Run("深度上限", func(t *testing.T) {
		t.Parallel()
		mustCount(t, "MaxDepth", 64, frontend.MaxDepth)
		ok := buildDepth(frontend.MaxDepth)
		mustNoErr(t, "深度 64 应通过", frontend.ValidateNode(ok))
		tooDeep := buildDepth(frontend.MaxDepth + 1)
		err := frontend.ValidateNode(tooDeep)
		mustErrContains(t, "深度超限", err, "深度超过")
	})

	t.Run("节点数上限", func(t *testing.T) {
		t.Parallel()
		mustCount(t, "MaxNodes", 2000, frontend.MaxNodes)
		wide := frontend.Node{T: "div", P: map[string]any{"class": "wide"}}
		for i := 0; i < frontend.MaxNodes-1; i++ {
			wide.C = append(wide.C, frontend.Node{T: "br"})
		}
		mustNoErr(t, "恰好 2000 个节点", frontend.ValidateNode(wide))
		wide.C = append(wide.C, frontend.Node{T: "br"})
		err := frontend.ValidateNode(wide)
		mustErrContains(t, "节点数超限", err, "节点数超过")
	})

	t.Run("单字符串上限", func(t *testing.T) {
		t.Parallel()
		mustCount(t, "MaxStringBytes", 32<<10, frontend.MaxStringBytes)
		edge := strings.Repeat("a", frontend.MaxStringBytes)
		mustNoErr(t, "恰好 32KB 的文本", frontend.ValidateNode(text(edge)))
		over := strings.Repeat("a", frontend.MaxStringBytes+1)
		mustErrContains(t, "文本超限", frontend.ValidateNode(text(over)), "超过单字符串上限")
		mustErrContains(t, "属性超限", frontend.ValidateNode(el("span", map[string]any{"title": over})), "超过单字符串上限")
	})

	t.Run("畸形节点", func(t *testing.T) {
		t.Parallel()
		mustErrContains(t, "缺少 t", frontend.ValidateNode(frontend.Node{}), "缺少 t")
		mustErrContains(t, "input type 枚举",
			frontend.ValidateNode(el("input", map[string]any{"type": "file"})), "不在允许取值里")
		mustErrContains(t, "form method 枚举",
			frontend.ValidateNode(el("form", map[string]any{"method": "put", "action": "/x"})), "只允许 get/post")
		mustNoErr(t, "合法表单（无 JS 主流程，7.8）", frontend.ValidateNode(
			el("form", map[string]any{"method": "post", "action": "/__interaction/respond"},
				el("input", map[string]any{"type": "hidden", "name": "requestId", "value": "01J0"}),
				el("button", map[string]any{"type": "submit"}, text("批准")),
			)))
	})

	t.Run("aria 与 data 前缀放行", func(t *testing.T) {
		t.Parallel()
		mustNoErr(t, "aria-*/data-*", frontend.ValidateNode(el("div", map[string]any{
			"class": "ada-card", "aria-live": "polite", "data-state": "open", "data-plan-id": "3",
		})))
		// 属性名先被 ToLower 再判前缀，所以 aria-BAD 与 aria-bad 等价（Schema 只收小写规范形，
		// 大小写混用在这里不会报错——记为观察项）。真正非法的是不符合 aria-/data- 形状的名字。
		mustErrContains(t, "非法 aria 名",
			frontend.ValidateNode(el("div", map[string]any{"aria_bad": "x"})), "不在白名单里")
		mustErrContains(t, "非法 data 名",
			frontend.ValidateNode(el("div", map[string]any{"data.bad": "x"})), "不在白名单里")
	})

	t.Run("报告与 Schema 限值一致", func(t *testing.T) {
		t.Parallel()
		r := frontend.Report()
		mustCount(t, "报告里的限值 maxDepth", frontend.MaxDepth, r.Limits["maxDepth"])
		mustCount(t, "报告里的限值 maxNodes", frontend.MaxNodes, r.Limits["maxNodes"])
		mustCount(t, "报告里的限值 maxStringBytes", frontend.MaxStringBytes, r.Limits["maxStringBytes"])
		mustCount(t, "报告里的限值 maxOutputBytes", frontend.MaxOutputBytes, r.Limits["maxOutputBytes"])
		if r.Limits["slotTimeoutMs"] != 100 || r.Limits["pageTimeoutMs"] != 300 {
			t.Errorf("渲染超时限值漂移: %+v", r.Limits)
		}
		// Go 白名单必须与 spec/vnode.schema.json 的 enum 逐字一致（7.3：Schema 是结构层真源）。
		assertMatchesSchema(t, "spec/vnode.schema.json",
			[]string{"$defs", "element", "properties", "t", "enum"}, frontend.AllowedTags)
		assertMatchesSchema(t, "spec/vnode.schema.json",
			[]string{"$defs", "component", "properties", "n", "enum"}, frontend.AllowedComponents)
		assertMatchesSchema(t, "spec/vnode.schema.json",
			[]string{"$defs", "elementProps", "properties"}, frontend.AllowedAttributes)
		assertMatchesSchema(t, "spec/vnode.schema.json",
			[]string{"$defs", "elementProps", "properties", "type", "enum"}, frontend.AllowedInputTypes)
		mustCount(t, "标签白名单数量", 41, len(frontend.AllowedTags))
		mustCount(t, "组件白名单数量", 27, len(frontend.AllowedComponents))
	})
}

// el 造元素节点。
func el(tag string, props map[string]any, children ...frontend.Node) frontend.Node {
	return frontend.Node{T: tag, P: props, C: children}
}

// comp 造宿主组件节点。
func comp(name string, props map[string]any, children ...frontend.Node) frontend.Node {
	return frontend.Node{T: frontend.ComponentTag, N: name, P: props, C: children}
}

// text 造文本节点（schema 约定：文本必须是 {"t":"#text","v":...}）。
func text(s string) frontend.Node { return frontend.Node{T: "#text", V: s} }

// buildDepth 造一条只有首节点的嵌套链，深度为 d。
func buildDepth(d int) frontend.Node {
	n := text("leaf")
	for i := 0; i < d-1; i++ {
		n = frontend.Node{T: "div", C: []frontend.Node{n}}
	}
	return n
}

// isAllowedTag 查 frontend.AllowedTags（校验器没导出查询函数，M0 缺口之一）。
func isAllowedTag(tag string) bool {
	for _, x := range frontend.AllowedTags {
		if x == tag {
			return true
		}
	}
	return false
}

// assertMatchesSchema 拿 spec/vnode.schema.json 里某处的集合与 Go 白名单逐字比对：
// JSON 节点是数组（enum）时比元素，是对象（properties）时比键集合。
// 这样「Schema 改了、Go 没改」和反过来的漂移都会在这里失败（10 §11.1 前端分层）。
func assertMatchesSchema(t *testing.T, rel string, path []string, goSet []string) {
	t.Helper()
	raw := readFixture(t, "..", "..", "..", rel)
	var doc any
	mustNoErr(t, "解析 "+rel, json.Unmarshal(raw, &doc))
	node := doc
	for _, key := range path {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%s 的 %v 路径上不是对象", rel, path)
		}
		node, ok = m[key]
		if !ok {
			t.Fatalf("%s 缺少路径 %v", rel, path)
		}
	}
	var want []string
	switch v := node.(type) {
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				t.Fatalf("%s %v: enum 里有非字符串项 %v", rel, path, x)
			}
			want = append(want, s)
		}
	case map[string]any:
		for k := range v {
			want = append(want, k)
		}
	default:
		t.Fatalf("%s %v: 既不是 enum 数组也不是 properties 对象", rel, path)
	}
	got := append([]string(nil), goSet...)
	schemaKey := rel + "/" + strings.Join(path, ".")
	if !sameSet(want, got) {
		t.Errorf("%s 与 Go 白名单不一致：\n  schema=%v\n  go    =%v", schemaKey, sortedStrings(want), sortedStrings(got))
	}
}

func sameSet(a, b []string) bool {
	ma := make(map[string]bool, len(a))
	for _, x := range a {
		ma[x] = true
	}
	mb := make(map[string]bool, len(b))
	for _, x := range b {
		mb[x] = true
	}
	if len(ma) != len(mb) {
		return false
	}
	for k := range ma {
		if !mb[k] {
			return false
		}
	}
	return true
}
