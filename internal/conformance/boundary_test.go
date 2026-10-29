package conformance

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// officialPluginIDs 是 v1 随包发布的官方插件 id（development-plan §1.1、02 §5.4 表）。
// 只在测试代码里出现：产品声明与清单夹具决定启用哪些，基座源码不认得它们（1.5.5）。
var officialPluginIDs = []string{
	"provider-openai-chat", "provider-anthropic-messages", "provider-openai-responses",
	"tools-fs", "tools-shell", "tools-git", "tools-plan", "tools-ask", "tools-skill", "tools-subagent",
	"policy-default", "persona-coding", "provider-admin", "token-usage",
	"ui-hello-tsx",
}

// TestConformanceBoundary 是 10 §11.1「边界」分层在 M0 能跑的两条：
//
//  1. internal/（含本包与其子包）没有任何 import 指向 official/；
//  2. internal/ 的非测试源码里不出现官方插件 id —— 官方插件只能由产品声明选进来，
//     基座/测试基础设施里出现 id 就意味着有特判路径（01 §3.3、05 §6.2、1.5.5）。
//
// 这两条与 `adag verify boundary` 互补：verify 面向发布物，本用例面向每次 go test。
func TestConformanceBoundary(t *testing.T) {
	t.Parallel()
	root := repoPath(t, "internal")

	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 并行任务尚未落地的 nodecompat 目录如果不存在就自然跳过。
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	mustNoErr(t, "扫描 internal/", err)
	if len(files) < 20 {
		t.Fatalf("扫描到的源码文件太少（%d），边界检查等于空转", len(files))
	}

	importsChecked := 0
	for _, path := range files {
		path := path
		isTest := strings.HasSuffix(path, "_test.go")
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("解析 %s: %v", path, perr)
		}
		for _, imp := range f.Imports {
			importsChecked++
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "official/") {
				t.Errorf("%s import 了 %q：internal/ 不得依赖 official/（01 §3.3 硬门禁）",
					filepath.ToSlash(path), p)
			}
		}
		if isTest {
			continue // 测试代码允许出现插件 id（清单夹具与产品声明断言本身就是插件清单）。
		}
		raw, rerr := readFile(path)
		if rerr != nil {
			t.Fatalf("读取 %s: %v", path, rerr)
		}
		for _, id := range officialPluginIDs {
			if strings.Contains(raw, id) {
				t.Errorf("%s 的非测试源码里出现官方插件 id %q（基座不得特判，1.5.5）",
					filepath.ToSlash(path), id)
			}
		}
	}
	t.Logf("边界检查：%d 个文件、%d 条 import，无 official/ 依赖、非测试源码无官方插件 id",
		len(files), importsChecked)

	// 本包自己也必须遵守同一规则（防止 conformance 变成「例外通道」）。
	assertNoImport(t, "internal/conformance", []string{"official/"})
	if self := findGoFiles(t, filepath.Join(root, "conformance")); len(self) < 8 {
		t.Errorf("internal/conformance 的源码文件数异常: %d", len(self))
	}
}

// readFile 读取源码原文（字符串层扫描用）。
func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// findGoFiles 列出目录下的 .go 文件（含测试文件）。
func findGoFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			out = append(out, path)
		}
		return nil
	})
	mustNoErr(t, "扫描 "+filepath.ToSlash(dir), err)
	return out
}
