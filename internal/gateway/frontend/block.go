package frontend

// Markdown 动态块的 VNode profile：比插件页面/插槽更窄。
//
// 动态块的内容由 Markdown（可能来自模型）驱动，不允许伪造审批托盘、表单或
// 宿主控制组件；交互仍走插件页面/动作桥。

import "fmt"

// BlockTags 是动态块允许的 HTML 标签（display-only）。
var BlockTags = []string{
	"div", "span", "p", "br", "hr",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"ul", "ol", "li", "strong", "em", "code", "pre", "small", "blockquote",
	"a", "section", "table", "thead", "tbody", "tr", "th", "td", "caption",
	"details", "summary",
}

// BlockComponents 是动态块允许的宿主组件（与当前 Go 渲染器已实现的面一致）。
var BlockComponents = []string{"Icon", "TodoList"}

var blockTagSet = toSet(BlockTags)
var blockComponentSet = toSet(BlockComponents)

// ValidateBlockNode 先走完整 VNode 校验，再应用 display-only profile。
func ValidateBlockNode(root Node) error {
	if err := ValidateNode(root); err != nil {
		return err
	}
	return validateBlockProfile(root, 1)
}

func validateBlockProfile(n Node, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("frontend: block VNode 深度超过 %d", MaxDepth)
	}
	if n.IsText || n.T == "#text" {
		return nil
	}
	if n.T == ComponentTag {
		canonical, ok := ResolveComponent(n.N)
		if !ok {
			return fmt.Errorf("frontend: block 组件 %q 不在白名单里", n.N)
		}
		if !blockComponentSet[canonical] {
			return fmt.Errorf("frontend: block 不允许宿主控制组件 %q", canonical)
		}
	} else if !blockTagSet[n.T] {
		return fmt.Errorf("frontend: block 不允许标签 %q", n.T)
	}
	for _, c := range n.C {
		if err := validateBlockProfile(c, depth+1); err != nil {
			return err
		}
	}
	return nil
}
