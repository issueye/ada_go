package app_test

// 环境变量管理页的数据面：env-manager 的 route 走 capability env.admin。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins/host"
)

type envManagerState struct {
	Global struct {
		Path string            `json:"path"`
		Vars map[string]string `json:"vars"`
	} `json:"global"`
	Workspace struct {
		Path string            `json:"path"`
		Vars map[string]string `json:"vars"`
	} `json:"workspace"`
	Merged map[string]string `json:"merged"`
	Bins   struct {
		Paths []struct {
			Scope  string `json:"scope"`
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"paths"`
		Programs []struct {
			Scope string `json:"scope"`
			Name  string `json:"name"`
			Size  int64  `json:"size"`
		} `json:"programs"`
	} `json:"bins"`
}

func TestEnvManagerRoutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	ws := tempDir(t)
	// 工作区服务程序：env-manager 的 /state 应列出它，并把它前置到 PATH。
	binDir := filepath.Join(ws, ".adago", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	toolName := "probe"
	if runtime.GOOS == "windows" {
		toolName = "probe.exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, toolName), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm := assemble(t, testProduct(t, "env-manager"), hc, nil)
	ctx := context.Background()

	call := func(method, path string, body map[string]any) (json.RawMessage, error) {
		t.Helper()
		if body == nil {
			return asm.CallHTTPAPI(ctx, "env-manager", method, path, nil, "")
		}
		payload, err := json.Marshal(map[string]any{"method": method, "path": path, "body": body})
		if err != nil {
			t.Fatal(err)
		}
		return asm.CallHTTPAPI(ctx, "env-manager", method, path, payload, "")
	}
	loadState := func() envManagerState {
		t.Helper()
		out, err := call("GET", "/state", nil)
		if err != nil {
			t.Fatalf("GET /state: %v", err)
		}
		var state envManagerState
		if err := json.Unmarshal(out, &state); err != nil {
			t.Fatalf("解析 /state: %v\n%s", err, out)
		}
		return state
	}

	// 初始状态：两层路径 + 空变量。
	state := loadState()
	if len(state.Global.Vars) != 0 || len(state.Workspace.Vars) != 0 {
		t.Fatalf("初始应为空: %+v", state)
	}
	if !strings.HasPrefix(state.Global.Path, home) || !strings.HasPrefix(state.Workspace.Path, ws) {
		t.Fatalf("路径不符: global=%q workspace=%q", state.Global.Path, state.Workspace.Path)
	}
	if len(state.Bins.Paths) != 2 || state.Bins.Paths[0].Scope != "workspace" || !state.Bins.Paths[0].Exists {
		t.Fatalf("服务程序目录不符: %+v", state.Bins.Paths)
	}
	if state.Bins.Paths[0].Path != binDir {
		t.Fatalf("工作区服务程序目录 = %q，期望 %q", state.Bins.Paths[0].Path, binDir)
	}
	if len(state.Bins.Programs) != 1 || state.Bins.Programs[0].Name != toolName || state.Bins.Programs[0].Scope != "workspace" {
		t.Fatalf("服务程序列表不符: %+v", state.Bins.Programs)
	}

	// 写工作区与全局。
	if _, err := call("POST", "/set", map[string]any{"scope": "workspace", "key": "WS_TOKEN", "value": "w1"}); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	if _, err := call("POST", "/set", map[string]any{"scope": "global", "key": "G_TOKEN", "value": "g1"}); err != nil {
		t.Fatalf("set global: %v", err)
	}
	wsRaw, err := os.ReadFile(filepath.Join(ws, ".adago", "env.json"))
	if err != nil || !strings.Contains(string(wsRaw), `"WS_TOKEN": "w1"`) {
		t.Fatalf("工作区文件: %v %q", err, wsRaw)
	}
	globalRaw, err := os.ReadFile(filepath.Join(home, "env.json"))
	if err != nil || !strings.Contains(string(globalRaw), `"G_TOKEN": "g1"`) {
		t.Fatalf("全局文件: %v %q", err, globalRaw)
	}

	// 合并视图与工作区覆盖。
	if _, err := call("POST", "/set", map[string]any{"scope": "global", "key": "OVERRIDE", "value": "from-global"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("POST", "/set", map[string]any{"scope": "workspace", "key": "OVERRIDE", "value": "from-ws"}); err != nil {
		t.Fatal(err)
	}
	state = loadState()
	if state.Merged["WS_TOKEN"] != "w1" || state.Merged["G_TOKEN"] != "g1" || state.Merged["OVERRIDE"] != "from-ws" {
		t.Fatalf("合并视图不符: %+v", state.Merged)
	}

	// 删除工作区变量不影响全局。
	if _, err := call("POST", "/remove", map[string]any{"scope": "workspace", "key": "WS_TOKEN"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	state = loadState()
	if _, ok := state.Workspace.Vars["WS_TOKEN"]; ok {
		t.Fatalf("删除后仍在工作区: %+v", state.Workspace.Vars)
	}
	if state.Global.Vars["G_TOKEN"] != "g1" {
		t.Fatalf("全局变量被误删: %+v", state.Global.Vars)
	}

	// 非法 scope / 非法键被拒绝。
	if _, err := call("POST", "/set", map[string]any{"scope": "nope", "key": "A", "value": "1"}); err == nil {
		t.Fatal("非法 scope 应报错")
	}
	if _, err := call("POST", "/set", map[string]any{"scope": "global", "key": "BAD-KEY", "value": "1"}); err == nil {
		t.Fatal("非法键应报错")
	}
}
