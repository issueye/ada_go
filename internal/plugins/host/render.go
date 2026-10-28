package host

// 本文件是 M4 的 TSX → VNode 渲染宿主（设计 7.3、7.5、9.5）：
//
//	esbuild transform（--jsx=transform --jsx-factory=jsx --jsx-fragment=Fragment）
//	→ moejs 一次性 Runtime（预置全局 jsx / Fragment 与虚拟模块 ssr:components）
//	→ 调模板默认导出函数(props)
//	→ VNode JSON（形态与 spec/vnode.schema.json 对齐，由 gateway/frontend 校验与渲染）
//
// 边界（7.5「Render Runtime 不安装 Node 兼容层」）：
//   - 渲染 Runtime 不装能力（无 fs/fetch/child_process/stdio），只有一个虚拟模块
//     ssr:components（宿主组件桥，真正的 DOM 在 Go 渲染器里实现）；
//   - 模板是单文件 ESM：transform 不打包，相对 import 在链接期失败（6.10 的同款约束）；
//   - 每次渲染一个新 Runtime：失败即丢弃，不跨请求复用脚本值（6.4 的 JSON 边界）；
//   - 超时用 Runtime.Interrupt 投递（脚本不可 catch，INV-8），失败必须返回错误。
//
// 本文件只新增渲染能力，不改变 host.go / load.go / runtime.go 的既有语义。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/evanw/esbuild/pkg/api"
)

// 渲染预算（7.3：插槽 100ms、页面 300ms；数值与 internal/gateway/frontend 的
// SlotTimeoutMS/PageTimeoutMS 一致，由装配方显式传入覆盖，这里的默认只兜底）。
const (
	DefaultRenderSlotTimeout = 100 * time.Millisecond
	DefaultRenderPageTimeout = 300 * time.Millisecond
	// maxRenderVNodeBytes 是模板返回值（VNode JSON）的上限（7.3 x-limits maxOutputBytes）。
	maxRenderVNodeBytes = 256 << 10
	// maxRenderCacheEntries 是编译缓存条目上限：超出后整体清空（模板集合很小）。
	maxRenderCacheEntries = 128
)

// renderComponentsModule 是渲染 Runtime 的虚拟模块名：TSX 通过
// `import { Icon, Todo } from "ssr:components"` 引用宿主组件（7.3）。
const renderComponentsModule = "ssr:components"

// renderPrelude 是渲染 Runtime 的工厂与 Fragment 预置（作为 ssr:components 的
// 模块体执行，先于模板顶层）。VNode 字段名与 spec/vnode.schema.json 一致：
// 元素 {"t","p","c"}、组件 {"t":"$C","n","p","c"}、文本 {"t":"#text","v"}。
const renderPrelude = `// adag 渲染运行时（宿主内置，非插件代码）。
const __adaText = (v) => ({ t: "#text", v: typeof v === "string" ? v : String(v) });
const __adaPush = (out, v) => {
  if (v === null || v === undefined || typeof v === "boolean") return;
  if (Array.isArray(v)) { for (let i = 0; i < v.length; i++) __adaPush(out, v[i]); return; }
  if (typeof v === "object") { out.push(v); return; }
  // 相邻文本合并成一个 #text（Schema 不接受裸字符串子节点，输出也保持紧凑）。
  const text = typeof v === "string" ? v : String(v);
  const last = out.length > 0 ? out[out.length - 1] : null;
  if (last !== null && typeof last === "object" && last.t === "#text") { last.v += text; return; }
  out.push(__adaText(v));
};
const __adaProps = (props) => {
  if (props === null || props === undefined) return {};
  if (typeof props !== "object") throw new TypeError("jsx: props 必须是对象");
  for (const k in props) {
    if (typeof props[k] === "function") {
      throw new TypeError("jsx: 属性 " + k + " 不能是函数（没有事件处理函数，onClick 等直接拒绝，7.3）");
    }
  }
  return props;
};
globalThis.Fragment = Object.freeze({ __ada_fragment: true });
globalThis.jsx = function jsx(tag, props) {
  const out = [];
  for (let i = 2; i < arguments.length; i++) __adaPush(out, arguments[i]);
  if (tag !== null && typeof tag === "object" && tag.__ada_fragment === true) {
    return out; // Fragment：拍平进父节点的子数组（Schema 没有片段节点）
  }
  if (typeof tag === "function") {
    const name = tag.__ada_component;
    if (typeof name !== "string" || name.length === 0) {
      throw new TypeError("jsx: 组件函数缺少 __ada_component 名（只允许从 ssr:components 导入宿主组件）");
    }
    return { t: "$C", n: name, p: __adaProps(props), c: out };
  }
  if (typeof tag !== "string" || tag.length === 0) {
    throw new TypeError("jsx: 标签必须是字符串或 ssr:components 的宿主组件");
  }
  return { t: tag, p: __adaProps(props), c: out };
};
`

// ComponentPair 声明 ssr:components 的一个导出：TSX 名 → Go 组件规范名
// （7.7.3：TodoList 在 TSX 侧叫 Todo）。
type ComponentPair struct {
	Export    string // TSX 侧的导入名
	Canonical string // VNode {"t":"$C","n":…} 里的规范名
}

// ComponentModuleSource 生成 ssr:components 虚拟模块源码：工厂预置 + 组件导出。
// 组件清单的唯一真源在 internal/gateway/frontend（AllowedComponents），由装配方
// 传入，host 不复制一份白名单。
func ComponentModuleSource(pairs []ComponentPair) string {
	b := make([]byte, 0, len(renderPrelude)+len(pairs)*48)
	b = append(b, renderPrelude...)
	b = append(b, "const __adaDef = (name) => { const f = () => {}; Object.defineProperty(f, \"__ada_component\", { value: name }); return f; };\n"...)
	for _, p := range pairs {
		b = append(b, ("export const " + p.Export + " = __adaDef(" + quoteJS(p.Canonical) + ");\n")...)
	}
	return string(b)
}

// quoteJS 用 JSON 编码输出一个 JS 字符串字面量（组件名是受控标识符，仅为稳妥）。
func quoteJS(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// Renderer 把 TSX 模板渲染成 VNode JSON。并发安全：编译缓存有锁，
// Runtime 每次渲染新建、不跨 goroutine。
type Renderer struct {
	mu       sync.Mutex
	cache    map[string]string // 源内容 SHA-256 → transform 后的 ESM
	compMod  string            // ssr:components 模块源码
	slotTime time.Duration
	pageTime time.Duration
}

// RendererOption 定制 Renderer。
type RendererOption func(*Renderer)

// WithComponentsModule 覆盖 ssr:components 的模块源码（通常是
// ComponentModuleSource 的产物）。空串表示只有工厂预置、没有组件导出。
func WithComponentsModule(src string) RendererOption {
	return func(r *Renderer) { r.compMod = src }
}

// WithRenderBudgets 覆盖插槽/页面渲染预算（装配方从 frontend 常量传入）。
func WithRenderBudgets(slot, page time.Duration) RendererOption {
	return func(r *Renderer) {
		if slot > 0 {
			r.slotTime = slot
		}
		if page > 0 {
			r.pageTime = page
		}
	}
}

// NewRenderer 构造渲染器。
func NewRenderer(opts ...RendererOption) *Renderer {
	r := &Renderer{
		cache:    map[string]string{},
		compMod:  renderPrelude,
		slotTime: DefaultRenderSlotTimeout,
		pageTime: DefaultRenderPageTimeout,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RenderTemplate 渲染一个插槽模板（默认导出函数；预算 = 插槽档）。
// 返回值是 VNode JSON（可能是字面量 null，表示该插槽留空）。
func (r *Renderer) RenderTemplate(src string, props json.RawMessage) (json.RawMessage, error) {
	return r.render(src, props, r.slotTime)
}

// RenderPage 渲染一个页面模板（预算 = 页面档）。
func (r *Renderer) RenderPage(src string, props json.RawMessage) (json.RawMessage, error) {
	return r.render(src, props, r.pageTime)
}

// render 的一次完整执行：transform → Runtime → eval → 调用 → JSON。
//
// 预算只管「模板函数这次调用」（7.3 的插槽/页面档）；模块编译与加载是宿主装配
// 步骤，另有独立的装配预算（DefaultCallTimeout），避免把进程首次建 Runtime 的
// 固定成本算进插件预算。两者都用 Runtime.Interrupt 投递，脚本不可 catch（INV-8）。
func (r *Renderer) render(src string, props json.RawMessage, budget time.Duration) (json.RawMessage, error) {
	if len(src) == 0 {
		return nil, errors.New("host: 渲染模板源码为空")
	}
	js, err := r.compile(src)
	if err != nil {
		return nil, err
	}
	// 顶层 side-effect import 保证工厂/Fragment 先于模板执行（模块图先求值依赖）。
	entry := "import " + quoteJS(renderComponentsModule) + ";\n" + js

	rt := moejs.NewRuntime(moejs.Options{
		MemoryLimit: MemoryLimit, // 与插件 Runtime 同一份 64MB 上限（7.5）
		Locale:      "en-US",
	})
	defer rt.ClearInterrupt()

	// 装配段：编译、链接与模块求值。
	setup := time.AfterFunc(DefaultCallTimeout, func() { rt.Interrupt(errRenderTimeout) })
	linked, err := r.link(entry)
	if err == nil {
		err = rt.Load(linked)
	}
	setup.Stop()
	if err != nil {
		return nil, r.renderError("模块加载", err)
	}
	fn, ok := rt.Export("default")
	if !ok || !engine.IsCallable(fn) {
		return nil, errors.New("host: 渲染模板缺少 `export default function (props) {}`（7.3）")
	}

	// 调用段：这次模板函数的执行按插槽/页面预算计。
	started := time.Now()
	watchdog := time.AfterFunc(budget, func() { rt.Interrupt(errRenderTimeout) })
	defer watchdog.Stop()

	arg := moejs.Undefined()
	if len(props) > 0 {
		v, perr := rt.ParseJSON(props)
		if perr != nil {
			return nil, fmt.Errorf("host: 渲染 props 不是合法 JSON: %w", perr)
		}
		arg = v
	}
	res, err := rt.CallFunction(fn, moejs.Undefined(), arg)
	if err != nil {
		if isRenderTimeout(err) {
			return nil, fmt.Errorf("host: 渲染模板执行超出预算 %v（实际墙钟 %v，7.3 超时上限）", budget, time.Since(started))
		}
		return nil, r.renderError("模板执行", err)
	}
	// 渲染必须同步：返回 Promise 意味着模板有副作用或 await（7.3 刻意不支持）。
	if res.IsObject() {
		r0 := rt.Realm()
		if then, terr := r0.GetMethod(res, r0.KeyFromGoString("then")); terr == nil && engine.IsCallable(then) {
			return nil, errors.New("host: 渲染模板返回了 Promise（渲染必须同步，7.3）")
		}
	}
	raw, err := rt.AppendJSON(nil, res)
	if err != nil {
		return nil, fmt.Errorf("host: 渲染结果不可 JSON 序列化: %w", err)
	}
	if len(raw) > maxRenderVNodeBytes {
		return nil, fmt.Errorf("host: 渲染结果 %d 字节，超过 VNode 输出上限 %d（7.3）", len(raw), maxRenderVNodeBytes)
	}
	return json.RawMessage(raw), nil
}

// errRenderTimeout 是渲染看门狗投递给 Runtime.Interrupt 的哨兵（脚本不可 catch）。
var errRenderTimeout = errors.New("host: 渲染超出预算")

// isRenderTimeout 报告 err 是否为看门狗投递的中断（脚本不可 catch，INV-8）。
func isRenderTimeout(err error) bool {
	var ie *moejs.InterruptedError
	if errors.As(err, &ie) && ie != nil {
		if reason, ok := ie.Value.(error); ok {
			return errors.Is(reason, errRenderTimeout)
		}
	}
	return false
}

// renderError 把 Runtime 的失败分类成人话：中断（超时）单独点名。
func (r *Renderer) renderError(stage string, err error) error {
	if isRenderTimeout(err) {
		return fmt.Errorf("host: 渲染%s超出预算（7.3 超时上限）", stage)
	}
	var exc *moejs.Exception
	if errors.As(err, &exc) {
		return fmt.Errorf("host: 渲染%s抛出 %s: %s", stage, exc.Name(), exc.Message())
	}
	return fmt.Errorf("host: 渲染%s失败: %w", stage, err)
}

// link 编译并链接入口（含 ssr:components 依赖）。相对/未知 import 在这里失败。
func (r *Renderer) link(entrySrc string) (*moejs.Module, error) {
	entry, err := moejs.Compile("adag:render-entry", entrySrc)
	if err != nil {
		return nil, fmt.Errorf("host: 渲染模块编译失败: %w", err)
	}
	comp, err := moejs.Compile(renderComponentsModule, r.compMod)
	if err != nil {
		return nil, fmt.Errorf("host: 渲染组件桥编译失败: %w", err)
	}
	modules := map[string]*moejs.Module{renderComponentsModule: comp}
	resolver := moejs.Resolver(func(_ moejs.Referrer, spec string) (*moejs.Module, error) {
		if m, ok := modules[spec]; ok {
			return m, nil
		}
		return nil, fmt.Errorf("host: 渲染模板 import %q 不可用：渲染模板是单文件 ESM，宿主组件只能来自 %s（7.3、6.10）", spec, renderComponentsModule)
	})
	linked, lerr := moejs.Link(entry, resolver)
	if lerr != nil {
		return nil, fmt.Errorf("host: 渲染模块链接失败: %w", lerr)
	}
	return linked, nil
}

// compile 做 esbuild transform 并按内容哈希缓存（7.5：官方产物构建期已定，
// 第三方在加载/首次渲染时编译，内容不变就不再付编译成本）。
func (r *Renderer) compile(src string) (string, error) {
	sum := sha256.Sum256([]byte(src))
	key := hex.EncodeToString(sum[:])
	r.mu.Lock()
	if js, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return js, nil
	}
	r.mu.Unlock()

	result := api.Transform(src, api.TransformOptions{
		Loader:           api.LoaderTSX,
		JSX:              api.JSXTransform,
		JSXFactory:       "jsx",
		JSXFragment:      "Fragment",
		Format:           api.FormatESModule,
		Target:           api.ES2022,
		Sourcefile:       "template.tsx",
		MinifyWhitespace: true,
	})
	if len(result.Errors) > 0 {
		e := result.Errors[0]
		if e.Location != nil {
			return "", fmt.Errorf("host: TSX 编译失败: %s（%s:%d）", e.Text, e.Location.File, e.Location.Line)
		}
		return "", fmt.Errorf("host: TSX 编译失败: %s", e.Text)
	}
	if len(result.Code) == 0 {
		return "", errors.New("host: TSX 编译没有产物")
	}
	js := string(result.Code)

	r.mu.Lock()
	if len(r.cache) >= maxRenderCacheEntries {
		r.cache = map[string]string{}
	}
	r.cache[key] = js
	r.mu.Unlock()
	return js, nil
}
