package web

import (
	"strings"
	"testing"
)

// TestRenderMarkdownExternalLinks：正文里的外部链接补 target=_blank/rel，
// 站内相对链接保持原样，危险协议被清洗掉。
func TestRenderMarkdownExternalLinks(t *testing.T) {
	got := string(renderMarkdown("[ext](https://example.com/a) [same](/overview) [mail](mailto:a@b.c) [bad](javascript:alert(1))"))

	ext := anchorTag(t, got, `href="https://example.com/a"`)
	if !strings.Contains(ext, `target="_blank"`) || !strings.Contains(ext, "noopener") || !strings.Contains(ext, "noreferrer") {
		t.Fatalf("外部链接缺少 target/rel: %s", ext)
	}
	mail := anchorTag(t, got, `href="mailto:a@b.c"`)
	if !strings.Contains(mail, `target="_blank"`) {
		t.Fatalf("mailto 也应外部打开: %s", mail)
	}
	same := anchorTag(t, got, `href="/overview"`)
	if strings.Contains(same, `target="_blank"`) {
		t.Fatalf("站内链接不应加 target=_blank: %s", same)
	}
	if strings.Contains(got, "javascript:") {
		t.Fatalf("危险协议应被清洗: %s", got)
	}
}

// anchorTag 从渲染结果里找包含 href 的 <a ...> 开标签。
func anchorTag(t *testing.T, rendered, href string) string {
	t.Helper()
	idx := strings.Index(rendered, href)
	if idx < 0 {
		t.Fatalf("找不到 %s:\n%s", href, rendered)
	}
	start := strings.LastIndex(rendered[:idx], "<a")
	end := strings.Index(rendered[idx:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("定位锚点失败（%s）:\n%s", href, rendered)
	}
	return rendered[start : idx+end+1]
}
