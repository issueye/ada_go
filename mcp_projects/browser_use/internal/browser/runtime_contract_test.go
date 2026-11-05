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

// TestRuntimePerfContracts 钉住批次 2 的页内性能机制（重构时容易丢）：
//   - ref 表在快照时清扫（sweepRefs）并有 REFS_MAX 硬上限，长驻 SPA 有界；
//   - text= 定位先 textContent 粗筛（deepTextMatch 单次遍历）再做样式计算；
//   - network wait 由 netRecord 页内 resolve（netWaiters），Go 侧不再轮询；
//     invoke 必须对 network await，否则 Promise 展开会得到空对象。
func TestRuntimePerfContracts(t *testing.T) {
	src := runtime.Source()
	for _, want := range []string{
		"function sweepRefs",
		"const REFS_MAX = 2000",
		"state.refsSweptAt = Date.now()",
		"function deepTextMatch",
		"NodeFilter.SHOW_ELEMENT",
		"function networkWait",
		`if (action === "wait") return networkWait(args);`,
		"state.netWaiters.add(waiter)",
		"if (state.netWaiters.size > 0)",
		"...(await network(args))",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("runtime.js 缺少契约片段 %q", want)
		}
	}
}

// TestRuntimeBatch4Contracts 钉住批次 4 的页内机制：
//   - drag 同时发 HTML5 DnD（dragstart→drop）与 pointer 步进序列；
//   - storage 直接读写 localStorage/sessionStorage（set 有 64KiB 上限）；
//   - humanBlocked 记录遮罩期被拦的人类输入（lastHumanBlockedAt）；
//   - 嵌套 iframe：routeOf 解析层级 id，subping/subpong 逐跳收集子帧清单。
func TestRuntimeBatch4Contracts(t *testing.T) {
	src := runtime.Source()
	for _, want := range []string{
		"async function drag",
		"new DataTransfer()",
		`fire(from.el, "dragstart"`,
		`dispatchEvent(pe("pointerdown"`,
		"function storage(",
		"STORAGE_MAX_SET",
		"function humanBlocked(",
		"state.lastHumanBlockedAt",
		"function routeOf(",
		"const MAX_FRAME_DEPTH = 5",
		`__buFrame: "subping"`,
		"__buFrame: \"subpong\"",
		`case "drag"`,
		`case "storage"`,
		`case "humanBlocked"`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("runtime.js 缺少契约片段 %q", want)
		}
	}
}
