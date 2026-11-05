package web

// 内嵌静态资源读取：宿主渲染器与浏览器必须拿到同一份 ada-render.js；
// 路径规则与 /static/ 一致，不允许目录穿越。

import (
	"regexp"
	"testing"
)

func TestStaticAssetAdaRender(t *testing.T) {
	data, ok := StaticAsset("ada-render.js")
	if !ok || len(data) == 0 {
		t.Fatal("ada-render.js 应可从内嵌静态资源读取")
	}
	if _, ok := StaticAsset("../server.go"); ok {
		t.Fatal("StaticAsset 不应接受目录穿越路径")
	}
	if _, ok := StaticAsset("static/ada-render.js"); ok {
		t.Fatal("StaticAsset 只接受 /static/ 之后的规范名")
	}
}

// 岛屿运行时的静态资源必须满足 CSP：不能出现 eval() / new Function()。
func TestIslandAssetsHonorCSP(t *testing.T) {
	danger := regexp.MustCompile(`\beval\s*\(|new\s+Function\s*\(`)
	for _, name := range []string{"ada-render.js", "ada-ui.js", "ada-ui-iso.js", "plugin-islands.js"} {
		data, ok := StaticAsset(name)
		if !ok || len(data) == 0 {
			t.Fatalf("缺少静态资源 %s", name)
		}
		if loc := danger.FindIndex(data); loc != nil {
			t.Fatalf("%s 含 CSP 禁止的 eval/new Function（字节 %d 附近）", name, loc[0])
		}
	}
}
