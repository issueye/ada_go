package nodemods

// 本文件是 nodemods 的自检：每个「声称已实现」的模块都必须能编译，
// 且 path 的两个视图确实是换了默认导出的同一份实现。
//
// 它不启动 Runtime：只做 moejs.Compile（语法与模块形状），因为行为验证在
// host 包的集成测试里做（真实装配、真实 import）。

import (
	"strings"
	"testing"

	"github.com/MoeForge/moejs"
)

// TestEveryModuleCompiles 钉「Source 说 ok 的模块一定能编译」。
func TestEveryModuleCompiles(t *testing.T) {
	for _, name := range Modules() {
		src, ok := Source(name)
		if !ok {
			t.Fatalf("模块 %s 在 Modules() 里但没有源码", name)
		}
		if _, err := moejs.Compile(name, src); err != nil {
			t.Fatalf("模块 %s 编译失败: %v", name, err)
		}
		if !strings.Contains(src, "export default") {
			t.Fatalf("模块 %s 没有默认导出（Node 的模块都有）", name)
		}
	}
}

// TestPathVariants 钉两个视图入口：同一份实现、不同的默认导出与分隔符。
func TestPathVariants(t *testing.T) {
	for _, which := range []string{"posix", "win32"} {
		src, ok := Source("path/" + which)
		if !ok {
			t.Fatalf("path/%s 没有源码", which)
		}
		if !strings.Contains(src, "export default path."+which+";") {
			t.Fatalf("path/%s 的默认导出不是 path.%s", which, which)
		}
		if _, err := moejs.Compile("path/"+which, src); err != nil {
			t.Fatalf("path/%s 编译失败: %v", which, err)
		}
	}
}

// TestUnknownModuleIsHonest 钉「没实现就返回 false」，不冒充兼容。
func TestUnknownModuleIsHonest(t *testing.T) {
	for _, name := range []string{"http", "worker_threads", "zstd", "nope"} {
		if _, ok := Source(name); ok {
			t.Fatalf("未实现的模块 %s 不应有源码", name)
		}
	}
	// node: 前缀与裸名等价。
	withPrefix, ok1 := Source("node:path")
	bare, ok2 := Source("path")
	if !ok1 || !ok2 || withPrefix != bare {
		t.Fatal("node:path 与 path 应解析到同一份源码")
	}
}
