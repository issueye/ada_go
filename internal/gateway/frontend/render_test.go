package frontend

// VNode → HTML 渲染器的 golden 与安全测试（7.3）：
// 文本转义、嵌套结构、属性转义是三个必保 golden；另外覆盖组件渲染、
// 危险属性拒绝与空插槽语义。

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustRenderHTML(t *testing.T, vnode string) string {
	t.Helper()
	out, err := RenderHTML(json.RawMessage(vnode))
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	return string(out)
}

// golden 1：文本节点转义（插件永远拿不到 template.HTML）。
func TestGoldenTextEscaping(t *testing.T) {
	got := mustRenderHTML(t, `{"t":"p","c":[
		{"t":"#text","v":"答案 <b>加粗</b> & \"引号\" '单引号'"}]}`)
	want := `<p>答案 &lt;b&gt;加粗&lt;/b&gt; &amp; &#34;引号&#34; &#39;单引号&#39;</p>`
	if got != want {
		t.Fatalf("golden 文本转义不符：\n got %s\nwant %s", got, want)
	}
}

// golden 2：嵌套结构（details/summary/列表，附录 A 的任务卡骨架）。
func TestGoldenNestedStructure(t *testing.T) {
	got := mustRenderHTML(t, `{"t":"details","p":{"class":"tp-card","data-state":"collapsed"},"c":[
		{"t":"summary","p":{"class":"tp-card__summary"},"c":[
			{"t":"span","p":{"class":"tp-card__title"},"c":[{"t":"#text","v":"任务规划"}]},
			{"t":"span","p":{"class":"tp-card__count"},"c":[{"t":"#text","v":"3/12"}]}]},
		{"t":"div","p":{"class":"tp-card__body"},"c":[
			{"t":"ol","p":{"class":"ada-todo__list"},"c":[
				{"t":"li","p":{"class":"ada-todo__step is-done"},"c":[{"t":"#text","v":"第一步"}]}]}]}]}`)
	want := `<details class="tp-card" data-state="collapsed">` +
		`<summary class="tp-card__summary">` +
		`<span class="tp-card__title">任务规划</span>` +
		`<span class="tp-card__count">3/12</span></summary>` +
		`<div class="tp-card__body">` +
		`<ol class="ada-todo__list"><li class="ada-todo__step is-done">第一步</li></ol></div></details>`
	if got != want {
		t.Fatalf("golden 嵌套不符：\n got %s\nwant %s", got, want)
	}
}

// golden 3：属性值转义（引号/尖括号不能逃出属性边界）。
func TestGoldenAttributeEscaping(t *testing.T) {
	got := mustRenderHTML(t, `{"t":"a","p":{
		"class":"link\" onmouseover=\"alert(1)",
		"href":"/thread/1?x=<script>",
		"data-note":"a&b"},"c":[{"t":"#text","v":"打开"}]}`)
	want := `<a class="link&#34; onmouseover=&#34;alert(1)" data-note="a&amp;b" href="/thread/1?x=&lt;script&gt;">打开</a>`
	if got != want {
		t.Fatalf("golden 属性转义不符：\n got %s\nwant %s", got, want)
	}
}

// 组件 golden：Icon（a_da 线性 SVG）与 TodoList（.ada-todo__* 结构）。
func TestGoldenComponents(t *testing.T) {
	icon := mustRenderHTML(t, `{"t":"$C","n":"Icon","p":{"name":"i-list","size":12},"c":[]}`)
	wantIcon := `<svg class="ada-icon" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` +
		`<path d="m3 7 2 2 4-4"/><path d="m3 17 2 2 4-4"/><path d="M13 6h8M13 12h8M13 18h8"/></svg>`
	if icon != wantIcon {
		t.Fatalf("Icon golden 不符：\n got %s\nwant %s", icon, wantIcon)
	}

	todo := mustRenderHTML(t, `{"t":"$C","n":"TodoList","p":{
		"steps":[{"step":"写测试","status":"done"},{"step":"跑测试","status":"in_progress"},{"step":"发布","status":"pending"}],
		"notes":"全部跑完再发"},"c":[]}`)
	for _, want := range []string{
		`<div class="ada-todo__list-wrap"><p class="ada-todo__notes"><span>备注：</span>全部跑完再发</p><ol class="ada-todo__list">`,
		`<li class="ada-todo__step is-done">`,
		`<li class="ada-todo__step is-running">`,
		`<li class="ada-todo__step is-pending">`,
		`<span>写测试</span>`, `<span>跑测试</span>`, `<span>发布</span>`,
	} {
		if !strings.Contains(todo, want) {
			t.Fatalf("TodoList golden 缺少 %s\n got %s", want, todo)
		}
	}

	// TSX 名 Todo 解析到规范名 TodoList（7.7.3 别名）。
	if _, err := RenderHTML(json.RawMessage(`{"t":"$C","n":"Todo","p":{"steps":[]},"c":[]}`)); err != nil {
		t.Fatalf("Todo 别名应可用: %v", err)
	}
}

// 空插槽语义：null 与空串渲染为空，不报错。
func TestRenderHTMLNull(t *testing.T) {
	for _, raw := range []string{"null", "", "  "} {
		out, err := RenderHTML(json.RawMessage(raw))
		if err != nil || out != "" {
			t.Fatalf("RenderHTML(%q) = (%q, %v), want 空", raw, out, err)
		}
	}
}

// 顶层数组不是单个节点：显式报错。
func TestRenderHTMLRejectsArrayRoot(t *testing.T) {
	if _, err := RenderHTML(json.RawMessage(`[{"t":"#text","v":"a"}]`)); err == nil {
		t.Fatal("顶层数组应报错（模板必须返回单个节点或 null）")
	}
}

// 危险输出拒绝：on*/style/dangerouslySetInnerHTML、javascript:、未知标签与组件。
func TestRenderHTMLRejectsDangerous(t *testing.T) {
	cases := []string{
		`{"t":"div","p":{"onClick":"x"},"c":[]}`,
		`{"t":"div","p":{"style":"color:red"},"c":[]}`,
		`{"t":"a","p":{"href":"javascript:alert(1)"},"c":[]}`,
		`{"t":"script","c":[]}`,
		`{"t":"$C","n":"NotInList","p":{},"c":[]}`,
		`{"t":"$C","n":"Button","p":{"label":"x"},"c":[{"t":"#text","v":"子节点"}]}`,
		`{"t":"$C","n":"Chip","p":{},"c":[]}`,
	}
	for _, raw := range cases {
		if _, err := RenderHTML(json.RawMessage(raw)); err == nil {
			t.Fatalf("危险 VNode %s 必须被拒绝", raw)
		}
	}
}

// 空元素不接受子节点；布尔属性按存在形输出。
func TestRenderHTMLVoidAndBooleans(t *testing.T) {
	got := mustRenderHTML(t, `{"t":"input","p":{"type":"text","name":"value","required":true,"disabled":false,"data-on":true}}`)
	// 属性键按字典序输出（确定性，golden 可 diff）。
	want := `<input data-on="true" name="value" required type="text">`
	if got != want {
		t.Fatalf("空元素/布尔属性不符：\n got %s\nwant %s", got, want)
	}
	if _, err := RenderHTML(json.RawMessage(`{"t":"br","c":[{"t":"#text","v":"x"}]}`)); err == nil {
		t.Fatal("br 不接受子节点")
	}
}

// 输出上限：超过 256KB 报错。
func TestRenderHTMLOutputLimit(t *testing.T) {
	big := strings.Repeat("字", MaxOutputBytes/3+100)
	var b strings.Builder
	b.WriteString(`{"t":"p","c":[{"t":"#text","v":"`)
	b.WriteString(big)
	b.WriteString(`"}]}`)
	// 单字符串 32KB 上限先拦截；拆成多个短文本绕开它，验证总输出上限。
	if _, err := RenderHTML(json.RawMessage(b.String())); err == nil {
		t.Fatal("超大文本应被拒绝")
	}
	var parts []string
	for i := 0; i < 80; i++ {
		parts = append(parts, `{"t":"#text","v":"`+strings.Repeat("a", 4000)+`"}`)
	}
	huge := `{"t":"p","c":[` + strings.Join(parts, ",") + `]}`
	if _, err := RenderHTML(json.RawMessage(huge)); err == nil || !strings.Contains(err.Error(), "总输出上限") {
		t.Fatalf("总输出超限应报错: %v", err)
	}
}
