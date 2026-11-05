package app_test

// 官方插件的浏览器 bundle 与 client.tsx 必须同步；CI 用 go test 兜住提交漂移。
// 走 pluginbuild --check，避免测试里复制 esbuild 规则。

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOfficialClientBundlesInSync(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := filepath.Dir(wd)
	for _, dir := range []string{
		"plugins/official/ui-hello-tsx",
		"plugins/official/token-usage",
		"plugins/official/provider-admin",
	} {
		cmd := exec.Command("go", "run", "./tools/pluginbuild",
			"-dir", dir,
			"-entry", "client.tsx",
			"-out", "static/client.js",
			"-check",
		)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s 的 client bundle 与 client.tsx 不同步（运行 go run ./tools/pluginbuild -dir %s -entry client.tsx -out static/client.js）：%v\n%s",
				dir, dir, err, out)
		}
	}
}
