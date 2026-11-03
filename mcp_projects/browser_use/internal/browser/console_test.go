package browser

import (
	"strings"
	"testing"

	"browseruse/internal/browser/runtime"
)

// TestRuntimePreloadCapturesConsole 钉「控制台采集必须在 document-start 预注入里」这条
// 约束：console 工具读的是页面运行时装好的缓冲，少接一处（invoke 分支、安装调用、
// iframe 转发名单）都会让工具静默返回空列表，而 Go 侧测试覆盖不到 JS。
func TestRuntimePreloadCapturesConsole(t *testing.T) {
	src := runtime.Source()
	for _, want := range []string{
		`case "console"`,
		"installConsole();",
		"__buConsoleInstalled",
		`"network", "console", "upload"`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("页面运行时缺少 %q：console 采集依赖预注入", want)
		}
	}
}

// TestConsoleValidation 钉 level/source 的取值集合：非法值必须拒绝，而不是静默回空列表。
func TestConsoleValidation(t *testing.T) {
	for _, ok := range []string{"", "log", "info", "warn", "error", "debug"} {
		if !validConsoleLevel(ok) {
			t.Fatalf("level %q 应被接受", ok)
		}
	}
	for _, bad := range []string{"trace", "LOG", "warning"} {
		if validConsoleLevel(bad) {
			t.Fatalf("level %q 应被拒绝", bad)
		}
	}
	for _, ok := range []string{"", "console", "exception", "rejection"} {
		if !validConsoleSource(ok) {
			t.Fatalf("source %q 应被接受", ok)
		}
	}
	for _, bad := range []string{"page", "error", "EXCEPTION"} {
		if validConsoleSource(bad) {
			t.Fatalf("source %q 应被拒绝", bad)
		}
	}
}

// TestConsoleArgs 钉 Go 参数到页面 op 的映射（字段名与 runtime.js 的过滤键一致）。
func TestConsoleArgs(t *testing.T) {
	args := consoleArgs("wait", ConsoleOptions{
		Level: "error", TextContains: "boom", Source: "exception",
		Since: "c3", Limit: 7, ID: "c9",
	})
	want := map[string]any{
		"action": "wait", "level": "error", "text_contains": "boom",
		"source": "exception", "since": "c3", "limit": 7, "id": "c9",
	}
	for k, v := range want {
		if args[k] != v {
			t.Fatalf("args[%q] = %#v，期望 %#v", k, args[k], v)
		}
	}
}
