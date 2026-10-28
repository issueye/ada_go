package verify

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/proto"
)

// protoMethods / hookPointNames / slotNames 是给文档数量检查用的实际计数来源。
func protoMethods() []string   { return proto.AllMethodNames() }
func hookPointNames() []string { return plugins.HookPointNames() }
func slotNames() []string      { return plugins.SlotNames() }

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
		{"internal/domain", nil, "domain 不 import 任何其它内部包（设计 13）"},
		{"internal/ports", []string{"internal/domain"}, "ports 只依赖 domain 与标准库（设计 3.3）"},
		// M1-A 起这些包有真实实现，依赖方向必须继续向下（设计 3.3 的图）。
		{"internal/toolkit", []string{"internal/domain", "internal/ports"}, "toolkit 实现 ports.ToolCatalog 等端口，不依赖 engine/gateway（设计 3.3）"},
		{"internal/engine", []string{"internal/domain", "internal/ports"}, "engine 只依赖 ports 与 domain：不知道 moejs、不知道 SQLite、不知道 Gin（设计 3.3）"},
		{"internal/diag", nil, "diag 是词汇表叶子，不依赖内部层"},
		{"internal/version", []string{"internal/domain"}, "version 只依赖 domain"},
	}
	for _, r := range rules {
		files, err := goFiles(filepath.Join(root, filepath.FromSlash(r.pkg)))
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("verify: %s 里没有 Go 文件", r.pkg)
		}
		for _, f := range files {
			imports, err := fileImports(f)
			if err != nil {
				return err
			}
			for _, imp := range imports {
				if !strings.HasPrefix(imp, modulePrefix) {
					continue // 标准库与外部库
				}
				sub := strings.TrimPrefix(imp, modulePrefix)
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
						r.pkg, filepath.Base(f), imp, r.why)
				}
			}
		}
	}
	return nil
}

// goFiles 列出目录下的非测试 Go 文件。
func goFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("verify: 读取目录失败 %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			out = append(out, filepath.Join(dir, name))
		}
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
