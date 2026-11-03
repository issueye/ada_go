package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

// TestEnvAdminContext 钉住 ctx.env（capability: env.admin）的读写与校验：
// 两层文件、合并视图、删除、非法键拒绝，且写入落在固定路径。
func TestEnvAdminContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)

	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapEnvAdmin)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "env_admin",
    description: "环境变量管理探针",
    parameters: { type: "object" },
    access: "read",
    execute: async (args, ctx) => {
      const before = ctx.env.list();
      ctx.env.set("workspace", "WS_KEY", "ws-value");
      ctx.env.set("global", "G_KEY", "g-value");
      const after = ctx.env.list();
      const removed = ctx.env.remove("workspace", "WS_KEY");
      const final = ctx.env.list();
      let invalid = "";
      try {
        ctx.env.set("global", "BAD-KEY", "x");
      } catch (e) {
        invalid = e && e.message ? String(e.message) : String(e);
      }
      return { content: [{ type: "text", text: JSON.stringify({ before: before.merged, after, removed, finalMerged: final.merged, invalid }) }] };
    },
  });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{
		Targets: Targets{Tools: &recordingTools{}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("结果 = %+v", res.Content)
	}
	var got struct {
		Before map[string]string `json:"before"`
		After  struct {
			Global struct {
				Path string            `json:"path"`
				Vars map[string]string `json:"vars"`
			} `json:"global"`
			Workspace struct {
				Path string            `json:"path"`
				Vars map[string]string `json:"vars"`
			} `json:"workspace"`
			Merged map[string]string `json:"merged"`
		} `json:"after"`
		Removed struct {
			Removed bool `json:"removed"`
		} `json:"removed"`
		FinalMerged map[string]string `json:"finalMerged"`
		Invalid     string            `json:"invalid"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("解析结果: %v\n%s", err, res.Content[0].Text)
	}
	if len(got.Before) != 0 {
		t.Fatalf("初始应没有变量: %v", got.Before)
	}
	if got.After.Workspace.Vars["WS_KEY"] != "ws-value" {
		t.Fatalf("工作区变量未写入: %v", got.After.Workspace.Vars)
	}
	if got.After.Global.Vars["G_KEY"] != "g-value" {
		t.Fatalf("全局变量未写入: %v", got.After.Global.Vars)
	}
	if got.After.Merged["WS_KEY"] != "ws-value" || got.After.Merged["G_KEY"] != "g-value" {
		t.Fatalf("合并视图不符: %v", got.After.Merged)
	}
	if !strings.HasPrefix(got.After.Global.Path, home) {
		t.Fatalf("全局路径应在 ADAGO_HOME 下: %q", got.After.Global.Path)
	}
	if !filepath.IsAbs(got.After.Workspace.Path) {
		t.Fatalf("工作区路径应为绝对路径: %q", got.After.Workspace.Path)
	}
	if !got.Removed.Removed {
		t.Fatal("remove 应报告已删除")
	}
	if _, ok := got.FinalMerged["WS_KEY"]; ok {
		t.Fatalf("删除后仍存在: %v", got.FinalMerged)
	}
	if got.FinalMerged["G_KEY"] != "g-value" {
		t.Fatalf("全局变量被工作区删除影响: %v", got.FinalMerged)
	}
	if !strings.Contains(got.Invalid, "非法") {
		t.Fatalf("非法键应报错: %q", got.Invalid)
	}

	// 磁盘上的文件确实是 envstore 的格式，且全局在工作区之外。
	raw, err := os.ReadFile(filepath.Join(home, "env.json"))
	if err != nil {
		t.Fatalf("全局 env.json 未落盘: %v", err)
	}
	if !strings.Contains(string(raw), `"G_KEY": "g-value"`) {
		t.Fatalf("全局文件内容: %s", raw)
	}
}

// TestEnvAdminReadsLegacyWorkspaceEnv：旧工作区 .adag/env.json 只读兼容；
// 第一次 set 时物化到 .adago/env.json，旧文件保持原样。
func TestEnvAdminReadsLegacyWorkspaceEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)

	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapEnvAdmin)
	legacyDir := filepath.Join(caps.WorkspaceRoot, ".adag")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyBody := `{"version":1,"vars":{"LEGACY":"1"}}`
	legacyPath := filepath.Join(legacyDir, "env.json")
	if err := os.WriteFile(legacyPath, []byte(legacyBody), 0o600); err != nil {
		t.Fatal(err)
	}

	src := `
export default function (agent) {
  agent.registerTool({
    name: "legacy_env",
    description: "旧目录环境变量探针",
    parameters: { type: "object" },
    access: "read",
    execute: async (args, ctx) => {
      const before = ctx.env.list();
      ctx.env.set("workspace", "NEW_KEY", "2");
      const after = ctx.env.list();
      return { content: [{ type: "text", text: JSON.stringify({ before, after }) }] };
    },
  });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{
		Targets: Targets{Tools: &recordingTools{}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var got struct {
		Before struct {
			Workspace struct {
				Path string            `json:"path"`
				Vars map[string]string `json:"vars"`
			} `json:"workspace"`
			Warnings []string `json:"warnings"`
		} `json:"before"`
		After struct {
			Workspace struct {
				Path string            `json:"path"`
				Vars map[string]string `json:"vars"`
			} `json:"workspace"`
		} `json:"after"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("解析结果: %v\n%s", err, res.Content[0].Text)
	}
	if got.Before.Workspace.Vars["LEGACY"] != "1" || got.Before.Workspace.Path != legacyPath {
		t.Fatalf("旧目录读取不符: %+v", got.Before.Workspace)
	}
	if len(got.Before.Warnings) == 0 || !strings.Contains(got.Before.Warnings[0], "旧目录") {
		t.Fatalf("应提示旧目录: %v", got.Before.Warnings)
	}
	if got.After.Workspace.Vars["LEGACY"] != "1" || got.After.Workspace.Vars["NEW_KEY"] != "2" {
		t.Fatalf("set 后应包含新旧变量: %+v", got.After.Workspace)
	}
	if !strings.HasSuffix(got.After.Workspace.Path, filepath.Join(".adago", "env.json")) {
		t.Fatalf("写入后应指向新目录: %q", got.After.Workspace.Path)
	}
	oldRaw, err := os.ReadFile(legacyPath)
	if err != nil || string(oldRaw) != legacyBody {
		t.Fatalf("旧文件不应被改写: %v %q", err, oldRaw)
	}
	newRaw, err := os.ReadFile(filepath.Join(caps.WorkspaceRoot, ".adago", "env.json"))
	if err != nil || !strings.Contains(string(newRaw), `"NEW_KEY": "2"`) {
		t.Fatalf("新文件内容不符: %v %q", err, newRaw)
	}
}
