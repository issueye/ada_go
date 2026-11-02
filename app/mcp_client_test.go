package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	adagapp "github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// fakeMCP 是一个最小 HTTP MCP server：initialize / tools/list / tools/call。
type fakeMCP struct {
	mu       sync.Mutex
	calls    []string
	sessions int
}

func (f *fakeMCP) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, req.Method)
		f.mu.Unlock()

		if req.ID == 0 {
			// notifications/initialized：MCP 允许 202 空响应。
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			f.mu.Lock()
			f.sessions++
			f.mu.Unlock()
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "fake", "version": "1"},
				},
			})
			return
		}
		switch req.Method {
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []any{
					map[string]any{
						"name": "echo", "description": "回显文本",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"text": map[string]any{"type": "string"}},
							"required":   []string{"text"},
						},
					},
					map[string]any{
						"name": "broken", "description": "不支持的组合 Schema",
						"inputSchema": map[string]any{"oneOf": []any{map[string]any{"type": "string"}}},
					},
				}},
			})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name != "echo" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"result": map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "unknown tool"}}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"content": []any{
					map[string]any{"type": "text", "text": "echo:" + fmt.Sprint(p.Arguments["text"])},
				}},
			})
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	})
}

func TestMCPClientHTTP(t *testing.T) {
	ws := tempDir(t)
	fake := &fakeMCP{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	bridge, err := host.NewHTTPBridge(host.HTTPBridgeConfig{})
	if err != nil {
		t.Fatalf("NewHTTPBridge: %v", err)
	}
	defer bridge.Close()

	if err := os.MkdirAll(filepath.Join(ws, ".adag"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "version": 1,
	  "servers": [{
	    "id": "demo",
	    "transport": "http",
	    "url": %q,
	    "timeoutMs": 5000,
	    "trustReadOnly": false
	  }]
	}`, srv.URL)
	if err := os.WriteFile(filepath.Join(ws, ".adag", "mcp.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	hc := host.Context{
		Workspace: ws,
		Log:       func(string, string, map[string]any) {},
		HTTP:      bridge,
	}
	asm := assemble(t, testProduct(t, "mcp-client", "policy-default"), hc, nil)

	echo, ok := asm.Tools.Lookup(context.Background(), "mcp_demo_echo")
	if !ok {
		t.Fatalf("缺少 mcp_demo_echo，现有 %v", toolNames(asm))
	}
	if echo.Access != "write" {
		t.Fatalf("默认 trustReadOnly=false 时 MCP 工具应为 write，实际 %q", echo.Access)
	}
	if _, ok := asm.Tools.Lookup(context.Background(), "mcp_demo_broken"); !ok {
		t.Fatalf("不支持的 Schema 应注册为宽松工具，现有 %v", toolNames(asm))
	}

	text, _ := callTool(t, asm, hc, "mcp_demo_echo", map[string]any{"text": "hi"})
	if text != "echo:hi" {
		t.Fatalf("mcp_demo_echo = %q", text)
	}

	fake.mu.Lock()
	calls := append([]string(nil), fake.calls...)
	sessions := fake.sessions
	fake.mu.Unlock()
	if sessions != 1 {
		t.Fatalf("initialize 次数 = %d，期望 1", sessions)
	}
	for _, want := range []string{"initialize", "tools/list", "tools/call"} {
		found := false
		for _, c := range calls {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("没有调用 %s：%v", want, calls)
		}
	}

	// 宽松 Schema 必须显式标记，而不是假装 MCP Schema 被完整支持。
	for _, inst := range asm.Plugins {
		if inst.ID() != "mcp-client" {
			continue
		}
		for _, td := range inst.Contribution().Tools {
			if td.Name != "mcp_demo_broken" {
				continue
			}
			if !strings.Contains(string(td.Parameters), "additionalProperties") {
				t.Fatalf("broken Schema 应是宽松兜底：%s", td.Parameters)
			}
		}
	}
}

func TestMCPClientServerUnavailable(t *testing.T) {
	ws := tempDir(t)
	bridge, err := host.NewHTTPBridge(host.HTTPBridgeConfig{})
	if err != nil {
		t.Fatalf("NewHTTPBridge: %v", err)
	}
	defer bridge.Close()

	if err := os.MkdirAll(filepath.Join(ws, ".adag"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
	  "version": 1,
	  "servers": [{
	    "id": "dead",
	    "transport": "http",
	    "url": "http://127.0.0.1:1/mcp",
	    "timeoutMs": 500
	  }]
	}`
	if err := os.WriteFile(filepath.Join(ws, ".adag", "mcp.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	hc := host.Context{
		Workspace: ws,
		Log:       func(string, string, map[string]any) {},
		HTTP:      bridge,
	}
	// 单 server 不可达不能让装配失败；插件应加载成功但贡献 0 个 MCP 工具。
	asm := assemble(t, testProduct(t, "mcp-client", "policy-default"), hc, nil)
	for _, name := range toolNames(asm) {
		if strings.HasPrefix(name, "mcp_dead_") {
			t.Fatalf("不可达 server 不应贡献工具：%s", name)
		}
	}
}

// TestMCPStdioHelper 是 stdio MCP server 的子进程助手。
func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("ADAG_MCP_STDIO_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil || req.ID == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "stdio-helper", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "stdio echo",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			}}}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			result = map[string]any{"content": []any{map[string]any{
				"type": "text", "text": "stdio:" + fmt.Sprint(p.Arguments["text"]),
			}}}
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			})
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}

func TestMCPClientStdio(t *testing.T) {
	ws := tempDir(t)
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	// 测试需要把 helper 环境变量透传到子进程；真实插件清单不应包含测试专用变量。
	for i := range scripts {
		if scripts[i].Manifest == nil || scripts[i].Manifest.ID != "mcp-client" {
			continue
		}
		m := *scripts[i].Manifest
		b := *m.Backend
		b.Env = append(append([]string(nil), b.Env...), "ADAG_MCP_STDIO_HELPER")
		m.Backend = &b
		scripts[i].Manifest = &m
	}
	if err := os.MkdirAll(filepath.Join(ws, ".adag"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "version": 1,
	  "servers": [{
	    "id": "local",
	    "transport": "stdio",
	    "command": %q,
	    "args": ["-test.run=TestMCPStdioHelper"],
	    "env": ["ADAG_MCP_STDIO_HELPER"],
	    "timeoutMs": 5000
	  }]
	}`, os.Args[0])
	if err := os.WriteFile(filepath.Join(ws, ".adag", "mcp.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm, err := adagapp.Assemble(context.Background(), testProduct(t, "mcp-client", "policy-default"), scripts, adagapp.Deps{
		Context: hc,
		HostEnv: map[string]string{"ADAG_MCP_STDIO_HELPER": "1"},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer func() { _ = asm.Close(context.Background()) }()

	if _, ok := asm.Tools.Lookup(context.Background(), "mcp_local_echo"); !ok {
		t.Fatalf("缺少 mcp_local_echo，现有 %v", toolNames(asm))
	}
	text, _ := callTool(t, asm, hc, "mcp_local_echo", map[string]any{"text": "hi"})
	if text != "stdio:hi" {
		t.Fatalf("mcp_local_echo = %q", text)
	}
}
