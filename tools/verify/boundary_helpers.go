package verify

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/common/proto"
)

// protoMethods / hookPointNames / slotNames 是给文档数量检查用的实际计数来源。
func protoMethods() []string   { return proto.AllMethodNames() }
func hookPointNames() []string { return pluginapi.HookPointNames() }
func slotNames() []string      { return pluginapi.SlotNames() }

// modulePrefix 是本仓库的导入前缀。
const modulePrefix = "github.com/adag/ada-go/"

// checkDependencyDirection 断言 domain 只用标准库、ports 只用标准库与 domain（设计 13 的约束）。
//
// 这条检查是 INV-1「只有一份多轮循环、依赖方向向下」在 M0 可执行的那一半：
// 引擎与适配器层的依赖约束由 M1 的架构测试补强。
func checkDependencyDirection(root string) error {
	rules := []struct {
		pkg      string
		allowSub []string
		why      string
	}{
		{"internal/common/domain", nil, "domain 不 import 任何其它内部包（设计 13）"},
		{"internal/common/ports", []string{"internal/common/domain"}, "ports 只依赖 domain 与标准库（设计 3.3）"},
		// M1-A 起这些包有真实实现，依赖方向必须继续向下（设计 3.3 的图）。
		{"internal/common/toolkit", []string{"internal/common/domain", "internal/common/ports"}, "toolkit 实现 ports.ToolCatalog 等端口，不依赖 engine/gateway（设计 3.3）"},
		{"internal/agent_core/engine", []string{"internal/common/domain", "internal/common/ports"}, "engine 只依赖 ports 与 domain：不知道 moejs、不知道 SQLite、不知道 Gin（设计 3.3）"},
		{"internal/common/diag", nil, "diag 是词汇表叶子，不依赖内部层"},
		{"internal/common/version", []string{"internal/common/domain"}, "version 只依赖 domain"},
		// Gateway 不进入执行内核：它可以复用产品声明（coreapp）与共享契约（common/pluginapi），
		// 但不再依赖插件宿主或其清单包（2026-11-10：契约类型下沉到 common/pluginapi）。
		{"internal/gateway", []string{
			"internal/agent_core/coreapp", "internal/common/domain", "internal/gateway", "internal/common/pluginapi",
			"internal/common/ports", "internal/common/proto", "internal/common/version", "internal/common/workspacepath",
		}, "Gateway 不 import engine/toolkit/adapters/official/插件宿主（设计 3.3）"},
	}
	for _, r := range rules {
		imports, err := packageImports(root, r.pkg)
		if err != nil {
			return err
		}
		if len(imports) == 0 {
			return fmt.Errorf("verify: %s 里没有 Go 文件", r.pkg)
		}
		for _, one := range imports {
			if !strings.HasPrefix(one.imp, modulePrefix) {
				continue // 标准库与外部库
			}
			sub := strings.TrimPrefix(one.imp, modulePrefix)
			if sub == r.pkg {
				continue
			}
			allowed := false
			for _, a := range r.allowSub {
				if sub == a || strings.HasPrefix(sub, a+"/") {
					allowed = true
				}
			}
			if !allowed {
				return fmt.Errorf("verify: 依赖方向越界：%s 的 %s import 了 %s（%s）",
					r.pkg, filepath.Base(one.file), one.imp, r.why)
			}
		}
	}
	// 组级方向：common 不得反向依赖 agent_core / gateway；agent_core 不得依赖 gateway。
	// 组内包级规则允许的东西（例如 globalconfig 曾 import pluginapi.ValidID）会在这里被拦下。
	if err := checkGroupDirection(root); err != nil {
		return err
	}
	return nil
}

// groupRule 声明一个目录组允许依赖的其它组。
var groupRules = []struct {
	group string
	allow []string
	why   string
}{
	{"common", nil, "common 是跨端共享底座，不得反向依赖 agent_core / gateway（设计 13 分层）"},
	{"agent_core", []string{"common"}, "agent_core 只依赖 common；网关不被内核反向依赖（设计 3.3）"},
	{"gateway", []string{"common", "agent_core"}, "gateway 可依赖 common 与 agent_core，但内核不得反向依赖它"},
}

// checkGroupDirection 检查 internal/ 三个目录组之间的依赖方向。
func checkGroupDirection(root string) error {
	for _, r := range groupRules {
		imports, err := packageImports(root, filepath.Join("internal", r.group))
		if err != nil {
			return err
		}
		if len(imports) == 0 {
			return fmt.Errorf("verify: internal/%s 里没有 Go 文件", r.group)
		}
		for _, one := range imports {
			sub := strings.TrimPrefix(one.imp, modulePrefix)
			if !strings.HasPrefix(sub, "internal/") {
				continue
			}
			parts := strings.Split(sub, "/")
			if len(parts) < 2 || parts[1] == r.group || parts[1] == "conformance" {
				continue // 组内自引用
			}
			ok := false
			for _, a := range r.allow {
				if parts[1] == a {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("verify: 组级依赖方向越界：internal/%s 的 %s import 了 internal/%s（%s）",
					r.group, filepath.Base(one.file), parts[1], r.why)
			}
		}

	}
	return nil
}

// importAt 是一条 import 及其来源文件。
type importAt struct {
	imp  string
	file string
}

// packageImports 汇总某目录（含子目录）非测试源码的全部内部 import。
func packageImports(root, pkg string) ([]importAt, error) {
	files, err := goFiles(filepath.Join(root, filepath.FromSlash(pkg)))
	if err != nil {
		return nil, err
	}
	var out []importAt
	for _, f := range files {
		imports, err := fileImports(f)
		if err != nil {
			return nil, err
		}
		for _, imp := range imports {
			out = append(out, importAt{imp: imp, file: f})
		}
	}
	return out, nil
}

// goFiles 列出目录（含子目录）下的非测试 Go 文件。
func goFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// fileImports 解析一个 Go 文件的 import 路径集合。
func fileImports(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("verify: 解析 %s 失败: %w", path, err)
	}
	var out []string
	for _, imp := range file.Imports {
		v := strings.Trim(imp.Path.Value, `"`)
		out = append(out, v)
	}
	return out, nil
}

// emptyBaseBoots 是「不装任何插件时基座可以启动」的可执行断言（1.4 成功标准、1.5.5）。
func emptyBaseBoots(root string) error {
	p, err := coreapp.LoadProduct(filepath.Join(root, "products", "skeleton.agent.json"))
	if err != nil {
		return err
	}
	rt, err := coreapp.Assemble(p)
	if err != nil {
		return fmt.Errorf("verify: 空基座装配失败: %w", err)
	}
	if !rt.IsEmptyBase() || rt.ToolCount != 0 || rt.ProviderCount != 0 || rt.SlotCount != 0 {
		return fmt.Errorf("verify: 空基座不空（tools=%d providers=%d slots=%d），基座偷偷补位了（INV-11）",
			rt.ToolCount, rt.ProviderCount, rt.SlotCount)
	}
	if len(rt.EmptyStateNotice()) == 0 {
		return fmt.Errorf("verify: 空基座没有空态提示，界面会白屏")
	}
	if rt.AllowsSkills() {
		return fmt.Errorf("verify: 空基座不该启用技能装配")
	}
	return nil
}
