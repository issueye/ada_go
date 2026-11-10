package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/agent_core/cli"
	"github.com/adag/ada-go/plugins/official"
)

// plugin-helper 的 route 必须把插件骨架安装到全局插件根（~/.adago/plugins），
// 并能被 discovery 发现；生产环境插件统一存放在用户目录，跨工作区共享。
func TestPluginHelperInstallsGlobalPlugin(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workspace := t.TempDir()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	sess, err := cli.Open(ctx, cli.Config{
		Workspace: workspace, DataDir: t.TempDir(), Product: productPath(t),
		Scripts: scripts, Assembler: app.Assembler{}, Fake: true, In: strings.NewReader(""), Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	info, err := sess.CallHTTPAPI(ctx, "plugin-helper", "GET", "/info", nil, "")
	if err != nil {
		t.Fatalf("GET /info: %v", err)
	}
	if !strings.Contains(string(info), "~/.adago/plugins") {
		t.Fatalf("GET /info 缺少全局插件根: %s", info)
	}

	create := json.RawMessage(`{"method":"POST","path":"/create","body":{
	  "id":"demo-plugin","name":"演示插件","description":"由测试创建","toolName":"demo_hello"
	}}`)
	out, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/create", create, "")
	if err != nil {
		t.Fatalf("POST /create: %v", err)
	}
	if !strings.Contains(string(out), `"id":"demo-plugin"`) || !strings.Contains(string(out), `"plugin.json"`) {
		t.Fatalf("创建结果 = %s", out)
	}

	dir := filepath.Join(home, ".adago", "plugins", "demo-plugin")
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
	// 工作区插件目录不再接收新安装。
	if _, err := os.Stat(filepath.Join(workspace, ".adago", "plugins", "demo-plugin")); !os.IsNotExist(err) {
		t.Fatalf("新安装不应写入工作区: %v", err)
	}

	// 同 id 不允许覆盖；非法 id 被宿主拒绝。
	if _, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/create", create, ""); err == nil {
		t.Fatal("重复创建没有报错")
	}
	bad := json.RawMessage(`{"method":"POST","path":"/create","body":{"id":"../escape","name":"bad"}}`)
	if _, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/create", bad, ""); err == nil {
		t.Fatal("非法插件 id 没有被拒绝")
	}

	// 语法错误在静态预检就会失败，且不留下半个插件目录。
	// 语义错误（如 registerTool 缺 execute）不再由预检执行入口工厂来发现，
	// 而是留给启用/重载时的装配（P0：安装与体检不执行插件代码）。
	badEntry := "export default function register(agent) {"
	body, merr := json.Marshal(map[string]any{
		"method": "POST", "path": "/create",
		"body": map[string]any{
			"id": "mcp-shape", "name": "MCP 形状", "description": "错误示例",
			"toolName": "mcp_shape_x", "entry": badEntry,
		},
	})
	if merr != nil {
		t.Fatal(merr)
	}
	if _, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/create", body, ""); err == nil {
		t.Fatal("非法入口没有被拒绝")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".adago", "plugins", "mcp-shape")); !os.IsNotExist(statErr) {
		t.Fatalf("预检失败的骨架不应落盘: %v", statErr)
	}

	// 重载后 discovery 应从全局根发现新插件（默认未启用）。
	if _, err := sess.ReloadPlugins(ctx, ""); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}
	if _, ok := sess.PluginViewDetail(ctx, "demo-plugin"); !ok {
		t.Fatal("重载后没有发现新插件")
	}

	// check_plugin：id 留空返回摘要；带 id 返回诊断报告（来源/启停/预检）。
	listRaw, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/check-plugin", nil, "")
	if err != nil {
		t.Fatalf("check-plugin 摘要: %v", err)
	}
	if !strings.Contains(string(listRaw), "plugin-helper") {
		t.Fatalf("check-plugin 摘要缺少插件: %s", listRaw)
	}
	check, merr := json.Marshal(map[string]any{
		"method": "POST", "path": "/check-plugin", "body": map[string]any{"id": "demo-plugin"},
	})
	if merr != nil {
		t.Fatal(merr)
	}
	reportRaw, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/check-plugin", check, "")
	if err != nil {
		t.Fatalf("check-plugin 报告: %v", err)
	}
	for _, want := range []string{`"found":true`, `~/.adago/plugins`, `启停状态`, `入口加载`} {
		if !strings.Contains(string(reportRaw), want) {
			t.Fatalf("check-plugin 报告缺少 %q: %s", want, reportRaw)
		}
	}
	// 检查未安装的插件：found=false 且有明确的下一步提示。
	missing, merr := json.Marshal(map[string]any{
		"method": "POST", "path": "/check-plugin", "body": map[string]any{"id": "no-such-plugin"},
	})
	if merr != nil {
		t.Fatal(merr)
	}
	missingRaw, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/check-plugin", missing, "")
	if err != nil {
		t.Fatalf("check-plugin 缺失: %v", err)
	}
	if !strings.Contains(string(missingRaw), `"found":false`) {
		t.Fatalf("缺失插件应返回 found=false: %s", missingRaw)
	}

	// remove_plugin：删除安装目录并清理启停条目；重载后插件消失。
	remove, merr := json.Marshal(map[string]any{
		"method": "POST", "path": "/remove-plugin", "body": map[string]any{"id": "demo-plugin"},
	})
	if merr != nil {
		t.Fatal(merr)
	}
	removeRaw, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/remove-plugin", remove, "")
	if err != nil {
		t.Fatalf("remove-plugin: %v", err)
	}
	if !strings.Contains(string(removeRaw), `"source":"global"`) {
		t.Fatalf("移除结果 = %s", removeRaw)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".adago", "plugins", "demo-plugin")); !os.IsNotExist(statErr) {
		t.Fatalf("移除后目录仍存在: %v", statErr)
	}
	if _, err := sess.ReloadPlugins(ctx, ""); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}
	if _, ok := sess.PluginViewDetail(ctx, "demo-plugin"); ok {
		t.Fatal("移除并重载后插件仍在")
	}
	// 启停条目也被清理（demo-plugin 从未显式启停，本就没有条目；这里验证不误伤）。
	if _, ok := sess.PluginViewDetail(ctx, "plugin-helper"); !ok {
		t.Fatal("移除流程不应影响随包插件")
	}
	// 随包插件拒绝移除。
	bundledRemove, merr := json.Marshal(map[string]any{
		"method": "POST", "path": "/remove-plugin", "body": map[string]any{"id": "tools-fs"},
	})
	if merr != nil {
		t.Fatal(merr)
	}
	if _, err := sess.CallHTTPAPI(ctx, "plugin-helper", "POST", "/remove-plugin", bundledRemove, ""); err == nil ||
		!strings.Contains(err.Error(), "随包插件") {
		t.Fatalf("随包插件移除没有被拒绝: %v", err)
	}
}
