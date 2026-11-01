package gateway

// 前端门禁的启用判定：Core 名单里的插件按状态，名单外的 block-only 插件安装即启用。

import "testing"

func TestFrontendGateEnabledIn(t *testing.T) {
	entry := frontendEnabledEntry{
		set:   map[string]bool{"ready-plugin": true},
		known: map[string]bool{"ready-plugin": true, "disabled-plugin": true, "failed-plugin": true},
	}
	for _, tc := range []struct {
		name string
		id   string
		want bool
	}{
		{"Core 明确启用", "ready-plugin", true},
		{"Core 明确禁用", "disabled-plugin", false},
		{"Core 加载失败", "failed-plugin", false},
		// 纯前端 block-only 包不会被 Core 加载（没有 backend），plugin.list 里没有它；
		// 安装即启用，要停用就移除插件目录。
		{"block-only 未知插件", "pure-blocks", true},
	} {
		if got := entry.enabledIn(tc.id); got != tc.want {
			t.Fatalf("%s: enabledIn(%q) = %v，期望 %v", tc.name, tc.id, got, tc.want)
		}
	}

	// Core 不可用时按启用渲染，避免整页插件区消失。
	if !(frontendEnabledEntry{allowAll: true}).enabledIn("anything") {
		t.Fatal("allowAll 应放行全部插件")
	}
}
