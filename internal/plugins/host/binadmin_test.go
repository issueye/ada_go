package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/adag/ada-go/internal/binpaths"
	"github.com/adag/ada-go/internal/plugins"
)

// TestEnvAdminListsServicePrograms：ctx.env.list() 的 bins 段返回服务程序目录
// 与其中的可执行文件（工作区优先），非可执行文件不列出。
func TestEnvAdminListsServicePrograms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)

	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapEnvAdmin)
	dirs, err := binpaths.Dirs(caps.WorkspaceRoot)
	if err != nil {
		t.Fatalf("binpaths.Dirs: %v", err)
	}
	name := "probe"
	if runtime.GOOS == "windows" {
		name = "probe.exe"
	}
	if err := os.MkdirAll(dirs[0].Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirs[0].Path, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirs[0].Path, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := `
export default function (agent) {
  agent.registerTool({
    name: "bins_probe",
    description: "服务程序探针",
    parameters: { type: "object" },
    access: "read",
    execute: async (args, ctx) => {
      const state = ctx.env.list();
      return { content: [{ type: "text", text: JSON.stringify(state.bins) }] };
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
	var bins struct {
		Paths []struct {
			Scope  string `json:"scope"`
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"paths"`
		Programs []struct {
			Scope    string `json:"scope"`
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			Modified string `json:"modified"`
		} `json:"programs"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &bins); err != nil {
		t.Fatalf("解析 bins: %v\n%s", err, res.Content[0].Text)
	}
	if len(bins.Paths) != 2 || bins.Paths[0].Scope != "workspace" || bins.Paths[1].Scope != "global" {
		t.Fatalf("目录列表不符: %+v", bins.Paths)
	}
	if !bins.Paths[0].Exists {
		t.Fatalf("工作区目录应存在: %+v", bins.Paths[0])
	}
	if len(bins.Programs) != 1 {
		t.Fatalf("只应列出一个可执行文件: %+v", bins.Programs)
	}
	got := bins.Programs[0]
	if got.Name != name || got.Scope != "workspace" || got.Size <= 0 || got.Modified == "" {
		t.Fatalf("程序元数据不符: %+v", got)
	}
}
