// module_graph_test.go —— 画布前端 ES Modules 的 import/export 契约门禁。
//
// 起因：一次拆分/重构后 `dock.js` 调用了 `toggleTreeRow` 却没 import、`nodes.js` 调用
// `applyTransform`/`zoomAt` 却没 import，表现分别是「项目树点不开」「画布拖不动、滚轮不缩放」——
// 浏览器只在运行时抛 ReferenceError，Go 侧测试与 `go vet` 都看不见。
// 这里把「导入必须存在、模块必须可达」变成机器门禁：
//  1. 每个 named import 都能在目标模块的 export 里找到（允许 `a as b`）；
//  2. 从入口 main.js 出发可达 js/ 下所有模块（没有孤儿脚本）。
//
// 纯正则实现，不依赖 node：CI 没装 Node 也能跑。
package canvas

import (
	"path"
	"regexp"
	"strings"
	"testing"
)

type jsImport struct {
	spec   string   // 原始说明符，如 "./view.js"、"../inspect.js"
	named  []string // 具名导入的「源名字」（`a as b` 记 a：要校验的是对面导出了 a）
	target string   // 解析后的模块名（相对 static/）；越界或不存在时为空
}

type jsModule struct {
	name    string
	src     string // 去掉注释后的源码
	exports map[string]bool
	imports []jsImport
}

var (
	reExportList   = regexp.MustCompile(`(?s)export\s*\{([^}]*)\}`)
	reExportFunc   = regexp.MustCompile(`export\s+(?:async\s+)?(?:function|class)\s+([A-Za-z_$][\w$]*)`)
	reExportVar    = regexp.MustCompile(`export\s+(?:const|let)\s+([^\n;]+)`)
	reImportClause = regexp.MustCompile(`(?m)import\s+([^"';]+?)\s+from\s+["']([^"']+)["']`)
	reImportSide   = regexp.MustCompile(`(?m)^\s*import\s+["']([^"']+)["']`)
	reNamedBlock   = regexp.MustCompile(`\{([^}]*)\}`)
	reIdentStart   = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
)

// exportVarNames 解析 `export const a = 1, b = f();` 这类合并声明里的每个名字。
// 逗号切分对「对象字面量里的逗号」是错的，但那些片段的首词通常是 `key: value` 形式，
// 通不过标识符校验，会被自然丢弃 —— 只可能少认名字，不会凭空多出导出。
func exportVarNames(decl string) []string {
	var out []string
	for _, part := range strings.Split(decl, ",") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "="); i >= 0 {
			part = strings.TrimSpace(part[:i])
		}
		if reIdentStart.MatchString(part) {
			out = append(out, part)
		}
	}
	return out
}

func parseJSModule(name, raw string) *jsModule {
	src := stripLineComments(raw)
	m := &jsModule{name: name, src: src, exports: map[string]bool{}}
	for _, mm := range reExportList.FindAllStringSubmatch(src, -1) {
		for _, part := range strings.Split(mm[1], ",") {
			fields := strings.Fields(strings.TrimSpace(part))
			switch {
			case len(fields) == 1:
				m.exports[fields[0]] = true
			case len(fields) == 3 && fields[1] == "as": // a as b：对外是 b
				m.exports[fields[2]] = true
			}
		}
	}
	for _, mm := range reExportFunc.FindAllStringSubmatch(src, -1) {
		m.exports[mm[1]] = true
	}
	for _, mm := range reExportVar.FindAllStringSubmatch(src, -1) {
		for _, n := range exportVarNames(mm[1]) {
			m.exports[n] = true
		}
	}
	for _, mm := range reImportClause.FindAllStringSubmatch(src, -1) {
		imp := jsImport{spec: mm[2]}
		if block := reNamedBlock.FindStringSubmatch(mm[1]); block != nil {
			for _, part := range strings.Split(block[1], ",") {
				fields := strings.Fields(strings.TrimSpace(part))
				switch {
				case len(fields) == 1:
					imp.named = append(imp.named, fields[0])
				case len(fields) == 3 && fields[1] == "as": // a as b：校验 a
					imp.named = append(imp.named, fields[0])
				}
			}
		}
		m.imports = append(m.imports, imp)
	}
	for _, mm := range reImportSide.FindAllStringSubmatch(src, -1) {
		m.imports = append(m.imports, jsImport{spec: mm[1]})
	}
	return m
}

func loadJSModules(t *testing.T) map[string]*jsModule {
	t.Helper()
	mods := map[string]*jsModule{}
	entries, err := staticFS.ReadDir("static/js")
	if err != nil {
		t.Fatalf("枚举 static/js: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		name := "js/" + e.Name()
		mods[name] = parseJSModule(name, readStatic(t, name))
	}
	mods["inspect.js"] = parseJSModule("inspect.js", readStatic(t, "inspect.js"))

	exists := func(name string) bool {
		if _, ok := mods[name]; ok {
			return true
		}
		_, err := staticFS.ReadFile("static/" + name)
		return err == nil
	}
	for _, m := range mods {
		for i := range m.imports {
			imp := &m.imports[i]
			if !strings.HasPrefix(imp.spec, "./") && !strings.HasPrefix(imp.spec, "../") {
				continue // node: 内置模块等，不在画布模块图里
			}
			if rel := path.Clean(path.Join(path.Dir(m.name), imp.spec)); exists(rel) {
				imp.target = rel
			}
		}
	}
	return mods
}

// TestCanvasModuleImportsResolve：具名导入必须是对面真实导出。
func TestCanvasModuleImportsResolve(t *testing.T) {
	mods := loadJSModules(t)
	for name, m := range mods {
		for _, imp := range m.imports {
			if !strings.HasPrefix(imp.spec, "./") && !strings.HasPrefix(imp.spec, "../") {
				continue
			}
			if imp.target == "" {
				t.Errorf("%s: import %q 指向不存在的模块", name, imp.spec)
				continue
			}
			dep := mods[imp.target]
			if dep == nil {
				continue // static/ 下的经典脚本（icons.js）没有 ESM 导出
			}
			for _, n := range imp.named {
				if !dep.exports[n] {
					t.Errorf("%s: import { %s } from %q，但 %s 没有导出 %s —— 运行时会抛 ReferenceError",
						name, n, imp.spec, imp.target, n)
				}
			}
		}
	}
}

// 顶层声明（含函数参数与局部赋值）：用来排除「本模块自己就有这个名字」的情形。
var (
	reLocalDecl = regexp.MustCompile(`(?m)(?:function\s+|class\s+|(?:const|let|var)\s+|^\s*)([A-Za-z_$][\w$]*)\s*(?:\(|=|,|;)`)
	reCallName  = regexp.MustCompile(`(?m)(?:[^.\w$]|^)([A-Za-z_$][\w$]*)\s*\(`)
)

// TestCanvasModuleNoUndefinedCalls 是「用到了但没导入」的兜底扫描：
// 浏览器对这类错误只在运行时抛 ReferenceError（项目树点不开、画布拖不动都是它）。
// 只查从别的模块导出、且在本模块里被当函数调用的名字；本模块任何位置声明过就算安全。
func TestCanvasModuleNoUndefinedCalls(t *testing.T) {
	mods := loadJSModules(t)
	// 跨模块导出的名字 → 导出方
	exported := map[string]bool{}
	for _, m := range mods {
		for n := range m.exports {
			exported[n] = true
		}
	}
	for name, m := range mods {
		src := m.src
		local := map[string]bool{} // 本模块声明/导入过的名字
		for _, imp := range m.imports {
			for _, n := range imp.named {
				local[n] = true
			}
		}
		for _, mm := range reLocalDecl.FindAllStringSubmatch(src, -1) {
			local[mm[1]] = true
		}
		// 简单参数表：`(a, b, c)`、`{a, b}` 解构不在白名单里也无妨 —— 它们极少被当函数调用
		for _, mm := range regexp.MustCompile(`\(([^()]*)\)`).FindAllStringSubmatch(src, -1) {
			for _, part := range strings.Split(mm[1], ",") {
				part = strings.TrimSpace(part)
				part = strings.TrimPrefix(part, "...")
				if i := strings.IndexAny(part, " ="); i >= 0 {
					part = strings.TrimSpace(part[:i])
				}
				if reIdentStart.MatchString(part) {
					local[part] = true
				}
			}
		}
		for _, mm := range reCallName.FindAllStringSubmatch(src, -1) {
			n := mm[1]
			if local[n] || !exported[n] {
				continue
			}
			t.Errorf("%s: 调用了 %s() 但既没 import 也没在本模块声明 —— 浏览器会抛 ReferenceError", name, n)
		}
	}
}

// TestCanvasModulesAllReachable：js/ 下每个模块都必须从入口 main.js 可达。
func TestCanvasModulesAllReachable(t *testing.T) {
	mods := loadJSModules(t)
	if _, ok := mods["js/main.js"]; !ok {
		t.Fatal("缺少入口模块 js/main.js")
	}
	seen := map[string]bool{}
	queue := []string{"js/main.js"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] || mods[name] == nil {
			seen[name] = true
			continue
		}
		seen[name] = true
		for _, imp := range mods[name].imports {
			if imp.target != "" && mods[imp.target] != nil {
				queue = append(queue, imp.target)
			}
		}
	}
	for name := range mods {
		if !seen[name] {
			t.Errorf("模块 %s 没有被入口 import（不会执行）—— 交互/快捷键类模块必须显式挂进 main.js", name)
		}
	}
}

// TestCanvasSpacePanContract：空格临时平移模式的四处契约（状态、键盘、画布、样式）。
func TestCanvasSpacePanContract(t *testing.T) {
	state := stripLineComments(readStatic(t, "js/state.js"))
	nodes := stripLineComments(readStatic(t, "js/nodes.js"))
	keys := stripLineComments(readStatic(t, "js/keyboard.js"))
	css := stripCSSComments(readStatic(t, "canvas.css"))

	checks := []struct {
		ok   bool
		what string
	}{
		{strings.Contains(state, "spacePan: false"), "state.js: S 需要 spacePan 状态位"},
		{strings.Contains(state, "function setSpacePan("), "state.js: 需要 setSpacePan() 作为唯一开关"},
		{strings.Contains(state, `"space-pan"`), "state.js: setSpacePan 要切 body.space-pan（CSS 与拖动都读它）"},
		{strings.Contains(keys, "setSpacePan(true)") && strings.Contains(keys, "setSpacePan(false)"),
			"keyboard.js: 空格 keydown/keyup 必须切 setSpacePan"},
		{strings.Contains(keys, "setSpacePan }") || strings.Contains(keys, "setSpacePan,"),
			"keyboard.js: 需要从 state.js 导入 setSpacePan"},
		{strings.Contains(nodes, "if (!S.spacePan || e.button !== 0 || S.panning) return;"),
			"nodes.js: 空格模式下 pointerdown 必须在捕获阶段接管平移"},
		{strings.Contains(nodes, "if (!S.spacePan) selectNode(n.id)"),
			"nodes.js: 空格模式下点正文不能选中卡片"},
		{strings.Contains(nodes, "if (S.spacePan || S.panning) return;"),
			"nodes.js: 空格模式下不能触发卡片拖动"},
		{strings.Contains(css, "body.space-pan"), "canvas.css: 缺少 spaces 平移模式的光标规则"},
		{strings.Contains(css, "body.panning"), "canvas.css: 拖动中要切「正在拖」的抓手光标"},
		{strings.Contains(css, "body.space-pan .node__body iframe{pointer-events:none}"),
			"canvas.css: 空格模式下 iframe 必须放行事件，否则卡片正文上拖不动"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Error(c.what)
		}
	}
}
