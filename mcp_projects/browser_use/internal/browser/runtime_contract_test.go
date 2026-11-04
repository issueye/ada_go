package browser

import (
	"strings"
	"testing"

	"browseruse/internal/browser/runtime"
)

// TestRuntimeCoordinateAndHiddenContracts 钉住三个容易在重构中丢掉的页面语义：
//   - point 点击必须使用解析出的坐标，而不是元素中心（canvas/地图/视频控件）；
//   - click 必须由显式 MouseEvent 派发，否则事件里的 clientX/clientY 恒为 0；
//   - wait for selector state=hidden 在元素不存在时必须立即命中，而不是等到超时。
//
// 页面运行时没有独立的 JS 测试进程，这里用源码契约做最小回归；真正的 DOM
// 行为测试留待 e2e（见 DESIGN §13.2）。
func TestRuntimeCoordinateAndHiddenContracts(t *testing.T) {
	src := runtime.Source()
	for _, want := range []string{
		"resolved.point.x",
		"resolved.point.y",
		`new MouseEvent("click"`,
		`found: stateWant === "hidden"`,
		"function isClickable",
		"function rankMatches",
		"ACTION_WAIT_MS",
		"dialogPolicy",
		"function installNav",
		"navCount",
		"diff=1",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("runtime.js 缺少契约片段 %q", want)
		}
	}
}
