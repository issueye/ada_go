package web

// Markdown → VNode 数据（浏览器 ui-markdown 的输入）。
//
// 不把 HTML 字符串交给前端：先用 goldmark + bluemonday 得到清洗后的 HTML，
// 再解析成 VNode JSON。这样：
//   - 仍然只有一套 Markdown/清洗实现（服务端 goldmark + bluemonday）；
//   - 浏览器拿到的只是结构化数据，渲染由 ui-markdown 组件负责；
//   - 不会把原始 HTML/脚本交给前端，也不需要在 JS 里再写 Markdown 解析器。
//
// VNode 字段与 ada-render 一致：元素 {t,p,c}、文本 {t:"#text",v}。

import (
	"encoding/json"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type markdownVNode struct {
	T string          `json:"t"`
	V string          `json:"v,omitempty"`
	P map[string]any  `json:"p,omitempty"`
	C []markdownVNode `json:"c,omitempty"`
}

var markdownAllowedAttrs = map[string]bool{
	"class": true, "href": true, "title": true, "align": true, "start": true,
	"type": true, "checked": true, "disabled": true, "selected": true,
	"src": true, "alt": true, "colspan": true, "rowspan": true,
}

var markdownBoolAttrs = map[string]bool{
	"checked": true, "disabled": true, "selected": true, "open": true, "required": true,
}

func markdownVNodes(text string) []markdownVNode {
	rendered := string(renderMarkdown(text))
	if strings.TrimSpace(rendered) == "" {
		return nil
	}
	context := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(rendered), context)
	if err != nil {
		return nil
	}
	out := make([]markdownVNode, 0, len(nodes))
	for _, node := range nodes {
		if v, ok := htmlToMarkdownVNode(node); ok {
			out = append(out, v)
		}
	}
	return out
}

func htmlToMarkdownVNode(node *html.Node) (markdownVNode, bool) {
	switch node.Type {
	case html.TextNode:
		if node.Data == "" {
			return markdownVNode{}, false
		}
		return markdownVNode{T: "#text", V: node.Data}, true
	case html.ElementNode:
		tag := strings.ToLower(node.Data)
		out := markdownVNode{T: tag}
		for _, attr := range node.Attr {
			key := strings.ToLower(attr.Key)
			if !markdownAllowedAttrs[key] {
				continue
			}
			if (key == "href" || key == "src") && !safeMarkdownURL(attr.Val) {
				continue
			}
			if out.P == nil {
				out.P = make(map[string]any, len(node.Attr))
			}
			if markdownBoolAttrs[key] {
				out.P[key] = true
				continue
			}
			out.P[key] = attr.Val
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if v, ok := htmlToMarkdownVNode(child); ok {
				out.C = append(out.C, v)
			}
		}
		return out, true
	default:
		return markdownVNode{}, false
	}
}

func safeMarkdownURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	for _, bad := range []string{"javascript:", "vbscript:", "data:text/html"} {
		if strings.HasPrefix(lower, bad) {
			return false
		}
	}
	return true
}

// markdownNodesJSON 是给模板/接口用的紧凑 JSON。
func markdownNodesJSON(text string) json.RawMessage {
	nodes := markdownVNodes(text)
	if len(nodes) == 0 {
		return json.RawMessage("[]")
	}
	raw, err := json.Marshal(nodes)
	if err != nil {
		return json.RawMessage("[]")
	}
	return raw
}
