package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/plugins/official"
)

// plugin-creator 的 route 必须在工作区生成可被 discovery 加载的插件骨架。
func TestPluginCreatorScaffoldsWorkspacePlugin(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	sess, err := cli.Open(ctx, cli.Config{
		Workspace: workspace, DataDir: t.TempDir(), Product: productPath(t),
		Scripts: scripts, Fake: true, In: strings.NewReader(""), Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	info, err := sess.CallHTTPAPI(ctx, "plugin-creator", "GET", "/info", nil, "")
	if err != nil {
		t.Fatalf("GET /info: %v", err)
	}
	if !strings.Contains(string(info), `".adago/plugins"`) {
		t.Fatalf("GET /info 缺少插件根: %s", info)
	}

	create := json.RawMessage(`{"method":"POST","path":"/create","body":{
	  "id":"demo-plugin","name":"演示插件","description":"由测试创建","toolName":"demo_hello"
	}}`)
	out, err := sess.CallHTTPAPI(ctx, "plugin-creator", "POST", "/create", create, "")
	if err != nil {
		t.Fatalf("POST /create: %v", err)
	}
	if !strings.Contains(string(out), `"id":"demo-plugin"`) || !strings.Contains(string(out), `"plugin.json"`) {
		t.Fatalf("创建结果 = %s", out)
	}

	dir := filepath.Join(workspace, ".adago", "plugins", "demo-plugin")
	manifestRaw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatalf("读取 plugin.json: %v", err)
	}
	var manifest struct {
		ID      string `json:"id"`
		Trust   string `json:"trust"`
		Backend struct {
			Entry string `json:"entry"`
		} `json:"backend"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatalf("plugin.json 不是合法 JSON: %v", err)
	}
	if manifest.ID != "demo-plugin" || manifest.Trust != "third-party" || manifest.Backend.Entry != "index.ts" {
		t.Fatalf("清单字段错误: %+v", manifest)
	}
	entry, err := os.ReadFile(filepath.Join(dir, "index.ts"))
	if err != nil {
		t.Fatalf("读取入口: %v", err)
	}
	if !strings.Contains(string(entry), `name: "demo_hello"`) {
		t.Fatalf("入口没有注册示例工具: %s", entry)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("缺少 README.md: %v", err)
	}

	// 同 id 不允许覆盖。
	if _, err := sess.CallHTTPAPI(ctx, "plugin-creator", "POST", "/create", create, ""); err == nil {
		t.Fatal("重复创建没有报错")
	}
	bad := json.RawMessage(`{"method":"POST","path":"/create","body":{"id":"../escape","name":"bad"}}`)
	if _, err := sess.CallHTTPAPI(ctx, "plugin-creator", "POST", "/create", bad, ""); err == nil {
		t.Fatal("非法插件 id 没有被拒绝")
	}

	// 重载后 discovery 应看到新插件（默认未启用，但会出现在插件目录里）。
	if _, err := sess.ReloadPlugins(ctx, ""); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}
	if _, ok := sess.PluginViewDetail(ctx, "demo-plugin"); !ok {
		t.Fatal("重载后没有发现新插件")
	}
}
