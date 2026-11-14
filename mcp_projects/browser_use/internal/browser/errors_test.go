package browser

import (
	"regexp"
	"testing"

	"browseruse/internal/browser/runtime"
)

// TestRuntimeErrorCodesAreKnown 钉住双端错误码契约(T1.9):页面运行时产生的
// 每个码 —— err("code", …) 与 {code:"…"},含 code: e.code || "fallback" ——
// 都必须在 errors.go 的常量表里。缺了常量,agent 拿到未知码就无法按 code
// 自纠,HTTP/MCP 层也失去稳定枚举。JS 侧只增码不加常量会在这里直接失败。
func TestRuntimeErrorCodesAreKnown(t *testing.T) {
	src := runtime.Source()
	re := regexp.MustCompile(`err\("([a-z_]+)"|code:\s*(?:e\.code\s*\|\|\s*)?"([a-z_]+)"`)
	known := map[string]bool{
		CodeBadRequest: true, CodeInvalidTab: true, CodeNoTab: true,
		CodeTabClosed: true, CodeTooManyTabs: true, CodeProtectedTab: true,
		CodeStaleRef: true, CodeNotFound: true, CodeAmbiguous: true,
		CodeNotVisible: true, CodeNotEnabled: true, CodeCovered: true,
		CodeTimeout: true, CodeNavFailed: true, CodeBlockedURL: true,
		CodeBlockedAction: true, CodeControlLocked: true, CodePageUnresp: true,
		CodeUnsupported: true, CodeInternal: true, CodeTooManyActions: true,
		CodeNotEditable: true, CodeNotSelect: true, CodeNoFileInput: true,
		CodeNoFrame: true, CodeFrameTimeout: true, CodeFrameError: true,
		CodeRuntimeError: true, CodeForbidden: true,
		CodeScriptSyntax: true, CodeScriptError: true, CodeScriptTimeout: true,
		CodeScriptMemory: true, CodeScriptOutput: true, CodeScriptResult: true,
		CodeScriptSource: true, CodeScriptActions: true, CodeScriptBusy: true,
		CodeScriptHung: true, CodeScriptUnsupported: true,
	}
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		code := m[1]
		if code == "" {
			code = m[2]
		}
		if code == "" {
			continue
		}
		seen[code] = true
		if !known[code] {
			t.Errorf("runtime.js 产出未知错误码 %q(errors.go 缺少常量)", code)
		}
	}
	// 正则失明(比如 err 改名)会让契约静默失效,给一个下限报警。
	if len(seen) < 10 {
		t.Fatalf("正则只匹配到 %d 个码,契约覆盖可能已失效: %v", len(seen), seen)
	}
}
