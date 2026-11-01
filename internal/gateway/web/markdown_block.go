package web

// Markdown 动态块（adag-ui fence）的解析与降级。
//
// 只在 Markdown 顶层识别 ```adag-ui fence；其余文本仍走原有
// goldmark + bluemonday + markdownVNodes 管线。块本身由前端插件桥渲染成
// VNode（经 VNode block profile 校验），Markdown 只负责把结果放回原位置。

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"regexp"
	"strings"
)

const (
	maxMarkdownBlocks       = 8
	maxMarkdownBlockBytes   = 64 << 10
	defaultBlockFailureText = "动态内容暂不可用"
)

// BlockRenderer 由前端插件桥可选实现：把 adag-ui block 渲染成 VNode/HTML。
type BlockRenderer interface {
	RenderBlock(r *http.Request, rendererID, blockID string, data json.RawMessage) (json.RawMessage, template.HTML, error)
}

var (
	blockFenceRe = regexp.MustCompile("(?ms)^[ \t]*```adag-ui[ \t]*\r?\n(.*?)\r?\n[ \t]*```[ \t]*(?:\r?\n|$)")
	rendererIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}/[a-z][a-z0-9-]{1,63}$`)
)

type markdownBlockEnvelope struct {
	Renderer string          `json:"renderer"`
	Version  int             `json:"version"`
	ID       string          `json:"id"`
	Data     json.RawMessage `json:"data"`
	Fallback string          `json:"fallback"`
}

func (s *Server) blockRenderer() BlockRenderer {
	if s == nil || s.cfg.PluginUI == nil {
		return nil
	}
	br, _ := s.cfg.PluginUI.(BlockRenderer)
	return br
}

// renderMarkdownWithBlocks 是 renderMarkdown 的动态块版本：HTML 端点使用。
func (s *Server) renderMarkdownWithBlocks(r *http.Request, text string) template.HTML {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	br := s.blockRenderer()
	if br == nil {
		return renderMarkdown(text)
	}
	var out strings.Builder
	last := 0
	count := 0
	for _, m := range blockFenceRe.FindAllStringSubmatchIndex(text, -1) {
		out.WriteString(string(renderMarkdown(text[last:m[0]])))
		last = m[1]
		payload := text[m[2]:m[3]]
		blockHTML, _ := s.renderMarkdownBlock(r, br, payload, &count)
		out.WriteString(string(blockHTML))
	}
	out.WriteString(string(renderMarkdown(text[last:])))
	return template.HTML(out.String())
}

// markdownNodesJSONWithBlocks 是 markdownNodesJSON 的动态块版本：AST 端点使用。
func (s *Server) markdownNodesJSONWithBlocks(r *http.Request, text string) json.RawMessage {
	br := s.blockRenderer()
	if br == nil {
		return markdownNodesJSON(text)
	}
	nodes := make([]markdownVNode, 0, 32)
	last := 0
	count := 0
	for _, m := range blockFenceRe.FindAllStringSubmatchIndex(text, -1) {
		nodes = append(nodes, markdownVNodes(text[last:m[0]])...)
		last = m[1]
		payload := text[m[2]:m[3]]
		raw, _, _ := s.renderMarkdownBlockNodes(r, br, payload, &count)
		if len(bytes.TrimSpace(raw)) > 0 {
			var node markdownVNode
			if err := json.Unmarshal(raw, &node); err == nil && node.T != "" {
				nodes = append(nodes, node)
				continue
			}
		}
		nodes = append(nodes, blockFallbackVNode(defaultBlockFailureText))
	}
	nodes = append(nodes, markdownVNodes(text[last:])...)
	raw, err := json.Marshal(nodes)
	if err != nil {
		return json.RawMessage("[]")
	}
	return raw
}

// renderMarkdownBlock 返回 HTML 降级/结果；解析失败时退回代码块。
func (s *Server) renderMarkdownBlock(r *http.Request, br BlockRenderer, payload string, count *int) (template.HTML, bool) {
	_, html, ok := s.renderMarkdownBlockNodes(r, br, payload, count)
	if html != "" {
		return html, ok
	}
	return blockFallbackHTML(defaultBlockFailureText), false
}

func (s *Server) renderMarkdownBlockNodes(r *http.Request, br BlockRenderer, payload string, count *int) (json.RawMessage, template.HTML, bool) {
	if count != nil {
		*count++
		if *count > maxMarkdownBlocks {
			return nil, blockFallbackHTML("动态块数量超过上限"), false
		}
	}
	if len(payload) > maxMarkdownBlockBytes {
		return nil, blockFallbackHTML("动态块数据超过上限"), false
	}
	var env markdownBlockEnvelope
	dec := json.NewDecoder(bytes.NewReader([]byte(payload)))
	if err := dec.Decode(&env); err != nil || strings.TrimSpace(env.Renderer) == "" || len(env.Data) == 0 {
		raw, _ := json.Marshal(blockSyntaxFallbackVNode(payload))
		return raw, blockSyntaxFallbackHTML(payload), false
	}
	if !rendererIDRe.MatchString(env.Renderer) {
		raw, _ := json.Marshal(blockSyntaxFallbackVNode(payload))
		return raw, blockSyntaxFallbackHTML(payload), false
	}
	raw, html, err := br.RenderBlock(r, env.Renderer, strings.TrimSpace(env.ID), env.Data)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		text := strings.TrimSpace(env.Fallback)
		if text == "" {
			text = defaultBlockFailureText
		}
		return nil, blockFallbackHTML(text), false
	}
	return raw, html, true
}

func blockSyntaxFallbackHTML(payload string) template.HTML {
	return template.HTML(`<pre><code class="language-adag-ui">` + esc(payload) + `</code></pre>`)
}

func blockSyntaxFallbackVNode(payload string) markdownVNode {
	return markdownVNode{
		T: "pre",
		C: []markdownVNode{{T: "code", P: map[string]any{"class": "language-adag-ui"}, C: []markdownVNode{{T: "#text", V: payload}}}},
	}
}

func blockFallbackHTML(text string) template.HTML {
	if strings.TrimSpace(text) == "" {
		text = defaultBlockFailureText
	}
	return template.HTML(`<div class="adag-ui-block adag-ui-block--error"><p class="adag-ui-block__error">` + esc(text) + `</p></div>`)
}

func blockFallbackVNode(text string) markdownVNode {
	if strings.TrimSpace(text) == "" {
		text = defaultBlockFailureText
	}
	return markdownVNode{
		T: "div",
		P: map[string]any{"class": "adag-ui-block adag-ui-block--error"},
		C: []markdownVNode{
			{T: "p", P: map[string]any{"class": "adag-ui-block__error"}, C: []markdownVNode{{T: "#text", V: text}}},
		},
	}
}
