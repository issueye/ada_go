package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/domain"
)

// tryCreatePlugin 在 plugin-helper 实例上直接调用 create_plugin，返回 (输出, 错误)。
func tryCreatePlugin(t *testing.T, asm *app.Assembly, hc host.Context, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range asm.Plugins {
		if inst.ID() != "plugin-helper" {
			continue
		}
		c := inst.Contribution()
		if c == nil {
			continue
		}
		for _, td := range c.Tools {
			if td.Name != "create_plugin" {
				continue
			}
			callCtx := hc
			callCtx.PluginID = inst.ID()
			res, err := inst.CallTool(context.Background(), td.Handler, raw, callCtx)
			if err != nil {
				return "", err
			}
			if len(res.Content) == 0 {
				return "", context.Canceled
			}
			return res.Content[0].Text, nil
		}
	}
	t.Fatalf("plugin-helper 没有注册 create_plugin（现有工具见目录）")
	return "", nil
}

// fakeInstallPlugin 模拟宿主的 plugin.admin 安装桥：把骨架写到临时 home 的
// 全局插件根下，含「不覆盖」语义。断言只关心路径与文件内容，够用即可。
func fakeInstallPlugin(t *testing.T, home string) func(context.Context, host.PluginInstallSpec) (host.PluginInstallResult, error) {
	t.Helper()
	return func(_ context.Context, spec host.PluginInstallSpec) (host.PluginInstallResult, error) {
		id := strings.ToLower(strings.TrimSpace(spec.ID))
		if id == "" {
			return host.PluginInstallResult{}, os.ErrInvalid
		}
		dir := filepath.Join(home, ".adago", "plugins", id)
		if _, err := os.Stat(dir); err == nil {
			return host.PluginInstallResult{}, fmt.Errorf("插件 %s 已存在（%s）", id, dir)
		}
		names := make([]string, 0, len(spec.Files))
		for _, f := range spec.Files {
			target := filepath.Join(dir, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return host.PluginInstallResult{}, err
			}
			if err := os.WriteFile(target, []byte(f.Content), 0o644); err != nil {
				return host.PluginInstallResult{}, err
			}
			names = append(names, f.Path)
		}
		return host.PluginInstallResult{ID: id, Dir: dir, Files: names}, nil
	}
}

// TestPluginHelperAITool 钉 AI 创建插件的链路：模型生成入口源码 → create_plugin
// 校验并经安装桥落盘 → 生成的 manifest/README/入口与声明一致；默认入口生成回显示例。
func TestPluginHelperAITool(t *testing.T) {
	ws := tempDir(t)
	home := t.TempDir()
	hc := host.Context{
		Workspace:     ws,
		Log:           func(string, string, map[string]any) {},
		InstallPlugin: fakeInstallPlugin(t, home),
	}
	p := testProduct(t, "plugin-helper")
	asm := assemble(t, p, hc, map[string]map[string]any{})

	if _, ok := asm.Tools.Lookup(context.Background(), "create_plugin"); !ok {
		t.Fatalf("工具目录里没有 create_plugin（现有：%v）", toolNames(asm))
	}

	// 1) AI 生成入口源码：原样写入，manifest 带声明的能力。
	entry := `// weather —— AI 生成的示例插件。
export default function register(agent) {
  agent.registerTool({
    name: "weather_lookup",
    description: "查询城市天气（示例）",
    parameters: {
      type: "object",
      properties: { city: { type: "string" } },
      required: ["city"],
      additionalProperties: false,
    },
    access: "read",
    async execute(args, ctx) {
      return { content: [{ type: "text", text: "Sunny " + String(args.city || "") }] };
    },
  });
}
`
	out, err := tryCreatePlugin(t, asm, hc, map[string]any{
		"id": "weather-plugin", "name": "天气插件", "description": "AI 生成的示例",
		"tool_name": "weather_lookup", "entry": entry, "capabilities": []string{"fs.read"},
	})
	if err != nil {
		t.Fatalf("create_plugin: %v", err)
	}
	for _, want := range []string{
		filepath.Join(".adago", "plugins", "weather-plugin"),
		"重载", "启用",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("工具输出缺少 %q:\n%s", want, out)
		}
	}
	dir := filepath.Join(home, ".adago", "plugins", "weather-plugin")
	manifestRaw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatalf("读取 plugin.json: %v", err)
	}
	var manifest struct {
		Trust      string   `json:"trust"`
		Backend    struct{} `json:"backend"`
		CapsParser struct {
			Capabilities []string `json:"capabilities"`
		} `json:"-"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatalf("plugin.json 不是合法 JSON: %v", err)
	}
	if manifest.Trust != "third-party" || !strings.Contains(string(manifestRaw), `"capabilities"`) ||
		!strings.Contains(string(manifestRaw), "fs.read") {
		t.Fatalf("清单没有按声明写能力: %s", manifestRaw)
	}
	entryRaw, err := os.ReadFile(filepath.Join(dir, "index.ts"))
	if err != nil {
		t.Fatalf("读取入口: %v", err)
	}
	if string(entryRaw) != entry {
		t.Fatal("AI 生成的入口没有被原样写入")
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || !strings.Contains(string(readme), "AI 生成") {
		t.Fatalf("README 未说明 AI 生成来源: %v", err)
	}

	// 2) 不传 entry：生成回显示例工具（默认名 <id>_hello）。
	out, err = tryCreatePlugin(t, asm, hc, map[string]any{"id": "hello-ai"})
	if err != nil {
		t.Fatalf("create_plugin(默认): %v", err)
	}
	helloEntry, err := os.ReadFile(filepath.Join(home, ".adago", "plugins", "hello-ai", "index.ts"))
	if err != nil {
		t.Fatalf("读取示例入口: %v", err)
	}
	if !strings.Contains(string(helloEntry), `name: "hello_ai_hello"`) {
		t.Fatalf("示例入口没有默认工具名: %s", helloEntry)
	}
	if !strings.Contains(out, "回显") && !strings.Contains(out, "hello_ai_hello") {
		t.Fatalf("默认创建输出缺少示例提示: %s", out)
	}

	// 3) 非法输入必须被拒绝，而不是落盘坏插件。
	// 注意：宿主对连续 3 次 tool reject 会自动禁用插件实例（06 §6.4），
	// 所以拒绝用例拆到新装配的实例上执行，避免被这个保护机制截断断言。
	if _, err := tryCreatePlugin(t, asm, hc, map[string]any{"id": "weather-plugin"}); err == nil ||
		!strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重复创建: err = %v, want 含 已存在", err)
	}
	rejectAsm := assemble(t, p, hc, map[string]map[string]any{})
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"入口缺默认导出", map[string]any{"id": "bad-entry", "entry": "module.exports = {};", "tool_name": "bad_entry_x"}, "export default"},
		{"工具名不一致", map[string]any{"id": "name-mismatch", "entry": entry, "tool_name": "other_tool"}, "other_tool"},
		{"能力越权", map[string]any{"id": "bad-cap", "capabilities": []string{"exec"}}, "exec"},
	}
	for i, tc := range cases {
		target := rejectAsm
		if i >= 2 {
			// 第三个拒绝用例换新实例：前两个失败已让实例进入“连续失败”计数。
			target = assemble(t, p, hc, map[string]map[string]any{})
		}
		_, err := tryCreatePlugin(t, target, hc, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want 含 %q", tc.name, err, tc.want)
		}
	}
	// 拒绝的用例不能留下半个目录。
	if _, err := os.Stat(filepath.Join(home, ".adago", "plugins", "bad-entry")); !os.IsNotExist(err) {
		t.Fatalf("失败的创建不应落盘: %v", err)
	}
}

// create_plugin 的回执是给模型看的文本；确保它是合法的文本内容块（INV-10）。
func TestPluginHelperToolResultShape(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{
		Workspace:     ws,
		Log:           func(string, string, map[string]any) {},
		InstallPlugin: fakeInstallPlugin(t, tempDir(t)),
	}
	p := testProduct(t, "plugin-helper")
	asm := assemble(t, p, hc, map[string]map[string]any{})

	raw, err := json.Marshal(map[string]any{"id": "shape-check"})
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range asm.Plugins {
		if inst.ID() != "plugin-helper" {
			continue
		}
		c := inst.Contribution()
		if c == nil {
			continue
		}
		for _, td := range c.Tools {
			if td.Name != "create_plugin" {
				continue
			}
			res, err := inst.CallTool(context.Background(), td.Handler, raw, hc)
			if err != nil {
				t.Fatalf("create_plugin: %v", err)
			}
			if res.Receipt.Status != domain.ReceiptOK {
				t.Fatalf("回执状态 = %v", res.Receipt.Status)
			}
			if len(res.Content) != 1 || res.Content[0].Type != "text" {
				t.Fatalf("内容形状 = %#v", res.Content)
			}
			return
		}
	}
	t.Fatal("plugin-helper 没有注册 create_plugin")
}
