package app_test

// 官方插件的行为验收：全部走真实装配（产品声明 × 嵌入包 × 宿主），
// 没有 Go 侧的「测试专用注册入口」——这正是 M1-F 要证明的那条约束。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/ports"
)

// TestToolsFS 钉 tools-fs 的五个工具在真实 fsGuard 下的行为。
func TestToolsFS(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm := assemble(t, testProduct(t, "tools-fs", "policy-default"), hc, nil)

	if _, ok := asm.Tools.Lookup(context.Background(), "read_file"); !ok {
		t.Fatalf("缺少 read_file，现有 %v", toolNames(asm))
	}

	// 写 → 读
	if text, res := callTool(t, asm, hc, "write_file", map[string]any{
		"path": "/workspace/note.txt", "content": "alpha\nbeta\ngamma\n",
	}); !strings.Contains(text, "已写入") || res.Details["bytes"].(float64) != 17 {
		t.Fatalf("write_file = %q / %#v", text, res.Details)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "note.txt")); string(got) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("落盘内容 = %q", got)
	}
	if text, res := callTool(t, asm, hc, "read_file", map[string]any{"path": "/workspace/note.txt"}); text != "alpha\nbeta\ngamma\n" {
		t.Fatalf("read_file = %q（details=%#v）", text, res.Details)
	}

	// 行偏移：只读第 2 行
	if text, _ := callTool(t, asm, hc, "read_file", map[string]any{
		"path": "note.txt", "offset": 1, "limit": 1,
	}); text != "beta" {
		t.Fatalf("read_file(offset=1,limit=1) = %q", text)
	}

	// 编辑：唯一匹配
	if text, res := callTool(t, asm, hc, "edit_file", map[string]any{
		"path": "/workspace/note.txt", "old": "beta", "new": "BETA",
	}); !strings.Contains(text, "替换 1 处") || res.Details["replaced"].(float64) != 1 {
		t.Fatalf("edit_file = %q / %#v", text, res.Details)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "note.txt")); !strings.Contains(string(got), "BETA") {
		t.Fatalf("编辑后内容 = %q", got)
	}

	// 列目录与搜索
	if text, _ := callTool(t, asm, hc, "list_files", map[string]any{"path": "/workspace"}); !strings.Contains(text, "file /workspace/note.txt") {
		t.Fatalf("list_files = %q", text)
	}
	if text, res := callTool(t, asm, hc, "search_files", map[string]any{"pattern": "BETA"}); !strings.Contains(text, "note.txt:2: BETA") ||
		res.Details["matches"].(float64) != 1 {
		t.Fatalf("search_files = %q / %#v", text, res.Details)
	}

	// 越界路径必须被拒（插件先判、宿主再判一次）
	inst, ok := asm.Plugin("tools-fs")
	if !ok {
		t.Fatal("没有 tools-fs 实例")
	}
	var readHandler host.HandlerRef
	for _, td := range inst.Contribution().Tools {
		if td.Name == "read_file" {
			readHandler = td.Handler
		}
	}
	_, err := inst.CallTool(context.Background(), readHandler,
		json.RawMessage(`{"path":"/etc/passwd"}`), hc)
	if err == nil {
		t.Fatal("读工作区外的绝对路径必须失败")
	}
	if !strings.Contains(err.Error(), "不在工作区") && !strings.Contains(err.Error(), "越出") {
		t.Fatalf("错误应说明越界：%v", err)
	}
}

// TestToolsShell 钉 run_command：无 shell、非零退出码不抛错、参数校验。
func TestToolsShell(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm := assemble(t, testProduct(t, "tools-shell", "policy-default"), hc, nil)

	cmdName, argv := "cmd.exe", []string{"/c", "echo", "spike-out"}
	if os.PathSeparator != '\\' {
		cmdName, argv = "/bin/echo", []string{"spike-out"}
	}
	text, res := callTool(t, asm, hc, "run_command", map[string]any{"command": cmdName, "args": argv})
	if !strings.HasPrefix(text, "exit 0") || !strings.Contains(text, "spike-out") {
		t.Fatalf("run_command = %q", text)
	}
	if res.Details["code"].(float64) != 0 {
		t.Fatalf("details.code = %v", res.Details["code"])
	}

	// 把整条命令行塞进 command：必须拒绝（不经过 shell）
	if _, ok := asm.Tools.Lookup(context.Background(), "run_command"); !ok {
		t.Fatal("缺少 run_command")
	}
	found := false
	for _, inst := range asm.Plugins {
		if inst.ID() != "tools-shell" {
			continue
		}
		for _, td := range inst.Contribution().Tools {
			_, err := inst.CallTool(context.Background(), td.Handler,
				json.RawMessage(`{"command":"git status"}`), hc)
			if err != nil && strings.Contains(err.Error(), "不经过 shell") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("带空白的 command 没有按预期被拒绝")
	}
}

// TestPolicyDefaultModes 钉 policy-default 的四种模式与两条失败关闭。
func TestPolicyDefaultModes(t *testing.T) {
	writeEvt := ports.HookEvent{
		Point:    ports.HookToolBeforeCall,
		Tool:     "write_file",
		Access:   domain.AccessWrite,
		Snapshot: json.RawMessage(`{"input":{"path":"/workspace/a.txt","content":"x"}}`),
	}
	execEvt := ports.HookEvent{
		Point:    ports.HookToolBeforeCall,
		Tool:     "run_command",
		Access:   domain.AccessExec,
		Snapshot: json.RawMessage(`{"input":{"command":"git","args":["status"]}}`),
	}
	readEvt := ports.HookEvent{
		Point:    ports.HookToolBeforeCall,
		Tool:     "read_file",
		Access:   domain.AccessRead,
		Snapshot: json.RawMessage(`{"input":{"path":"/workspace/a.txt"}}`),
	}
	outsideEvt := ports.HookEvent{
		Point:    ports.HookToolBeforeCall,
		Tool:     "write_file",
		Access:   domain.AccessWrite,
		Snapshot: json.RawMessage(`{"input":{"path":"/etc/passwd"}}`),
	}

	// runIn 以给定模式装配一份「写工具 + 策略插件」的目录，并返回钩子链。
	runIn := func(t *testing.T, mode string, ui host.Context) *plugins.HookChain {
		t.Helper()
		ws := tempDir(t)
		hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
		hc.UI = ui.UI
		settings := map[string]map[string]any{"policy-default": {"mode": mode}}
		asm := assemble(t, testProduct(t, "tools-fs", "policy-default"), hc, settings)
		t.Cleanup(func() { _ = asm.Close(context.Background()) })
		return asm.Hooks
	}

	// readonly：读放行，写/执行拒绝
	ro := runIn(t, "readonly", host.Context{})
	if v, err := ro.Run(context.Background(), readEvt); err != nil || !v.Allowed {
		t.Fatalf("readonly 读应放行: %+v %v", v, err)
	}
	if v, _ := ro.Run(context.Background(), writeEvt); v.Allowed || !strings.Contains(v.Reason, "readonly") {
		t.Fatalf("readonly 写应拒绝: %+v", v)
	}
	if v, _ := ro.Run(context.Background(), execEvt); v.Allowed {
		t.Fatalf("readonly 执行应拒绝: %+v", v)
	}

	// full：写与执行放行，但工作区外路径仍拒绝
	full := runIn(t, "full", host.Context{})
	if v, _ := full.Run(context.Background(), writeEvt); !v.Allowed {
		t.Fatalf("full 写应放行: %+v", v)
	}
	if v, _ := full.Run(context.Background(), execEvt); !v.Allowed {
		t.Fatalf("full 执行应放行: %+v", v)
	}
	if v, _ := full.Run(context.Background(), outsideEvt); v.Allowed || !strings.Contains(v.Reason, "工作区") {
		t.Fatalf("full 也必须拒绝工作区外路径: %+v", v)
	}

	// ask + 用户同意 / 拒绝
	yes := runIn(t, "ask", host.Context{UI: func(context.Context, host.UIRequest) (host.UIResolution, error) {
		return host.UIResolution{Status: domain.InteractionAccepted, Value: "yes"}, nil
	}})
	if v, _ := yes.Run(context.Background(), writeEvt); !v.Allowed {
		t.Fatalf("ask + 同意应放行: %+v", v)
	}
	no := runIn(t, "ask", host.Context{UI: func(context.Context, host.UIRequest) (host.UIResolution, error) {
		return host.UIResolution{Status: domain.InteractionDenied}, nil
	}})
	if v, _ := no.Run(context.Background(), writeEvt); v.Allowed || !strings.Contains(v.Reason, "拒绝") {
		t.Fatalf("ask + 拒绝应拒绝: %+v", v)
	}

	// ask 但没有 UI：失败关闭
	noUI := runIn(t, "ask", host.Context{})
	if v, _ := noUI.Run(context.Background(), writeEvt); v.Allowed || !strings.Contains(v.Reason, "失败关闭") {
		t.Fatalf("无 UI 时必须失败关闭: %+v", v)
	}

	// auto：写放行（工作区内），执行仍询问 → 无 UI 时拒绝
	auto := runIn(t, "auto", host.Context{})
	if v, _ := auto.Run(context.Background(), writeEvt); !v.Allowed {
		t.Fatalf("auto 写应放行: %+v", v)
	}
	if v, _ := auto.Run(context.Background(), execEvt); v.Allowed {
		t.Fatalf("auto 执行在无 UI 时应拒绝: %+v", v)
	}
}

// tempDir 造工作区（可重试删除，避免 Windows 句柄竞争）。
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "adago-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
