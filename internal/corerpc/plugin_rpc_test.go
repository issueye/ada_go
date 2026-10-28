package corerpc_test

// M3 插件方法的 RPC 验收：plugin.list / plugin.reload / plugin.setEnabled 走
// coreproto dispatch 的直连调用（不经过 WebSocket 传输层，语义一致）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/corerpc"
	"github.com/adag/ada-go/internal/proto"
)

const rpcHelloManifest = `{
  "id": "hello-echo",
  "name": "Hello Echo",
  "version": "0.1.0",
  "apiVersion": 1,
  "trust": "third-party",
  "backend": { "entry": "index.ts", "capabilities": [] }
}`

const rpcHelloSource = `export default function (agent) {
  agent.registerTool({
    name: "echo",
    description: "回显",
    parameters: { type: "object", properties: { text: { type: "string" } }, required: ["text"] },
    access: "read",
    execute: async (args) => ({ content: [{ type: "text", text: String(args.text || "") }] }),
  });
}
`

// rpcSession 打开一个带 hello-echo 插件的会话（独立于 server_test.go 的 newSession，
// 因为这里要控制工作区内容）。
func rpcSession(t *testing.T) *cli.Session {
	t.Helper()
	cfg := cli.Config{
		Workspace: t.TempDir(),
		DataDir:   t.TempDir(),
		Product:   productPath(t),
		Fake:      true,
		In:        strings.NewReader(""),
		Out:       discard{},
	}
	dir := filepath.Join(cfg.Workspace, ".adago", "plugins", "hello-echo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(rpcHelloManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte(rpcHelloSource), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := cli.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

// rpcCall 直连 dispatch 一个方法。
func rpcCall(t *testing.T, srv *corerpc.Server, method string, params any) (json.RawMessage, *proto.RPCError) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	resp := srv.Core().Handle(context.Background(), proto.Request{
		JSONRPC: "2.0", ID: 1, Method: method, Params: raw,
	})
	return resp.Result, resp.Error
}

func TestPluginRPCListSetEnabledReload(t *testing.T) {
	sess := rpcSession(t)
	srv, err := corerpc.New(corerpc.Options{Session: sess})
	if err != nil {
		t.Fatalf("corerpc.New: %v", err)
	}
	defer srv.Close()

	// plugin.list：第三方行带 enabled/status/fingerprint/source。
	result, rpcErr := rpcCall(t, srv, "plugin.list", map[string]any{})
	if rpcErr != nil {
		t.Fatalf("plugin.list: %v", rpcErr.Message)
	}
	var list struct {
		Plugins []struct {
			ID          string `json:"id"`
			Enabled     bool   `json:"enabled"`
			Status      string `json:"status"`
			Fingerprint string `json:"fingerprint"`
			Source      string `json:"source"`
			Trust       string `json:"trust"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(result, &list); err != nil {
		t.Fatalf("解析 plugin.list 结果: %v", err)
	}
	var echoRow *struct {
		ID          string `json:"id"`
		Enabled     bool   `json:"enabled"`
		Status      string `json:"status"`
		Fingerprint string `json:"fingerprint"`
		Source      string `json:"source"`
		Trust       string `json:"trust"`
	}
	for i := range list.Plugins {
		if list.Plugins[i].ID == "hello-echo" {
			echoRow = &list.Plugins[i]
		}
	}
	if echoRow == nil {
		t.Fatalf("plugin.list 缺少第三方插件: %s", result)
	}
	if !echoRow.Enabled || echoRow.Status != "ready" || echoRow.Fingerprint == "" ||
		echoRow.Source != "workspace" || echoRow.Trust != "third-party" {
		t.Fatalf("第三方行不完整: %+v", echoRow)
	}

	// setEnabled(false) → 工具消失（INV-11）。
	_, rpcErr = rpcCall(t, srv, "plugin.setEnabled", map[string]any{"pluginId": "hello-echo", "enabled": false})
	if rpcErr != nil {
		t.Fatalf("plugin.setEnabled(false): %v", rpcErr.Message)
	}
	if hasTool(t, sess, "echo") {
		t.Fatal("禁用后 echo 仍在目录里")
	}

	// setEnabled(true) → 恢复。
	_, rpcErr = rpcCall(t, srv, "plugin.setEnabled", map[string]any{"pluginId": "hello-echo", "enabled": true})
	if rpcErr != nil {
		t.Fatalf("plugin.setEnabled(true): %v", rpcErr.Message)
	}
	if !hasTool(t, sess, "echo") {
		t.Fatal("重新启用后 echo 没有恢复")
	}

	// plugin.reload：重载后返回插件列表。
	result, rpcErr = rpcCall(t, srv, "plugin.reload", map[string]any{"pluginId": "hello-echo"})
	if rpcErr != nil {
		t.Fatalf("plugin.reload: %v", rpcErr.Message)
	}
	if !strings.Contains(string(result), "hello-echo") {
		t.Fatalf("plugin.reload 结果缺少插件: %s", result)
	}

	// 参数校验：缺 enabled 拒绝。
	if _, rpcErr = rpcCall(t, srv, "plugin.setEnabled", map[string]any{"pluginId": "hello-echo"}); rpcErr == nil {
		t.Fatal("缺 enabled 应该被拒绝")
	}
}

func hasTool(t *testing.T, sess *cli.Session, name string) bool {
	t.Helper()
	for _, d := range sess.Assembly().Tools.List(context.Background()) {
		if d.Name == name {
			return true
		}
	}
	return false
}
