package web

// 内嵌静态资源的基线测试：保留的脚本全部满足 CSP
// （不能出现 eval() / new Function()）。SSR 渲染器所需的静态读取出口
// （web.StaticAsset）已随 SSR 移除。

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// TestStaticAssetsHonorCSP 扫描内嵌静态资源里的全部脚本：CSP script-src 'self'
// 不允许 eval/new Function（ada-runtime 与保留的壳脚本都要守住这条）。
func TestStaticAssetsHonorCSP(t *testing.T) {
	danger := regexp.MustCompile(`\beval\s*\(|new\s+Function\s*\(`)
	found := 0
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".mjs") {
			return nil
		}
		data, readErr := staticFS.ReadFile(p)
		if readErr != nil {
			t.Fatalf("读取 %s: %v", p, readErr)
		}
		found++
		if loc := danger.FindIndex(data); loc != nil {
			t.Fatalf("%s 含 CSP 禁止的 eval/new Function（字节 %d 附近）", p, loc[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("内嵌静态资源里没有任何脚本，扫描没有覆盖面")
	}
}
