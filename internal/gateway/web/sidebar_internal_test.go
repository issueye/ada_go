package web

import (
	"fmt"
	"strings"
	"testing"
)

// TestSidebarPluginNavScrollsAllItems：插件 nav 不再截断成「还有 N 个…」，
// 全部条目进入固定高度的滚动容器（CSS 提供 max-height/overflow）。
func TestSidebarPluginNavScrollsAllItems(t *testing.T) {
	var links []pluginNavLink
	for i := 0; i < 8; i++ {
		links = append(links, pluginNavLink{
			PluginID: fmt.Sprintf("p%d", i),
			Label:    fmt.Sprintf("插件 %d", i),
			Href:     fmt.Sprintf("/ext/p%d/settings", i),
		})
	}
	html := string(renderSidebar(sidebarData{BrandName: "ada-go", PluginNav: links, Active: "ext:p0"}))
	if !strings.Contains(html, `class="ada-nav__scroll"`) {
		t.Fatalf("插件导航应包在可滚动容器里:\n%s", html)
	}
	if strings.Contains(html, "还有") {
		t.Fatalf("不应再出现「还有 N 个…」:\n%s", html)
	}
	for _, link := range links {
		if !strings.Contains(html, link.Href) || !strings.Contains(html, link.Label) {
			t.Fatalf("缺少插件导航 %q（%s）", link.Label, link.Href)
		}
	}
	if !strings.Contains(html, `aria-current="page"`) {
		t.Fatal("激活的插件页应带 aria-current")
	}
}
