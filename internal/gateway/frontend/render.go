package frontend

// M4 的 VNode → HTML 渲染器（设计 7.3）：
//
//	插件 TSX → host.Renderer 产出 VNode JSON → 本文件校验并渲染成 HTML。
//
// 安全不变量：
//   - 标签/组件/属性白名单与 spec/vnode.schema.json 一致（校验复用 vnode.go 的
//     ValidateNode，渲染时对每个属性再做一次防御性复查）；
//   - 所有文本与属性值由本包转义，插件永远拿不到 template.HTML —— 本函数是
//     唯一把校验通过的 VNode 提升为 template.HTML 的出口；
//   - 禁止 on* / style / dangerouslySetInnerHTML / javascript: URL（vnode.go 拒绝）；
//   - 深度/节点数/单字符串/总输出上限沿用 vnode.go 的常量；
//   - 宿主组件只实现白名单内的通用组件：Icon 与 TodoList（M4 示例所需，DOM 与
//     class 照 a_da crates/agent-web/ui）；其余组件 fail-closed 报错，绝不猜测 DOM。
//
// 校验不通过或渲染失败：调用方（插槽桥）把该插槽留空并记录错误，不影响页面其余部分。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"sort"
	"strconv"
	"strings"
)

// voidElements 是 HTML 的空元素：只有开始标签，不收子节点。
var voidElements = map[string]bool{"br": true, "hr": true, "input": true}

// booleanAttrs 是以「存在与否」表达状态的属性（HTML 布尔属性语义）。
var booleanAttrs = map[string]bool{
	"required": true, "disabled": true, "readonly": true, "checked": true, "open": true,
}

// iconPaths 是 a_da 线性图标集的子集（来源 crates/agent-web/src/ui/icons.rs，
// 24×24 视窗、1.75 描边、圆角线帽；stroke 1.75 由外层 svg 属性统一给出）。
// 图标按需增加：不在表里的名字按 a_da 语义输出为空。
var iconPaths = map[string]string{
	"i-list":         `<path d="m3 7 2 2 4-4"/><path d="m3 17 2 2 4-4"/><path d="M13 6h8M13 12h8M13 18h8"/>`,
	"i-copy":         `<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`,
	"i-chevron-up":   `<path d="m6 15 6-6 6 6"/>`,
	"i-check-circle": `<circle cx="12" cy="12" r="9"/><path d="m8.5 12.5 2.5 2.5 4.5-5"/>`,
	"i-arrow-right":  `<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>`,
	"i-circle":       `<circle cx="12" cy="12" r="9"/>`,
}

// RenderHTML 把一段 VNode JSON（spec/vnode.schema.json）渲染成 template.HTML。
//
// 返回值语义：
//   - vnode 为 null / 空：返回空 HTML（插槽留空，桥接层回退到宿主占位）；
//   - 顶层是数组：报错（模板必须返回单个节点或 null）；
//   - 校验或渲染失败：返回错误，绝不返回未转义的部分输出。
func RenderHTML(vnode json.RawMessage) (template.HTML, error) {
	trimmed := bytes.TrimSpace(vnode)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", nil
	}
	var root Node
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		// 顶层数组/标量等非节点形态：显式报错，不猜。
		return "", fmt.Errorf("frontend: VNode 不是单个节点（模板必须返回单个节点或 null）: %w", err)
	}
	if err := ValidateNode(root); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := renderNode(&b, root, 1); err != nil {
		return "", err
	}
	out := []byte(b.String())
	if err := CheckOutputSize(out); err != nil {
		return "", err
	}
	return template.HTML(out), nil
}

// renderNode 递归渲染一个已校验的节点。
func renderNode(b *strings.Builder, n Node, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("frontend: VNode 深度超过 %d", MaxDepth)
	}
	switch {
	case n.IsText || n.T == "#text":
		b.WriteString(html.EscapeString(n.V))
		return nil
	case n.T == ComponentTag:
		return renderComponent(b, n)
	case n.T == "":
		return errors.New("frontend: 节点缺少 t")
	}

	b.WriteString("<")
	b.WriteString(n.T)
	if err := renderAttrs(b, n); err != nil {
		return err
	}
	if voidElements[n.T] {
		if len(n.C) > 0 {
			return fmt.Errorf("frontend: 空元素 %s 不接受子节点", n.T)
		}
		b.WriteString(">")
		return nil
	}
	b.WriteString(">")
	for _, c := range n.C {
		if err := renderNode(b, c, depth+1); err != nil {
			return err
		}
	}
	b.WriteString("</")
	b.WriteString(n.T)
	b.WriteString(">")
	return nil
}

// renderAttrs 输出元素属性（键序稳定，保证 golden 可 diff）。
func renderAttrs(b *strings.Builder, n Node) error {
	names := make([]string, 0, len(n.P))
	for k := range n.P {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		v := n.P[name]
		// 防御性复查：ValidateNode 已拒绝 on*/style/javascript:，这里兜底再查一次。
		if n.T == ComponentTag {
			if err := validateComponentProp(name, v); err != nil {
				return err
			}
		} else if err := validateAttribute(n.T, name, v); err != nil {
			return err
		}
		switch value := v.(type) {
		case nil:
			continue
		case bool:
			if booleanAttrs[name] {
				// HTML 布尔属性：true 输出存在形，false 直接省略。
				if value {
					b.WriteString(" " + name)
				}
				continue
			}
			// data-*/aria-* 的布尔按字符串输出（存在形对它们没有语义）。
			b.WriteString(" " + name + `="` + strconv.FormatBool(value) + `"`)
		case string:
			b.WriteString(" " + name + `="` + html.EscapeString(value) + `"`)
		case json.Number:
			b.WriteString(" " + name + `="` + html.EscapeString(value.String()) + `"`)
		default:
			// 嵌套对象/数组不是合法属性值：fail-closed（Schema 也只允许标量）。
			return fmt.Errorf("frontend: 属性 %q 的值类型 %T 不是标量（标签 %q）", name, v, n.T)
		}
	}
	return nil
}

// validateComponentProp 是组件 props 的安全底线（Schema $defs.componentProps）：
// 属性名禁 on* / style / dangerouslySetInnerHTML；值必须是标量。组件自己的 props
// 语义由渲染函数解释（如 Icon 的 name/size），这里只守安全。
func validateComponentProp(name string, value any) error {
	if err := validateComponentPropName(name); err != nil {
		return err
	}
	switch value.(type) {
	case nil, string, bool, json.Number, float64, int:
		return nil
	default:
		return fmt.Errorf("frontend: 组件属性 %q 的值类型 %T 不是标量", name, value)
	}
}

// renderComponent 渲染宿主组件节点（{"t":"$C","n":…}）。
// DOM 与 class 照 a_da 的组件库（7.7.3 组件表），不发明第二套结构。
func renderComponent(b *strings.Builder, n Node) error {
	canonical, ok := ResolveComponent(n.N)
	if !ok {
		return fmt.Errorf("frontend: 组件 %q 不在白名单里", n.N)
	}
	if len(n.C) > 0 {
		return fmt.Errorf("frontend: 宿主组件 %q 不携带子节点（内容一律走 props）", canonical)
	}
	switch canonical {
	case "Icon":
		return renderIcon(b, n.P)
	case "TodoList":
		return renderTodoList(b, n.P)
	default:
		// 组件清单里的其余组件属于基座页面（7.7.3），插件侧 M4 还没有用例；
		// 没有对照 DOM 就不允许输出，避免发明第二套结构。
		return fmt.Errorf("frontend: 组件 %q 的插件侧渲染尚未实现（M4 只开放 Icon/TodoList）", canonical)
	}
}

// renderIcon 输出 a_da 的线性 SVG 图标（stroke 1.75、currentColor，7.7.3）。
// 未知图标名输出为空（与 a_da icons.rs 的 icon() 语义一致）。
func renderIcon(b *strings.Builder, props map[string]any) error {
	name, _ := props["name"].(string)
	body, ok := iconPaths[name]
	if !ok || name == "" {
		return nil
	}
	size := 14
	if v, ok := numberProp(props["size"]); ok {
		size = v
	}
	if size < 8 {
		size = 8
	}
	if size > 32 {
		size = 32
	}
	fmt.Fprintf(b, `<svg class="ada-icon" width="%d" height="%d" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">%s</svg>`,
		size, size, body)
	return nil
}

// renderTodoList 输出 a_da 的任务步骤清单（crates/agent-web/src/ui/todo.rs 的
// TodoList Render 实现，同一套 .ada-todo__* class 与状态修饰符）。
func renderTodoList(b *strings.Builder, props map[string]any) error {
	steps, ok := props["steps"].([]any)
	if !ok || len(steps) == 0 {
		// a_da：空清单渲染为空串，不画空面板。
		return nil
	}
	notes, _ := props["notes"].(string)
	var out strings.Builder
	if strings.TrimSpace(notes) != "" {
		out.WriteString(`<p class="ada-todo__notes"><span>备注：</span>`)
		out.WriteString(html.EscapeString(notes))
		out.WriteString(`</p>`)
	}
	out.WriteString(`<ol class="ada-todo__list">`)
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("frontend: TodoList steps[%d] 不是对象", i)
		}
		title := todoStepTitle(step)
		if strings.TrimSpace(title) == "" {
			return fmt.Errorf("frontend: TodoList steps[%d] 缺少标题", i)
		}
		status := todoStatus(step["status"])
		icon := map[string]string{
			"done":        "i-check-circle",
			"in_progress": "i-arrow-right",
			"pending":     "i-circle",
		}[status]
		class := map[string]string{
			"done":        "is-done",
			"in_progress": "is-running",
			"pending":     "is-pending",
		}[status]
		var glyph strings.Builder
		if err := renderIcon(&glyph, map[string]any{"name": icon, "size": json.Number("12")}); err != nil {
			return err
		}
		fmt.Fprintf(&out, `<li class="ada-todo__step %s">%s<span>%s</span></li>`,
			class, glyph.String(), html.EscapeString(title))
	}
	out.WriteString(`</ol>`)
	b.WriteString(`<div class="ada-todo__list-wrap">`)
	b.WriteString(out.String())
	b.WriteString(`</div>`)
	return nil
}

// todoStepTitle 兼容三种字段名（工具侧的 step、附录 A 的 text、a_da 的 title），
// 不替任何具体插件做假设。
func todoStepTitle(step map[string]any) string {
	for _, key := range []string{"step", "text", "title"} {
		if s, ok := step[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// todoStatus 归一化步骤状态；未知值按待办处理（失败方向：不假称完成）。
func todoStatus(v any) string {
	s, _ := v.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "done", "completed":
		return "done"
	case "in_progress", "in-progress", "active", "running":
		return "in_progress"
	default:
		return "pending"
	}
}

// numberProp 把标量属性读成 int（size 之类）。
func numberProp(v any) (int, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := strconv.Atoi(x.String())
		return n, err == nil
	case float64:
		return int(x), true
	case int:
		return x, true
	}
	return 0, false
}
