// corpus_test.go 把 corpus/ 的九层语料跑在 moejs v0.1.0-alpha.11 上，并断言
// 「当前真实行为」（不是 Node 20 的期望行为——期望与实测之差就是 deviations.md）。
//
// 三条铁律（M1-D 的验收口径，也是 D-7 的口径）：
//  1. 上游没有的能力 → t.Skip("上游缺口 E-x：…")，或断言「模块不存在」；绝不写永真断言。
//  2. ada-go 宿主侧才有的行为（execFile、16MB/10k 夹逼、成员裁剪调用即抛）→ 宿主还没实现，
//     t.Skip("等待 M1-C 宿主安装层：…")。
//  3. 已符合的项要真断言（ENOENT 的 code/errno/syscall/path、fs 与 node:fs 同实例、
//     中断不可 catch、WithoutCodeGeneration 下 eval 抛错、定时器顺序与 unref…）。
package nodecompat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// decision 是基线里写的判定，对应 corpus/README.md 的三取一。
type decision string

const (
	dAssert       decision = "assert"
	dSkipUpstream decision = "skip-upstream"
	dSkipHost     decision = "skip-host"
)

// expect 是 <case>.expect.json 的形状。
type expect struct {
	ID         string          `json:"id"`
	Layer      string          `json:"layer"`
	Runtime    runtimeOverride `json:"runtime"`
	Decision   decision        `json:"decision"`
	Reason     string          `json:"reason"`
	Result     json.RawMessage `json:"result"`
	Observe    observe         `json:"observe"`
	NodeParity string          `json:"nodeParity"`
}

// runtimeOverride 用指针字段，好区分「基线里没写」与「显式写了零值」：
// 没写的键沿用所在层的默认配置（layerRuntime）。
type runtimeOverride struct {
	FS               *string `json:"fs"`
	Buffer           *bool   `json:"buffer"`
	Web              *bool   `json:"web"`
	MemLimit         *int64  `json:"memLimitBytes"`
	NoCodegen        *bool   `json:"disableDynamicCode"`
	Bare             *bool   `json:"bareRuntime"`
	InterruptAfterMS *int    `json:"interruptAfterMs"`
	StopAfterMS      *int    `json:"stopAfterMs"`
	WithMany         *bool   `json:"withMany"`
}

// apply 把覆盖项叠到层默认值上。
func (o runtimeOverride) apply(def runtimeSpec) runtimeSpec {
	out := def
	if o.FS != nil {
		out.FS = *o.FS
	}
	if o.Buffer != nil {
		out.Buffer = *o.Buffer
	}
	if o.Web != nil {
		out.Web = *o.Web
	}
	if o.MemLimit != nil {
		out.MemLimit = *o.MemLimit
	}
	if o.NoCodegen != nil {
		out.NoCodegen = *o.NoCodegen
	}
	if o.Bare != nil {
		out.Bare = *o.Bare
	}
	if o.InterruptAfterMS != nil {
		out.InterruptAfterMS = *o.InterruptAfterMS
	}
	if o.StopAfterMS != nil {
		out.StopAfterMS = *o.StopAfterMS
	}
	if o.WithMany != nil {
		out.WithMany = *o.WithMany
	}
	return out
}

// toOverride 把生效配置写回基线（记录模式：全部键显式落地，基线自解释）。
func toOverride(s runtimeSpec) runtimeOverride {
	return runtimeOverride{
		FS: ptr(s.FS), Buffer: ptr(s.Buffer), Web: ptr(s.Web), MemLimit: ptr(s.MemLimit),
		NoCodegen: ptr(s.NoCodegen), Bare: ptr(s.Bare),
		InterruptAfterMS: ptr(s.InterruptAfterMS), StopAfterMS: ptr(s.StopAfterMS),
		WithMany: ptr(s.WithMany),
	}
}

func ptr[T any](v T) *T { return &v }

// observe 是 Go 侧观察（脚本抓不到的东西只能在这里断言）。
type observe struct {
	Reported        *bool  `json:"reported"`
	Rejections      *int   `json:"rejections"`
	RunErrContains  string `json:"runErrContains"`
	LoadErrContains string `json:"loadErrContains"`
}

// groups 是语料自报的四组可比较结果（corpus/README.md 的协议）。
type groups struct {
	Returns map[string]any `json:"returns"`
	Codes   map[string]any `json:"codes"`
	Order   []any          `json:"order"`
	Effects map[string]any `json:"effects"`
	Skip    string         `json:"skip"`
}

// layerRuntime 是每个层的默认 Runtime 配置：只打开上游真实存在的安装面
// （node.Options 的 FS/Buffer 与 webapi 的全局），不装任何 ada-go 宿主模块。
func layerRuntime(layer string) runtimeSpec {
	spec := runtimeSpec{Buffer: true, Web: true}
	switch layer {
	case "01-modules", "05-fs", "06-process-os", "09-errors":
		spec.FS = "ro"
	}
	return spec
}

// corpusDir 返回语料目录。用 runtime.Caller 而不是相对路径，
// 这样 IDE 以仓库根为工作目录跑测试时也能定位（同 internal/conformance 的口径）。
func corpusDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试源文件路径")
	}
	dir := filepath.Join(filepath.Dir(thisFile), rootName)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("语料目录缺失 %s: %v", dir, err)
	}
	return dir
}

// recording 是 ADAG_NODECOMPAT_RECORD=1：把实测写回基线的 result/observe 段。
func recording() bool { return os.Getenv("ADAG_NODECOMPAT_RECORD") == "1" }

// TestNodeCompat 是设计 6.14.4 的门禁入口（CI 用 -run TestNodeCompat 抓它）。
func TestNodeCompat(t *testing.T) {
	dir := corpusDir(t)
	var names []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// 下划线前缀是夹具与运行器（_run-one.mjs、_lib/），不是用例：它们只在 Node 侧
		// 对跑时被 fork，moejs 侧没有对应的宿主安装面，当成用例会误报链接失败。
		if d.IsDir() || !strings.HasSuffix(p, ".mjs") || strings.HasSuffix(p, ".inc.mjs") {
			return nil
		}
		if base := filepath.Base(p); base == "run-node20.mjs" || strings.HasPrefix(base, "_") {
			return nil
		}
		rel, aerr := filepath.Rel(dir, p)
		if aerr != nil {
			return aerr
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("扫描语料失败: %v", err)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("语料为空：corpus/ 下一份用例 .mjs 都没找到")
	}

	for _, rel := range names {
		rel := rel
		t.Run(strings.TrimSuffix(filepath.Base(rel), ".mjs"), func(t *testing.T) {
			runCorpusCase(t, dir, rel)
		})
	}
}

func runCorpusCase(t *testing.T, dir, rel string) {
	t.Helper()
	layer := filepath.ToSlash(filepath.Dir(rel))
	id := strings.TrimSuffix(filepath.Base(rel), ".mjs")
	casePath := filepath.Join(dir, filepath.FromSlash(rel))
	if _, err := os.ReadFile(casePath); err != nil {
		t.Fatalf("读语料失败: %v", err)
	}

	expPath := strings.TrimSuffix(casePath, ".mjs") + ".expect.json"
	exp := loadExpect(t, expPath, id, layer)

	// 铁律 1/2：上游缺口与宿主未落地都不伪装成通过。
	switch exp.Decision {
	case dSkipUpstream:
		requireReason(t, exp, id)
		t.Skipf("上游缺口 E-x（%s）：%s", id, exp.Reason)
	case dSkipHost:
		requireReason(t, exp, id)
		t.Skipf("等待 M1-C 宿主安装层（%s）：%s", id, exp.Reason)
	case dAssert:
	default:
		t.Fatalf("基线 %s 的 decision=%q 非法（assert|skip-upstream|skip-host）", id, exp.Decision)
	}

	spec := exp.Runtime.apply(layerRuntime(layer))
	ws := prepareWorkspace(t, spec.WithMany)
	src := caseSource(modulePrefix+rel, ws, spec.FS == "ro")
	st := runCase(t, src, spec, ws)

	if recording() {
		exp.Runtime = toOverride(spec)
		writeBaseline(t, expPath, exp, st)
		return
	}

	wantsReport := exp.Observe.Reported == nil || *exp.Observe.Reported
	if st.linkErr != nil {
		t.Fatalf("语料链接失败: %v\n%s", st.linkErr, st.summary())
	}
	if st.loadErr != nil && exp.Observe.LoadErrContains == "" {
		t.Fatalf("语料求值失败: %v\n%s", st.loadErr, st.summary())
	}
	if wantsReport && !st.reported {
		t.Fatalf("语料没有上报四组结果\n%s", st.summary())
	}

	g, raw := parseGroups(t, st)
	if g.Skip != "" {
		t.Skipf("语料自报未验证：%s", g.Skip)
	}

	// 协议自检：四组必须齐全，缺组就是语料写坏了，不能算通过。
	for _, chk := range []struct {
		name string
		bad  bool
	}{
		{"returns", g.Returns == nil},
		{"codes", g.Codes == nil},
		{"order", g.Order == nil},
		{"effects", g.Effects == nil},
	} {
		if chk.bad {
			t.Errorf("语料 %s 的产物缺少 %q 组（协议要求四组都在，见 corpus/README.md）", id, chk.name)
		}
	}

	var want map[string]any
	if len(exp.Result) > 0 {
		if err := json.Unmarshal(exp.Result, &want); err != nil {
			t.Fatalf("基线 result 段不是合法 JSON: %v", err)
		}
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("实测结果不是合法 JSON: %v\n原始输出=%s", err, st.report)
	}
	for _, d := range subsetDiff(want, got, "") {
		t.Errorf("与基线不符：%s", d)
	}
	assertObserve(t, id, exp.Observe, st)
}

// parseGroups 解析 __report 的 JSON。
func parseGroups(t *testing.T, st *runState) (*groups, json.RawMessage) {
	t.Helper()
	g := &groups{}
	raw := json.RawMessage(st.report)
	if len(raw) == 0 {
		return g, raw
	}
	if err := json.Unmarshal(raw, g); err != nil {
		t.Fatalf("解析语料产物失败: %v\n原始输出=%s", err, st.report)
	}
	return g, raw
}

// assertObserve 断言 Go 侧观察：中断、内存上限、未处理 rejection 这类脚本抓不到的事实。
func assertObserve(t *testing.T, id string, o observe, st *runState) {
	t.Helper()
	if o.Reported != nil && *o.Reported != st.reported {
		t.Errorf("%s: reported 期望 %v，实测 %v（%s）", id, *o.Reported, st.reported, st.summary())
	}
	if o.Rejections != nil && *o.Rejections != st.rejections {
		t.Errorf("%s: 未处理 rejection 次数期望 %d，实测 %d", id, *o.Rejections, st.rejections)
	}
	if o.RunErrContains != "" {
		if st.runErr == nil {
			t.Errorf("%s: 期望 Run 以包含 %q 的错误终止，实测没有错误（%s）", id, o.RunErrContains, st.summary())
		} else if !strings.Contains(st.runErr.Error(), o.RunErrContains) {
			t.Errorf("%s: Run 错误期望包含 %q，实测 %q", id, o.RunErrContains, st.runErr.Error())
		}
	}
	if o.LoadErrContains != "" {
		if st.loadErr == nil {
			t.Errorf("%s: 期望模块求值期失败并包含 %q，实测没有（%s）", id, o.LoadErrContains, st.summary())
		} else if !strings.Contains(st.loadErr.Error(), o.LoadErrContains) {
			t.Errorf("%s: 求值期错误期望包含 %q，实测 %q", id, o.LoadErrContains, st.loadErr.Error())
		}
	}
}

// loadExpect 读基线；记录模式下允许缺失（生成骨架后由人复核）。
func loadExpect(t *testing.T, path, id, layer string) *expect {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if recording() {
			return &expect{ID: id, Layer: layer, Decision: dAssert, Runtime: toOverride(layerRuntime(layer))}
		}
		t.Skipf("基线缺失 %s.expect.json —— 用 ADAG_NODECOMPAT_RECORD=1 生成并人工复核，不算通过", id)
	}
	var e expect
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("基线 %s 解析失败: %v", path, err)
	}
	if e.ID != id {
		t.Errorf("基线 id=%q 与文件名 %q 不符", e.ID, id)
	}
	if e.Layer == "" {
		e.Layer = layer
	}
	return &e
}

// requireReason 保证 skip 真有归属，不是随手关掉一条断言。
func requireReason(t *testing.T, e *expect, id string) {
	t.Helper()
	if strings.TrimSpace(e.Reason) == "" {
		t.Errorf("%s: decision=%s 却没写 reason（必须指到 E-x 或「等待 M1-C 宿主安装层」）", id, e.Decision)
	}
	if e.Decision == dSkipUpstream && !strings.Contains(e.Reason, "E-") {
		t.Errorf("%s: skip-upstream 的 reason 必须引用上游缺口编号 E-1…E-8，实际=%q", id, e.Reason)
	}
}

// subsetDiff 按基线的键做子集比较：基线没写的键不参与判定。
func subsetDiff(want map[string]any, got map[string]any, at string) []string {
	var out []string
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := k
		if at != "" {
			path = at + "." + k
		}
		gv, ok := got[k]
		if !ok {
			out = append(out, fmt.Sprintf("%s: 实测没有这个键", path))
			continue
		}
		out = append(out, valueDiff(path, want[k], gv)...)
	}
	return out
}

func valueDiff(path string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: 期望对象，实得 %T", path, got)}
		}
		return subsetDiff(w, g, path)
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: 期望数组，实得 %T", path, got)}
		}
		var out []string
		for i, e := range w {
			if i >= len(g) {
				out = append(out, fmt.Sprintf("%s[%d]: 实测长度 %d 不够", path, i, len(g)))
				continue
			}
			out = append(out, valueDiff(fmt.Sprintf("%s[%d]", path, i), e, g[i])...)
		}
		return out
	default:
		if !reflect.DeepEqual(want, got) {
			return []string{fmt.Sprintf("%s: 基线 %s，实测 %s", path, jsonText(want), jsonText(got))}
		}
	}
	return nil
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

// writeBaseline 是记录模式：重写 result/observe/runtime 三段，
// decision/reason/nodeParity 归人管（已存在就保留）。
func writeBaseline(t *testing.T, path string, e *expect, st *runState) {
	t.Helper()
	if st.reported {
		var m map[string]any
		if err := json.Unmarshal([]byte(st.report), &m); err != nil {
			t.Fatalf("实测输出不是 JSON: %v", err)
		}
		e.Result = mustJSON(m)
	}
	rep, rj := st.reported, st.rejections
	e.Observe.Reported, e.Observe.Rejections = &rep, &rj
	e.Observe.RunErrContains, e.Observe.LoadErrContains = "", ""
	if st.runErr != nil {
		e.Observe.RunErrContains = firstFragment(st.runErr.Error())
	}
	if st.loadErr != nil {
		e.Observe.LoadErrContains = firstFragment(st.loadErr.Error())
	}
	if e.NodeParity == "" {
		e.NodeParity = parityNote[e.ID]
		if e.NodeParity == "" {
			e.NodeParity = "unknown"
		}
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		t.Fatalf("编基线: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("写基线: %v", err)
	}
	t.Logf("已写基线 %s", filepath.Base(path))
}

// firstFragment 取 Go 错误里稳定的前缀（类型 + 首个短语）当匹配关键字，
// 免得把临时目录之类的非确定内容写进基线。
func firstFragment(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if i := strings.Index(s, " "); i > 0 && i < 60 {
		return s[:i]
	}
	if len(s) > 60 {
		return s[:60]
	}
	return s
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("nodecompat: 编码基线失败: %v", err))
	}
	return b
}

// parityNote 见 parity_test.go：人工归档每个用例与真 Node 20 的关系，
// 也就是 deviations.md 的行索引。
