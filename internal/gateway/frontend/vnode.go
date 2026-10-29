// Package frontend 是 Gateway 侧的前端插件宿主：TSX 编译、Render Runtime、VNode 校验与插槽注册表。
//
// M0 阶段这里落地的是契约与校验逻辑本身（设计 7.3）：插件的前端输出只能是经过校验的
// VNode，不能注入原始 HTML、内联脚本或内联样式。校验器现在就可用，M4 只是把它接到
// esbuild + moejs 渲染链前面。
package frontend

import (
	"fmt"
	"strings"
)

// 渲染限制（设计 7.3、6.4、9.5）。数值与 spec/vnode.schema.json 的 x-limit 必须一致，
// adag verify vnode 会逐条比对。
const (
	MaxDepth       = 64
	MaxNodes       = 2000
	MaxStringBytes = 32 << 10
	MaxOutputBytes = 256 << 10
	SlotTimeoutMS  = 100
	PageTimeoutMS  = 300
	RuntimeHeapMB  = 64
)

// 组件节点的类型标记：{"t":"$C","n":"Button",...}（7.3 示例）。
const ComponentTag = "$C"

// AllowedTags 是插件可用的 HTML 标签白名单，与 spec/vnode.schema.json 的 $defs.element.t.enum 逐字一致。
//
// 刻意排除：script/iframe/object/embed/link/style/svg —— 图标走 Icon 组件（7.1、7.7.3），
// 插件不产出裸 svg；图片在 v1 不给插件任意 src 面，走 Markdown 组件与基座的图片投影链路。
var AllowedTags = []string{
	"div", "span", "p", "br", "hr",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"ul", "ol", "li", "strong", "em", "code", "pre", "small", "blockquote",
	"a", "main", "nav", "section",
	"form", "details", "summary", "label", "input", "button", "select", "option", "optgroup", "textarea",
	"table", "thead", "tbody", "tr", "th", "td", "caption",
}

// AllowedComponents 是宿主组件名白名单（7.3 与 7.7.3 的组件清单，全部是通用组件）。
//
// TSX 侧 Todo 对应 Go 侧 TodoList（7.7.3），两者都列出来，映射表在 componentAliases。
var AllowedComponents = []string{
	"Button", "IconButton", "SendButton", "Chip", "TextField", "Select", "Tabs",
	"Modal", "ConfirmDialog", "Disclosure", "Panel", "UserMessage", "AssistantMessage",
	"Notice", "EmptyState", "Composer", "ComposerAux", "ModelPicker", "ModelTable",
	"QueueTray", "Form", "ToolCall", "TodoList", "Icon", "Slot", "InteractionTray", "Markdown",
}

// componentAliases 把 TSX 名映射到 Go 组件名。
var componentAliases = map[string]string{
	"Todo": "TodoList",
}

// AllowedAttributes 是元素属性名白名单，与 spec/vnode.schema.json 的 $defs.elementProps.properties 逐字一致。
// aria-* 与 data-* 由 patternProperties 规则放行（见 isAllowedAttribute）。
var AllowedAttributes = []string{
	"class", "id", "href", "title", "for", "name", "value", "type", "placeholder",
	"required", "disabled", "readonly", "checked", "open", "min", "max", "step",
	"minlength", "maxlength", "cols", "rows", "action", "method", "start", "scope",
	"colspan", "rowspan", "role",
}

// AllowedInputTypes 是 input[type] 的取值集合，与 Schema 的 enum 一致。
// 不接受 file：v1 只有插件 zip 安装（CLI），HTTP 不接受任意文件上传（09 §9.3）。
var AllowedInputTypes = []string{
	"button", "submit", "reset", "text", "search", "email", "password",
	"number", "checkbox", "radio", "hidden",
}

var (
	tags        = toSet(AllowedTags)
	components  = toSet(AllowedComponents)
	attributes  = toSet(AllowedAttributes)
	dangerousUR = []string{"javascript:", "vbscript:", "data:text/html"}
)

// toSet 构造查找集合。
func toSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}

// Node 是解码后的 VNode 节点。文本节点形态是 {"t":"#text","v":"..."}。
type Node struct {
	T      string         `json:"t"`
	N      string         `json:"n,omitempty"`
	V      string         `json:"v,omitempty"`
	P      map[string]any `json:"p,omitempty"`
	C      []Node         `json:"c,omitempty"`
	IsText bool           `json:"-"`
}

// TextNode 构造文本节点；文本由 Go 侧转义，插件永远拿不到 template.HTML（7.3）。
func TextNode(s string) Node { return Node{T: "#text", V: s, IsText: true} }

// ResolveComponent 把 TSX 组件名解析为 Go 组件名。
func ResolveComponent(name string) (string, bool) {
	if alias, ok := componentAliases[name]; ok {
		name = alias
	}
	_, ok := components[name]
	return name, ok
}

// ValidateNode 递归校验一棵 VNode：标签/组件白名单、属性白名单、禁用 on*/style、
// 危险 URL、深度/节点数/字符串长度上限。
func ValidateNode(root Node) error {
	count := 0
	return validate(root, 1, &count)
}

func validate(n Node, depth int, count *int) error {
	if depth > MaxDepth {
		return fmt.Errorf("frontend: VNode 深度超过 %d（7.3）", MaxDepth)
	}
	*count++
	if *count > MaxNodes {
		return fmt.Errorf("frontend: VNode 节点数超过 %d（7.3）", MaxNodes)
	}
	if n.IsText || n.T == "#text" {
		return checkString("#text", n.V)
	}
	if n.T == "" {
		return fmt.Errorf("frontend: 节点缺少 t")
	}
	if n.T == ComponentTag {
		if _, ok := ResolveComponent(n.N); !ok {
			return fmt.Errorf("frontend: 组件名 %q 不在宿主组件白名单里", n.N)
		}
		// 组件 props 走 $defs.componentProps 的安全底线（on*/style/dangerouslySetInnerHTML）；
		// 各组件的合法键由渲染期逐个解释，Schema 不在这里枚举属性名。
		for k, v := range n.P {
			if err := validateComponentPropName(k); err != nil {
				return err
			}
			if s, ok := v.(string); ok {
				if err := checkString(k, s); err != nil {
					return err
				}
			}
		}
	} else if !tags[n.T] {
		return fmt.Errorf("frontend: 标签 %q 不在白名单里", n.T)
	} else {
		for k, v := range n.P {
			if err := validateAttribute(n.T, k, v); err != nil {
				return err
			}
		}
	}
	for _, c := range n.C {
		if err := validate(c, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func validateAttribute(tag, name string, value any) error {
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "on"):
		return fmt.Errorf("frontend: 禁止事件属性 %q（7.3：onClick 等直接拒绝）", name)
	case lower == "style":
		return fmt.Errorf("frontend: 禁止内联 style（一律走类名，7.3）")
	case lower == "dangerouslysetinnerhtml":
		return fmt.Errorf("frontend: 禁止 dangerouslySetInnerHTML（1.5.6）")
	case !attributes[name] && !isPatternAttribute(lower):
		return fmt.Errorf("frontend: 属性 %q 不在白名单里（标签 %q）", name, tag)
	}
	s, ok := value.(string)
	if !ok {
		return nil // 布尔存在型属性与数字标量：由 Go 侧转义后写入
	}
	if err := checkString(name, s); err != nil {
		return err
	}
	if name == "type" && tag == "input" && !containsStr(AllowedInputTypes, s) {
		return fmt.Errorf("frontend: input[type]=%q 不在允许取值里（Schema enum）", s)
	}
	if name == "method" && s != "get" && s != "post" {
		return fmt.Errorf("frontend: form method=%q 只允许 get/post", s)
	}
	if isURLAttribute(lower) {
		return validateURL(name, s)
	}
	return nil
}

// validateComponentPropName 是组件 props 的属性名安全底线（$defs.componentProps 的
// propertyNames 约束）：禁止 on* / style / dangerouslySetInnerHTML。
func validateComponentPropName(name string) error {
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "on"):
		return fmt.Errorf("frontend: 组件属性 %q 是事件处理（7.3：没有事件处理函数）", name)
	case lower == "style":
		return fmt.Errorf("frontend: 禁止内联 style（一律走类名，7.3）")
	case strings.Contains(lower, "dangerouslysetinnerhtml"):
		return fmt.Errorf("frontend: 禁止 dangerouslySetInnerHTML（1.5.6）")
	}
	return nil
}

// isPatternAttribute 对应 Schema 的 patternProperties：^aria-[a-z0-9-]{1,40}$ 与
// ^data-[a-z0-9][a-z0-9-]{0,63}$（保守实现：前缀 + 字符集检查）。
func isPatternAttribute(lower string) bool {
	if rest, ok := strings.CutPrefix(lower, "aria-"); ok {
		return isDashName(rest, 40)
	}
	if rest, ok := strings.CutPrefix(lower, "data-"); ok {
		return isDashName(rest, 64)
	}
	return false
}

func isDashName(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func isURLAttribute(name string) bool {
	switch name {
	case "href", "src", "formaction", "xlink:href", "poster", "action":
		return true
	}
	return false
}

// validateURL 只允许相对路径、锚点与 http/https/mailto（09 §9.3），并拒绝危险协议。
func validateURL(name, value string) error {
	v := strings.TrimSpace(strings.ToLower(value))
	for _, bad := range dangerousUR {
		if strings.HasPrefix(v, bad) {
			return fmt.Errorf("frontend: 属性 %q 使用了禁止的协议（%q）", name, bad)
		}
	}
	if strings.HasPrefix(v, "http:") || strings.HasPrefix(v, "https:") || strings.HasPrefix(v, "mailto:") {
		return nil
	}
	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") {
		return nil
	}
	if strings.Contains(v, "://") {
		return fmt.Errorf("frontend: 属性 %q 的协议 %q 不在允许范围", name, strings.SplitN(v, ":", 2)[0])
	}
	return nil // 相对路径（无 scheme）
}

func checkString(where, s string) error {
	if len(s) > MaxStringBytes {
		return fmt.Errorf("frontend: %s 的字符串 %d 字节，超过单字符串上限 %d（7.3）", where, len(s), MaxStringBytes)
	}
	return nil
}

// CheckOutputSize 校验渲染结果的总输出上限（256KB）。
func CheckOutputSize(html []byte) error {
	if len(html) > MaxOutputBytes {
		return fmt.Errorf("frontend: 渲染输出 %d 字节，超过总输出上限 %d（7.3）", len(html), MaxOutputBytes)
	}
	return nil
}

// WhitelistReport 供 adag verify 与文档生成：三类白名单加上限值的当前值。
type WhitelistReport struct {
	Tags       []string
	Components []string
	Attributes []string
	InputTypes []string
	Limits     map[string]int
}

// Report 返回白名单快照（排序后，便于 diff）。
func Report() WhitelistReport {
	r := WhitelistReport{
		Tags:       append([]string(nil), AllowedTags...),
		Components: append([]string(nil), AllowedComponents...),
		Attributes: append([]string(nil), AllowedAttributes...),
		InputTypes: append([]string(nil), AllowedInputTypes...),
		Limits: map[string]int{
			"maxDepth":       MaxDepth,
			"maxNodes":       MaxNodes,
			"maxStringBytes": MaxStringBytes,
			"maxOutputBytes": MaxOutputBytes,
			"slotTimeoutMs":  SlotTimeoutMS,
			"pageTimeoutMs":  PageTimeoutMS,
		},
	}
	for _, set := range [][]string{r.Tags, r.Components, r.Attributes} {
		sortStrings(set)
	}
	return r
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}
