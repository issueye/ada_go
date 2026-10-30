package web

import (
	"strings"
	"testing"
)

func TestHighlightPluginSource(t *testing.T) {
	html, language := highlightPluginSource("export const answer = 42;\n", "index.ts")
	if language != "TypeScript" {
		t.Fatalf("language = %q, want TypeScript", language)
	}
	got := string(html)
	for _, want := range []string{`class="kr"`, `class="nx"`, `class="mi"`, "answer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("高亮结果缺少 %q: %s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Fatalf("高亮没有转义代码内容: %s", got)
	}
	if strings.Contains(got, "style=") {
		t.Fatalf("高亮不应输出内联 style（CSP style-src 'self'）: %s", got)
	}
	escaped, _ := highlightPluginSource("<script>alert(1)</script>", "unknown.zzz")
	if strings.Contains(string(escaped), "<script>") {
		t.Fatalf("未知语言没有转义: %s", escaped)
	}
}
