package canvas

// 前端几何纯函数的代理执行：用 node --test 跑 testdata/geometry.test.mjs。
// node 不在 PATH 时跳过（CI 未装 Node 的环境不会因此变红），但本机有 node 就必须通过。

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGeometryPureFunctions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未找到 node，跳过前端几何纯函数测试")
	}
	cmd := exec.Command(node, "--test", filepath.Join("testdata", "geometry.test.mjs"))
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test 失败: %v\n%s", err, out)
	}
}
