// matrix_test.go 把 M0 冻结的安装矩阵（internal/plugins/host/node/matrix.go）与
// moejs v0.1.0-alpha.11 的实测逐条对账。
//
// 判定口径：
//   - TierNever 的模块：必须实测「链接期就失败」，这是硬断言（设计 6.10、6.12.1(1)）；
//   - TierAlways / TierLoop / TierCapability 的模块：必须能 import 且成员可枚举；
//     上游给不了的（alpha.11 只剩 buffer 与 fs/fs/promises）就是宿主工作量，
//     必须能在 deviations.md 里找到对应行，否则 t.Errorf；
//   - 实测与登记的基线（measuredAlpha11）一旦漂移（上游新增或删除了模块），
//     无论朝哪个方向都 t.Errorf：矩阵与偏差表都要跟着改，改谁是矩阵负责人的事。
//
// 本文件不修改 matrix.go，只报差异。
package nodecompat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins/host/node"
)

// probeOutcome 是一个模块名在 alpha.11 里的实测状态。
type probeOutcome string

const (
	pImportOK probeOutcome = "import-ok" // 链接并求值成功，成员可枚举
	pLinkFail probeOutcome = "link-fail" // 链接期就失败（宿主 resolver 不认这个 specifier）
)

// measuredAlpha11 是 go run ./tools/moejspike -case=matrix 的登记结果（重跑即可复现）。
var measuredAlpha11 = map[string]probeOutcome{
	"buffer": pImportOK, "fs": pImportOK, "fs/promises": pImportOK,

	"console": pLinkFail, "path": pLinkFail, "path/posix": pLinkFail, "path/win32": pLinkFail,
	"url": pLinkFail, "querystring": pLinkFail, "string_decoder": pLinkFail, "events": pLinkFail,
	"util": pLinkFail, "assert": pLinkFail, "crypto": pLinkFail, "zlib": pLinkFail,
	"stream/web": pLinkFail, "process": pLinkFail, "os": pLinkFail, "module": pLinkFail,
	"timers": pLinkFail, "timers/promises": pLinkFail, "child_process": pLinkFail,

	// TierNever：alpha.11 里同样一律 LINK-FAIL（宿主 resolver 不认），与矩阵一致。
	"http": pLinkFail, "https": pLinkFail, "net": pLinkFail, "tls": pLinkFail,
	"dgram": pLinkFail, "dns": pLinkFail, "worker_threads": pLinkFail, "cluster": pLinkFail,
	"vm": pLinkFail, "inspector": pLinkFail, "repl": pLinkFail, "wasi": pLinkFail,
	"async_hooks": pLinkFail, "perf_hooks": pLinkFail, "sqlite": pLinkFail, "test": pLinkFail,
}

// neverTierModules 返回矩阵里 TierNever 的全部模块（6.12.2 第四张表）。
func neverTierModules() []string {
	var out []string
	for _, m := range node.Modules() {
		if m.Tier == node.TierNever {
			out = append(out, m.Name)
		}
	}
	return out
}

// TestMatrixTierNeverIsAbsent 断言：矩阵说不安装的模块，在 Runtime 里必须真的不存在。
// 这是「未安装的东西不存在」（6.10）与「未知模块链接期失败」（6.12.1(1)）的可执行版本。
func TestMatrixTierNeverIsAbsent(t *testing.T) {
	names := neverTierModules()
	if len(names) == 0 {
		t.Fatal("矩阵里没有 TierNever 模块？与 6.12.2 第四张表不符")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			st := probeModule(t, "node:"+name)
			if st.linkErr == nil {
				if st.reported {
					t.Errorf("矩阵把 node:%s 标为不安装，实测却能导入（%s）—— 模块表泄漏，违反 6.12.2", name, st.report)
				} else {
					t.Errorf("矩阵把 node:%s 标为不安装，实测链接期没有失败（%s）", name, st.summary())
				}
				return
			}
			if !strings.Contains(st.linkErr.Error(), "cannot resolve module") {
				t.Errorf("node:%s 的失败形状不对：期望宿主 resolver 的 cannot resolve module，实测 %q", name, st.linkErr.Error())
			}
		})
	}
	if err := node.CheckMatrix(); err != nil {
		t.Errorf("安装矩阵自洽检查失败: %v", err)
	}
}

// TestMatrixAgainstUpstream 把每个矩阵模块的实测状态与登记基线对账，
// 并要求「矩阵与实测的差异」在偏差表里有一行。
func TestMatrixAgainstUpstream(t *testing.T) {
	dev := deviationsTableText(t)
	for _, m := range node.Modules() {
		m := m
		t.Run(m.Name, func(t *testing.T) {
			want, ok := measuredAlpha11[m.Name]
			if !ok {
				t.Fatalf("模块 %s 没登记 alpha.11 实测状态（重跑 go run ./tools/moejspike -case=matrix 后补进 measuredAlpha11）", m.Name)
			}
			got := probeModule(t, "node:"+m.Name)
			switch {
			case want == pImportOK && !got.reported:
				t.Errorf("登记基线说 node:%s 可导入，实测失败（上游回归？矩阵的 Tier=%q 要重估）：%s", m.Name, m.Tier, got.summary())
			case want != pImportOK && got.reported:
				t.Errorf("登记基线说 node:%s 是 %s，实测却能导入（%s）—— 上游新增了模块，偏差表与矩阵都要改", m.Name, want, got.report)
			}
			if m.Tier == node.TierNever || got.reported {
				return
			}
			if !deviatesFor(dev, m.Name) {
				t.Errorf("矩阵把 %s 列为 %s，alpha.11 实测装不出来，但 deviations.md 里没有记载这个模块的差异行（矩阵是 M1-C 的输入，差异不能只留在测试里）", m.Name, m.Tier)
			}
		})
	}
}

// TestMatrixTierAlwaysOwnership 把「始终安装」这张表分成上游提供 / 宿主工作量两堆，
// 并硬断言上游提供的那批成员真的可枚举。
func TestMatrixTierAlwaysOwnership(t *testing.T) {
	var provided, host []string
	for _, m := range node.Modules() {
		if m.Tier != node.TierAlways {
			continue
		}
		st := probeModule(t, "node:"+m.Name)
		if !st.reported {
			host = append(host, m.Name)
			continue
		}
		provided = append(provided, m.Name)
		if n := memberCount(t, st.report); n <= 0 {
			t.Errorf("TierAlways 模块 node:%s 能导入但成员不可枚举（%s）", m.Name, st.report)
		}
	}
	if len(provided) == 0 {
		t.Error("alpha.11 里没有任何 TierAlways 模块由上游提供？与 D-7 的判定不符（buffer 应当可导入）")
	}
	t.Logf("TierAlways 对账：上游提供 %d 个 %v；ada-go 宿主工作量 %d 个 %v（M1-C 落地，见 deviations.md）",
		len(provided), provided, len(host), host)
	if len(host) < 12 {
		t.Errorf("登记给宿主的 TierAlways 模块只有 %d 个，与尖刺实测（path/crypto/zlib/process/os/module/console/events/util/assert/url/querystring/string_decoder/stream/web 共 15 个）不符 —— 上游可能新增了模块，需要重跑尖刺并更新偏差表", len(host))
	}
}

// TestGlobalsAgainstDesign 锁住全局面：上游没给的必须真的没有（console/process/Blob），
// 上游多给的必须被记载（SharedArrayBuffer/Atomics，设计 6.11 二进制行说不提供）。
func TestGlobalsAgainstDesign(t *testing.T) {
	st := runGlobalsProbe(t, false)
	if !st.reported {
		t.Fatalf("裸 Runtime 全局探针没跑起来: %s", st.summary())
	}
	// 裸 Runtime（L1 引擎层，没有 Loop）：Node 的那套全局一律不存在。
	for _, name := range []string{"Buffer", "process", "console", "setTimeout", "setInterval", "setImmediate", "URL", "fetch"} {
		if got := globalValue(t, st.report, name); got != "undefined" {
			t.Errorf("裸 Runtime（L1 引擎）里 %s 应当不存在，实测 typeof=%s —— 与设计 6.10「未安装的东西不存在」冲突", name, got)
		}
	}
	if got := globalValue(t, st.report, "SharedArrayBuffer"); got == "undefined" {
		t.Errorf("登记偏差说上游仍然提供 SharedArrayBuffer，实测没有 —— 上游若已补禁用位，deviations.md 对应行可以撤销")
	}
	if !deviatesFor(deviationsTableText(t), "SharedArrayBuffer") {
		t.Error("SharedArrayBuffer/Atomics 与设计 6.11 相反，却没在 deviations.md 里记载")
	}

	st = runGlobalsProbe(t, true)
	if !st.reported {
		t.Fatalf("装齐 node+webapi 的全局探针没跑起来: %s", st.summary())
	}
	for _, name := range []string{"Blob", "File", "FormData", "process", "console", "crypto"} {
		if got := globalValue(t, st.report, name); got != "undefined" {
			t.Errorf("装齐 node+webapi 后 %s 仍应当不存在（上游不提供 / 宿主未安装），实测 typeof=%s —— 基线要更新", name, got)
		}
	}
	for _, name := range []string{"Buffer", "URL", "AbortController", "ReadableStream", "performance", "fetch", "setTimeout"} {
		if got := globalValue(t, st.report, name); got == "undefined" {
			t.Errorf("node/webapi/eventloop 应当提供全局 %s，实测缺失 —— 与登记的语料基线不符", name)
		}
	}
	for _, name := range []string{"setInterval", "setImmediate", "structuredClone", "queueMicrotask"} {
		if got := globalValue(t, st.report, name); got == "undefined" {
			t.Errorf("定时器/克隆基线里 %s 应当存在，实测缺失", name)
		}
	}
}

// probeModule 在装好 fs/buffer/webapi 的 Runtime 里静态导入某个模块名。
func probeModule(t *testing.T, spec string) *runState {
	t.Helper()
	ws := prepareWorkspace(t, false)
	src := fmt.Sprintf(`import * as m from %q;
__report(JSON.stringify({keys: Object.keys(m).length}));`, spec)
	return runCase(t, src, runtimeSpec{FS: "ro", Buffer: true, Web: true}, ws)
}

// memberCount 从探针输出里取成员数量。
func memberCount(t *testing.T, report string) int {
	t.Helper()
	var m struct {
		Keys int `json:"keys"`
	}
	if err := json.Unmarshal([]byte(report), &m); err != nil {
		t.Fatalf("解析探针输出失败: %v（原始=%s）", err, report)
	}
	return m.Keys
}

const globalsProbe = `__report(JSON.stringify((() => {
const names = ["Buffer","process","console","setTimeout","setInterval","setImmediate","URL","URLSearchParams",
  "fetch","AbortController","AbortSignal","EventTarget","DOMException","performance","ReadableStream",
  "WritableStream","TransformStream","Blob","File","FormData","crypto","SharedArrayBuffer","Atomics","Worker","structuredClone","queueMicrotask"];
const out = {};
for (const n of names) out[n] = typeof globalThis[n];
return out;
})()));`

// runGlobalsProbe 跑全局面探针；withStdlib=false 时是裸 Runtime（设计里的 L1 引擎层）。
func runGlobalsProbe(t *testing.T, withStdlib bool) *runState {
	t.Helper()
	spec := runtimeSpec{Bare: true}
	if withStdlib {
		spec = runtimeSpec{FS: "ro", Buffer: true, Web: true}
	}
	return runCase(t, globalsProbe, spec, prepareWorkspace(t, false))
}

func globalValue(t *testing.T, report, name string) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(report), &m); err != nil {
		t.Fatalf("解析全局面失败: %v", err)
	}
	v, ok := m[name]
	if !ok {
		t.Fatalf("全局面探针没有报告 %s", name)
	}
	return v
}

// deviationsTableText 读偏差表正文（测试与文档之间的最后一道咬合）。
func deviationsTableText(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试源文件路径")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "deviations.md"))
	if err != nil {
		t.Fatalf("偏差表缺失（M1-D 的交付物之一）: %v", err)
	}
	return string(raw)
}

// deviatesFor 判断偏差表里是否有记载该模块/成员的行。
func deviatesFor(text, name string) bool {
	if name == "" {
		return false
	}
	return strings.Contains(text, "`"+name+"`") || strings.Contains(text, name)
}
