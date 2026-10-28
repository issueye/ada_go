// harness_test.go 是 nodecompat 语料的宿主夹具：把 corpus/ 里的 JS 用例跑在
// moejs v0.1.0-alpha.11 的真实 API 上，并把「返回值 / 错误 code / 事件顺序 / 副作用」
// 四组结果按 corpus/README.md 的协议收成一个 JSON。
//
// 这个夹具只暴露上游真实存在的能力（node.Options 只有 FS 与 Buffer；webapi 只有
// fetch/URL/Streams/Abort*）：它不模拟 M1-C 的 ada-go 宿主安装层。宿主行为
// （execFile、16MB/10k 夹逼、成员裁剪调用即抛、process/console/crypto）在语料里
// 一律 decision=skip-host，等 M1-C 再翻成断言，不在这里用测试替身冒充。
package nodecompat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"
	"github.com/MoeForge/moejs/node"
	"github.com/MoeForge/moejs/webapi"
)

// entryName 是入口模块的虚拟名字（设计 6.12.3 的 plugin://<id>/ 口径）。
// 用固定字符串而不是宿主路径，错误栈映射断言（e-01）才有意义。
const entryName = "plugin://conformance/index.js"

// runtimeSpec 是一个语料用例需要的 Runtime 配置，对应 corpus/README.md 的 runtime 字段。
type runtimeSpec struct {
	FS        string `json:"fs"` // none | ro | rw
	Buffer    bool   `json:"buffer"`
	Web       bool   `json:"web"`
	MemLimit  int64  `json:"memLimitBytes"`
	NoCodegen bool   `json:"disableDynamicCode"`
	Bare      bool   `json:"bareRuntime"` // true：不建 eventloop（没有定时器）

	InterruptAfterMS int  `json:"interruptAfterMs"` // >0：Run 到该时长后 Loop.Interrupt
	StopAfterMS      int  `json:"stopAfterMs"`      // >0：Run 到该时长后 Loop.Stop
	WithMany         bool `json:"withMany"`         // 夹具是否铺 many/（10500 条）与 big.bin（20MB）

	// Setup 在入口模块求值之前往 Runtime 上装宿主函数，只给 engine_test.go 的运行时
	// 事实探针用（AsyncWork 关闭竞态、未到期定时器活到下一次 Run）。不参与 JSON 基线。
	Setup func(h *hostProbe) `json:"-"`
}

// hostProbe 是给运行时事实探针的宿主把手（能力面与 tools/moejspike 的夹具一致）。
type hostProbe struct {
	rt   *moejs.Runtime
	loop *eventloop.Loop
	st   *runState
}

// setNative 把一个宿主函数装到全局。
func (h *hostProbe) setNative(name string, fn moejs.NativeFunc) {
	if err := h.rt.SetGlobal(name, fn); err != nil {
		h.st.setupErr = errors.Join(h.st.setupErr, fmt.Errorf("SetGlobal %s: %w", name, err))
	}
}

// peek 在当前 Runtime 上求值一段脚本，但不驱动 Loop：用来读「上一轮结束后、下一轮开始前」
// 的状态（迟到的宿主投递到底有没有跑）。调用方保证此时 Loop 没在 Run。
func (s *runState) peek(t *testing.T, src string) string {
	t.Helper()
	if s.rt == nil {
		return "无 Runtime"
	}
	script, err := moejs.CompileScript("peek", src)
	if err != nil {
		return "编译失败: " + err.Error()
	}
	v, err := s.rt.RunScript(script)
	if err != nil {
		return "runErr: " + err.Error()
	}
	out, err := s.rt.ToGo(v)
	if err != nil {
		return "ToGo: " + err.Error()
	}
	return fmt.Sprint(out)
}

// afterRunEval 在同一 Runtime 上、Loop 结束之后求值一段脚本。
// 用来观察「上一轮留下的状态」：未到期定时器有没有补跑、未结算的 Promise 有没有被拒绝。
func (s *runState) afterRunEval(t *testing.T, src string) string {
	t.Helper()
	if s.rt == nil {
		return "无 Runtime"
	}
	script, err := moejs.CompileScript("resume", src)
	if err != nil {
		return "编译失败: " + err.Error()
	}
	if s.loop != nil {
		_ = s.loop.Run(func(*moejs.Runtime) error { return nil })
	}
	v, err := s.rt.RunScript(script)
	if err != nil {
		return "runErr: " + err.Error()
	}
	out, err := s.rt.ToGo(v)
	if err != nil {
		return "ToGo: " + err.Error()
	}
	return fmt.Sprint(out)
}

// runState 收集一次运行的全部可观察面（Go 侧 + JS 侧）。
type runState struct {
	report     string
	reported   bool
	compileErr error
	linkErr    error
	loadErr    error
	runErr     error
	setupErr   error
	rejections int
	ranMS      int64

	// rt/loop 留给 afterRunEval：观察「上一轮之后」的状态
	// （未到期定时器是否补跑、未结算 Promise 是否被拒绝）。
	rt   *moejs.Runtime
	loop *eventloop.Loop
}

func (s *runState) summary() string {
	var parts []string
	for _, kv := range []struct {
		name string
		err  error
	}{
		{"setupErr", s.setupErr}, {"compileErr", s.compileErr},
		{"linkErr", s.linkErr}, {"loadErr", s.loadErr}, {"runErr", s.runErr},
	} {
		if kv.err != nil {
			parts = append(parts, kv.name+"="+fmt.Sprintf("%T: %s", kv.err, strings.ReplaceAll(kv.err.Error(), "\n", " | ")))
		}
	}
	if s.reported {
		parts = append(parts, "JS="+s.report)
	}
	if len(parts) == 0 {
		parts = append(parts, "JS=<未上报>")
	}
	return strings.Join(parts, "\n         ")
}

// workspace 是夹具工作区：宿主目录 + 语料看到的虚拟根（设计口径 /）。
type workspace struct {
	Root  string // 宿主绝对路径
	Mount string // 脚本视图
}

// prepareWorkspace 铺设语料依赖的夹具（协议规定由运行器准备，Node 侧同构）：
//
//	hello.txt、dir/inner.txt、many/（10500 条目，仅在 withMany 时）、
//	out.link -> ../outside/secret.txt（平台不支持符号链接时跳过，语料自报 skip）。
func prepareWorkspace(t *testing.T, withMany bool) *workspace {
	t.Helper()
	base, err := os.MkdirTemp("", "adag-nodecompat-")
	if err != nil {
		t.Fatalf("建临时工作区失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "ws")
	for _, d := range []string{root, filepath.Join(base, "outside"), filepath.Join(root, "dir")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("建夹具目录 %s: %v", d, err)
		}
	}
	put := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatalf("写夹具 %s: %v", p, err)
		}
	}
	put(filepath.Join(root, "hello.txt"), "hello moejs")
	put(filepath.Join(root, "dir", "inner.txt"), "inner")
	put(filepath.Join(root, ".env"), "SECRET=hunter2")
	put(filepath.Join(base, "outside", "secret.txt"), "outside secret")
	if withMany {
		many := filepath.Join(root, "many")
		if err := os.MkdirAll(many, 0o755); err != nil {
			t.Fatalf("建 many: %v", err)
		}
		for i := 0; i < 10500; i++ {
			if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("f%05d.txt", i)), []byte("x"), 0o644); err != nil {
				t.Fatalf("建 many 条目: %v", err)
			}
		}
	}
	// 越界符号链接：Windows 非开发者模式会失败，失败就留空，语料自己用 lstat 探测并 skip。
	_ = os.Symlink(filepath.Join(base, "outside", "secret.txt"), filepath.Join(root, "out.link"))
	_ = os.Symlink(filepath.Join(root, "hello.txt"), filepath.Join(root, "inner.link"))
	return &workspace{Root: root, Mount: "/workspace"}
}

// caseSource 把语料包成一个入口模块：跑 run(ctx)，把四组结果 JSON 上报给宿主。
func caseSource(virtualName string, ws *workspace, readOnly bool) string {
	mount := "/workspace"
	if ws == nil {
		mount = ""
	}
	return fmt.Sprintf(`import __case from %q;
const __ctx = { workspace: %q, host: "moejs", readOnly: %v };
let __res;
try {
  __res = await __case(__ctx);
} catch (e) {
  __res = { caseThrew: { name: String(e && e.name), code: (e && e.code !== undefined) ? e.code : null, message: String(e && e.message) } };
}
__report(JSON.stringify(__res));
`, virtualName, mount, readOnly)
}

// runCase 编译-链接-加载-运行一个语料用例。
//
// 与 tools/moejspike 的夹具同源，但这里不带 -case 探针的判定逻辑：只负责真实执行
// 并把结果交出去，判定全在 *.expect.json 基线里。
func runCase(t *testing.T, src string, spec runtimeSpec, ws *workspace) *runState {
	t.Helper()
	st := &runState{}

	virtual := corpusModules(t)
	cache := map[string]*moejs.Module{}
	hostResolve := func(from moejs.Referrer, spec2 string) (*moejs.Module, error) {
		name := resolveVirtual(virtual, cache, from, spec2)
		if name == "" {
			if strings.HasPrefix(spec2, "node:") {
				return nil, fmt.Errorf("No such built-in module: %s", spec2)
			}
			return nil, fmt.Errorf("Cannot find module '%s'", spec2)
		}
		if m, ok := cache[name]; ok {
			return m, nil
		}
		m, err := moejs.Compile(name, virtual[name])
		if err != nil {
			return nil, err
		}
		cache[name] = m
		return m, nil
	}
	resolve := moejs.Resolver(hostResolve)
	resolve = node.Resolver(resolve)
	rt := moejs.NewRuntime(moejs.Options{
		Importer:           &moejs.Importer{Resolve: resolve},
		MemoryLimit:        spec.MemLimit,
		DisableDynamicCode: spec.NoCodegen,
	})

	m, err := moejs.Compile(entryName, src)
	if err != nil {
		st.compileErr = err
		return st
	}
	linked, lerr := moejs.Link(m, resolve)
	if lerr != nil {
		st.linkErr = lerr
		return st
	}
	if spec.Bare {
		// 不建 Loop：验证「没有事件循环时定时器不存在」（设计 6.12.2 的 Loop 前置）。
		if err := rt.SetGlobal("__report", reportFunc(st)); err != nil {
			st.setupErr = err
			return st
		}
		if err := rt.Load(linked); err != nil {
			st.loadErr = err
		}
		return st
	}

	opts := eventloop.Options{}
	loop, err := eventloop.New(rt, opts)
	if err != nil {
		st.setupErr = fmt.Errorf("eventloop.New: %w", err)
		return st
	}
	rt.SetPromiseRejectionTracker(func(_ moejs.Value, op moejs.PromiseRejectionOperation) {
		if op == moejs.PromiseRejectionReject {
			st.rejections++
		}
	})
	if spec.FS != "" && spec.FS != "none" {
		if ws == nil {
			t.Fatal("语料声明了 fs 却没有工作区夹具")
		}
		api, ierr := node.InstallLoop(loop, node.Options{
			FS: &node.FSOptions{
				Root:  ws.Root,
				Mount: ws.Mount,
				Write: spec.FS == "rw",
			},
			Buffer: spec.Buffer,
		})
		if ierr != nil {
			st.setupErr = errors.Join(st.setupErr, fmt.Errorf("node.InstallLoop: %w", ierr))
			return st
		}
		defer api.Close()
	} else if spec.Buffer {
		api, ierr := node.InstallLoop(loop, node.Options{Buffer: true})
		if ierr != nil {
			st.setupErr = errors.Join(st.setupErr, fmt.Errorf("node.InstallLoop(buffer): %w", ierr))
			return st
		}
		defer api.Close()
	}
	if spec.Web {
		api, werr := webapi.Install(loop, webapi.Options{})
		if werr != nil {
			st.setupErr = errors.Join(st.setupErr, fmt.Errorf("webapi.Install: %w", werr))
			return st
		}
		defer api.Close()
	}

	start := time.Now()
	st.rt, st.loop = rt, loop
	st.runErr = loop.Run(func(rt *moejs.Runtime) error {
		if err := rt.SetGlobal("__report", reportFunc(st)); err != nil {
			st.setupErr = errors.Join(st.setupErr, err)
			return nil
		}
		if spec.Setup != nil {
			spec.Setup(&hostProbe{rt: rt, loop: loop, st: st})
		}
		if spec.InterruptAfterMS > 0 {
			time.AfterFunc(time.Duration(spec.InterruptAfterMS)*time.Millisecond, func() {
				loop.Interrupt("nodecompat deadline")
			})
		}
		if spec.StopAfterMS > 0 {
			time.AfterFunc(time.Duration(spec.StopAfterMS)*time.Millisecond, func() { _ = loop.Stop() })
		}
		if err := rt.Load(linked); err != nil && !errors.Is(err, moejs.ErrModulePending) {
			st.loadErr = err
		}
		return nil
	})
	st.ranMS = time.Since(start).Milliseconds()
	return st
}

// reportFunc 是语料上报四组结果的宿主函数。
func reportFunc(st *runState) moejs.NativeFunc {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		st.reported = true
		st.report = moejs.Arg(args, 0).String()
		return moejs.Undefined(), nil
	}
}

// corpusModules 读全部语料源码（虚拟模块名 = corpus/<layer>/<file>），一次读、多用例复用。
func corpusModules(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(corpusDir(t), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".mjs") {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, aerr := filepath.Rel(corpusDir(t), p)
		if aerr != nil {
			return aerr
		}
		out[modulePrefix+filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("读语料失败: %v", err)
	}
	return out
}

const (
	rootName     = "corpus"
	modulePrefix = "plugin://conformance/"
)

// resolveVirtual 解析语料内部的相对导入（./x.mjs 与 ../_lib/x.mjs 按导入者的目录定位）。
//
// 手写而不用 path.Join：模块名带 plugin:// 协议头，path.Join/Clean 会把双斜杠压成
// 单斜杠，虚拟模块名就对不上语料表了。
func resolveVirtual(virtual map[string]string, _ map[string]*moejs.Module, from moejs.Referrer, spec string) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		if _, ok := virtual[spec]; ok {
			return spec
		}
		return ""
	}
	base := ""
	if from != nil {
		base = from.Name()
	}
	name := joinVirtual(base, spec)
	if _, ok := virtual[name]; ok {
		return name
	}
	return ""
}

// joinVirtual 按 ESM 的相对解析规则拼模块名（保留协议头的双斜杠）。
func joinVirtual(base, spec string) string {
	dir := base
	if i := strings.LastIndex(base, "/"); i >= 0 {
		dir = base[:i]
	}
	parts := append([]string{}, strings.Split(dir, "/")...)
	for _, seg := range strings.Split(spec, "/") {
		switch seg {
		case ".", "":
			continue
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, seg)
		}
	}
	return strings.Join(parts, "/")
}
