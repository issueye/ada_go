package web

import (
	"bytes"
	"html"
	"html/template"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// highlightPluginSource 用 Chroma 生成带 token class 的 HTML。
// 只输出 span/class，不写内联 style，避免撞上 CSP 的 style-src 'self'；
// 颜色由 app-local.css 里的 .chroma 规则从设计令牌取值。
func highlightPluginSource(source, filename string) (template.HTML, string) {
	if strings.TrimSpace(source) == "" {
		return "", ""
	}
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(source)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return template.HTML(html.EscapeString(source)), ""
	}
	var buf bytes.Buffer
	formatter := chromahtml.New(
		chromahtml.WithClasses(true),
		chromahtml.PreventSurroundingPre(true),
		chromahtml.TabWidth(2),
	)
	if err := formatter.Format(&buf, styles.Get("github"), iterator); err != nil {
		return template.HTML(html.EscapeString(source)), ""
	}
	language := ""
	if cfg := lexer.Config(); cfg != nil {
		language = cfg.Name
	}
	return template.HTML(buf.String()), language
}
