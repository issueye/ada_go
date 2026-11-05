package host

// P0 同构渲染探针：证明 ada-render.js 可以在 moejs 里、没有 DOM 的情况下，
// 用同一份组件定义输出 VNode JSON，并且事件处理函数不会进入线格式。
//
// 这条路径是插件 SSR + 客户端 hydrate 框架的前提（docs/20）：
//   - 服务端用 renderToJSON 得到 VNode → Go 校验/转义；
//   - 浏览器用同一份组件定义 hydrate。
//
// 测试直接拼接引擎源码与组件入口：引擎是 UMD，加载后会设置 globalThis.AdaRender；
// 拼接后的模块没有 document，如果引擎依赖 DOM，这里会直接失败。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moejs"
)

func TestIsoRenderToJSONWithoutDOM(t *testing.T) {
	enginePath := filepath.Join("..", "..", "gateway", "web", "static", "ada-render.js")
	engine, err := os.ReadFile(enginePath)
	if err != nil {
		t.Fatalf("读取渲染引擎失败: %v", err)
	}

	entry := string(engine) + `
const def = {
  props: { label: { default: "" } },
  data() { return { n: 1 }; },
  methods: { inc() { this.n += 1; } },
  render(h) {
    return h("div", { class: "iso", onClick: this.inc }, [
      h("span", null, this.label + ":" + this.n),
      h("button", { type: "button", "data-action": "inc" }, "add")
    ]);
  }
};
export const out = globalThis.AdaRender.renderToJSON(def, { label: "a" });
`

	rt := moejs.NewRuntime(moejs.Options{MemoryLimit: 64 << 20, Locale: "en-US"})
	mod, err := moejs.Compile("iso-render-test", entry)
	if err != nil {
		t.Fatalf("编译同构模块失败: %v", err)
	}
	if err := rt.Load(mod); err != nil {
		t.Fatalf("加载同构模块失败: %v", err)
	}
	v, ok := rt.Export("out")
	if !ok {
		t.Fatal("同构模块没有导出 out")
	}
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		t.Fatalf("VNode 不能序列化为 JSON: %v", err)
	}

	var node struct {
		T string                     `json:"t"`
		P map[string]json.RawMessage `json:"p"`
		C []json.RawMessage          `json:"c"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatalf("VNode 不是合法 JSON: %v", err)
	}
	if node.T != "div" {
		t.Fatalf("根节点 = %q，期望 div", node.T)
	}
	if _, ok := node.P["class"]; !ok {
		t.Fatalf("根节点缺少 class：%s", raw)
	}
	if _, ok := node.P["onClick"]; ok {
		t.Fatalf("事件处理函数进入了线格式：%s", raw)
	}
	if strings.Contains(string(raw), "onClick") || strings.Contains(string(raw), "function") {
		t.Fatalf("线格式里出现了函数痕迹：%s", raw)
	}
	if !strings.Contains(string(raw), `"v":"a:1"`) {
		t.Fatalf("组件状态没有渲染进文本节点：%s", raw)
	}
}
