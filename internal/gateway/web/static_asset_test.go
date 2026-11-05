package web

// 内嵌静态资源读取：宿主渲染器与浏览器必须拿到同一份 ada-render.js；
// 路径规则与 /static/ 一致，不允许目录穿越。

import "testing"

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
