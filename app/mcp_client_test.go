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
	"time"

	adagapp "github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// fakeMCP 是一个最小 HTTP MCP server：initialize / tools/list / tools/call。
type fakeMCP struct {
	mu       sync.Mutex
	calls    []string
	sessions int
	// sawSession 记录后续请求是否带上了 initialize 返回的会话头。
	sawSession bool
	// expireOnce 让第一个 tools/call 返回 404（模拟会话过期），用于验证重连。
	expireOnce bool
	expired    bool
	// hangCall 非空时 tools/call 一直挂起，直到测试关闭它（验证调用期限）。
	hangCall chan struct{}
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
		if req.ID != 0 && req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") == "sess-1" {
			f.sawSession = true
		}
		expireNow := f.expireOnce && !f.expired && req.ID != 0 && req.Method == "tools/call"
		if expireNow {
			f.expired = true
		}
		f.mu.Unlock()

		if expireNow {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

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
			if f.hangCall != nil {
				<-f.hangCall
				return
			}
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
	sawSession := fake.sawSession
	fake.mu.Unlock()
	if sessions != 1 {
		t.Fatalf("initialize 次数 = %d，期望 1", sessions)
	}
	if !sawSession {
		t.Fatal("后续请求应带上 initialize 返回的 Mcp-Session-Id")
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
	scripts := mcpScriptsWithStdioHelper(t)
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

// mcpScriptsWithStdioHelper 返回官方插件脚本，并给 mcp-client 清单补上测试专用的
// 环境变量白名单（真实清单不应包含测试变量）。
func mcpScriptsWithStdioHelper(t *testing.T) []host.Script {
	t.Helper()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
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
	return scripts
}

// TestMCPStdioBigHelper 是「一条响应就超过 50KB」的 stdio MCP server。
// 用来钉住分帧：宿主 process.read 必须按头部切片 + 保留余量，不能 tail 截断
// （Linux 管道 64KB 时，tail 截断会把 JSON 行弄坏，调用只能等到超时）。
func TestMCPStdioBigHelper(t *testing.T) {
	if os.Getenv("ADAG_MCP_STDIO_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(sc.Text())), &req); err != nil || req.ID == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "stdio-big", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "big", "description": "大响应",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{
				"type": "text", "text": strings.Repeat("x", 120*1024),
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

// TestMCPClientStdioLargeResponse：一条 >50KB 的 stdio 响应必须能完整收到并被截断，
// 而不是因为分帧丢字节变成「调用超时」。
func TestMCPClientStdioLargeResponse(t *testing.T) {
	ws := tempDir(t)
	scripts := mcpScriptsWithStdioHelper(t)
	if err := os.MkdirAll(filepath.Join(ws, ".adag"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "version": 1,
	  "servers": [{
	    "id": "big",
	    "transport": "stdio",
	    "command": %q,
	    "args": ["-test.run=TestMCPStdioBigHelper"],
	    "env": ["ADAG_MCP_STDIO_HELPER"],
	    "timeoutMs": 8000
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

	text, res := callTool(t, asm, hc, "mcp_big_big", map[string]any{})
	if res.Details["mcpError"] == true {
		t.Fatalf(">50KB 的 stdio 响应被当成分帧失败：%s", text)
	}
	if len(text) < 50*1024 {
		t.Fatalf("大响应应在 50KB 处截断而不是丢失，实际 %d 字节", len(text))
	}
	if !strings.HasPrefix(text, "xxxx") {
		t.Fatalf("响应头部被破坏：%q", text[:32])
	}
}

// TestMCPClientInvalidConfigDegrades：配置文件写错（非法 id、重复 id）只降级对应项，
// 不能让插件装配失败——官方插件的装配失败会让整个产品起不来。
func TestMCPClientInvalidConfigDegrades(t *testing.T) {
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
	  "servers": [
	    {"id": "BAD ID", "transport": "http", "url": %q},
	    {"id": "demo", "transport": "http", "url": %q, "timeoutMs": 5000},
	    {"id": "demo", "transport": "http", "url": "http://127.0.0.1:1/mcp"}
	  ]
	}`, srv.URL, srv.URL)
	if err := os.WriteFile(filepath.Join(ws, ".adag", "mcp.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	hc := host.Context{
		Workspace: ws,
		Log:       func(string, string, map[string]any) {},
		HTTP:      bridge,
	}
	asm := assemble(t, testProduct(t, "mcp-client", "policy-default"), hc, nil)
	if _, ok := asm.Tools.Lookup(context.Background(), "mcp_demo_echo"); !ok {
		t.Fatalf("非法配置项不应拖垮整份配置，现有工具 %v", toolNames(asm))
	}
}

// TestMCPClientHangingServerDegrades：一个「TCP 能连、永远不回响应」的 server
// 必须在自己的期限内降级，不能把装配拖到 initTimeoutMs / 宿主 fetch 兜底。
func TestMCPClientHangingServerDegrades(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		hang.Close()
	}()
	fake := &fakeMCP{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	bridge, err := host.NewHTTPBridge(host.HTTPBridgeConfig{})
	if err != nil {
		t.Fatalf("NewHTTPBridge: %v", err)
	}
	defer bridge.Close()

	ws := tempDir(t)
	if err := os.MkdirAll(filepath.Join(ws, ".adag"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "version": 1,
	  "servers": [
	    {"id": "hang", "transport": "http", "url": %q, "timeoutMs": 1000},
	    {"id": "demo", "transport": "http", "url": %q, "timeoutMs": 5000}
	  ]
	}`, hang.URL, srv.URL)
	if err := os.WriteFile(filepath.Join(ws, ".adag", "mcp.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	hc := host.Context{
		Workspace: ws,
		Log:       func(string, string, map[string]any) {},
		HTTP:      bridge,
	}
	begin := time.Now()
	asm := assemble(t, testProduct(t, "mcp-client", "policy-default"), hc, nil)
	elapsed := time.Since(begin)
	if _, ok := asm.Tools.Lookup(context.Background(), "mcp_demo_echo"); !ok {
		t.Fatalf("挂起 server 不应拖垮其它 server，现有工具 %v", toolNames(asm))
	}
	for _, name := range toolNames(asm) {
		if strings.HasPrefix(name, "mcp_hang_") {
			t.Fatalf("挂起 server 不应贡献工具：%s", name)
		}
	}
	if elapsed > 15*time.Second {
		t.Fatalf("装配被挂起 server 拖了 %v（单 server 期限应 ≤ timeoutMs）", elapsed)
	}
}

// TestMCPClientSessionExpiryReinit：服务端用 404 表示会话失效时，客户端应重新
// initialize 一次并重试该请求（被 404 拒绝的请求没有执行，重试不会重复副作用）。
func TestMCPClientSessionExpiryReinit(t *testing.T) {
	ws := tempDir(t)
	fake := &fakeMCP{expireOnce: true}
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
	  "servers": [{"id": "demo", "transport": "http", "url": %q, "timeoutMs": 5000}]
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
	text, res := callTool(t, asm, hc, "mcp_demo_echo", map[string]any{"text": "hi"})
	if res.Details["mcpError"] == true {
		t.Fatalf("会话过期后应重连并成功，实际：%s", text)
	}
	if text != "echo:hi" {
		t.Fatalf("mcp_demo_echo = %q", text)
	}
	fake.mu.Lock()
	sessions, sawSession := fake.sessions, fake.sawSession
	fake.mu.Unlock()
	if sessions != 2 {
		t.Fatalf("initialize 次数 = %d，过期后应重连一次（共 2 次）", sessions)
	}
	if !sawSession {
		t.Fatal("后续请求应带上 initialize 返回的 Mcp-Session-Id")
	}
}

// TestMCPClientCallTimeoutBounded：tools/call 挂起时，server.timeoutMs 必须真正取消
// HTTP 请求（而不是只放弃等待），调用按普通错误结果返回，不拖到工具/轮次预算。
func TestMCPClientCallTimeoutBounded(t *testing.T) {
	ws := tempDir(t)
	release := make(chan struct{})
	fake := &fakeMCP{hangCall: release}
	srv := httptest.NewServer(fake.handler())
	defer func() {
		close(release)
		srv.Close()
	}()

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
	  "servers": [{"id": "demo", "transport": "http", "url": %q, "timeoutMs": 1000}]
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

	begin := time.Now()
	text, res := callTool(t, asm, hc, "mcp_demo_echo", map[string]any{"text": "hi"})
	elapsed := time.Since(begin)
	if res.Details["mcpError"] != true {
		t.Fatalf("挂起的 tools/call 应返回错误结果，实际：%s", text)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("调用被挂起 server 拖了 %v（应 ≤ timeoutMs 附近）", elapsed)
	}
}

// TestMCPClientAdminRoutes 钉住管理页的数据面：状态（含工具摘要/来源/停用项）、
// 工作区配置读取与保存、单 server 启停，以及非法配置的拒绝。
func TestMCPClientAdminRoutes(t *testing.T) {
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
	initial := fmt.Sprintf(`{
  "version": 1,
  "servers": [
    {"id": "demo", "transport": "http", "url": %q, "timeoutMs": 5000},
    {"id": "off", "transport": "http", "url": %q, "enabled": false}
  ]
}`, srv.URL, srv.URL)
	cfgPath := filepath.Join(ws, ".adag", "mcp.json")
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	hc := host.Context{
		Workspace: ws,
		Log:       func(string, string, map[string]any) {},
		HTTP:      bridge,
	}
	asm := assemble(t, testProduct(t, "mcp-client", "policy-default"), hc, nil)
	ctx := context.Background()

	out, err := asm.CallHTTPAPI(ctx, "mcp-client", "GET", "/servers", nil, "")
	if err != nil {
		t.Fatalf("GET /servers: %v", err)
	}
	var state struct {
		ConfigPath string `json:"configPath"`
		Servers    []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Enabled bool   `json:"enabled"`
			Source  string `json:"source"`
			Tools   int    `json:"tools"`
			Items   []struct {
				Name             string `json:"name"`
				McpName          string `json:"mcpName"`
				Access           string `json:"access"`
				SchemaSimplified bool   `json:"schemaSimplified"`
			} `json:"items"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(out, &state); err != nil {
		t.Fatalf("解析 /servers: %v\n%s", err, out)
	}
	if state.ConfigPath != ".adag/mcp.json" {
		t.Fatalf("configPath = %q", state.ConfigPath)
	}
	byID := map[string]int{}
	for i, s := range state.Servers {
		byID[s.ID] = i
	}
	demo, ok := byID["demo"]
	if !ok {
		t.Fatalf("缺少 demo: %s", out)
	}
	if got := state.Servers[demo]; got.Status != "ready" || !got.Enabled || got.Source != ".adag/mcp.json" || got.Tools != 2 || len(got.Items) != 2 {
		t.Fatalf("demo 状态不符: %+v", got)
	}
	off, ok := byID["off"]
	if !ok {
		t.Fatalf("停用的 server 也应出现在管理页: %s", out)
	}
	if got := state.Servers[off]; got.Status != "disabled" || got.Enabled || got.Tools != 0 {
		t.Fatalf("off 状态不符: %+v", got)
	}
	var sawBroken bool
	for _, it := range state.Servers[demo].Items {
		if it.Name == "mcp_demo_broken" && it.SchemaSimplified {
			sawBroken = true
		}
		if it.McpName == "" || it.Access == "" {
			t.Fatalf("工具摘要缺字段: %+v", it)
		}
	}
	if !sawBroken {
		t.Fatalf("工具摘要里应标记 schemaSimplified: %+v", state.Servers[demo].Items)
	}

	// 读工作区配置原文。
	out, err = asm.CallHTTPAPI(ctx, "mcp-client", "GET", "/config", nil, "")
	if err != nil {
		t.Fatalf("GET /config: %v", err)
	}
	var cfg struct {
		Path   string `json:"path"`
		Exists bool   `json:"exists"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("解析 /config: %v", err)
	}
	if cfg.Path != ".adag/mcp.json" || !cfg.Exists || cfg.Text != initial {
		t.Fatalf("工作区配置读取不符: path=%q exists=%v text=%q", cfg.Path, cfg.Exists, cfg.Text)
	}

	// 保存新配置：应写回工作区文件。
	next := fmt.Sprintf(`{"version":1,"servers":[{"id":"demo","transport":"http","url":%q,"timeoutMs":5000},{"id":"extra","transport":"http","url":%q,"enabled":false}]}`, srv.URL, srv.URL)
	save, _ := json.Marshal(map[string]any{
		"method": "POST", "path": "/config",
		"body": map[string]any{"text": next},
	})
	out, err = asm.CallHTTPAPI(ctx, "mcp-client", "POST", "/config", save, "")
	if err != nil {
		t.Fatalf("POST /config: %v", err)
	}
	if !strings.Contains(string(out), `"saved":true`) || !strings.Contains(string(out), `"servers":2`) {
		t.Fatalf("保存结果不符: %s", out)
	}
	if raw, rerr := os.ReadFile(cfgPath); rerr != nil || string(raw) != next {
		t.Fatalf("配置文件未被写回: %v %q", rerr, raw)
	}

	// 非法 JSON 必须被拒绝且不改文件。
	bad, _ := json.Marshal(map[string]any{
		"method": "POST", "path": "/config",
		"body": map[string]any{"text": "{not json"},
	})
	if _, err := asm.CallHTTPAPI(ctx, "mcp-client", "POST", "/config", bad, ""); err == nil || !strings.Contains(err.Error(), "不是合法 JSON") {
		t.Fatalf("非法 JSON 的错误 = %v", err)
	}
	if raw, _ := os.ReadFile(cfgPath); string(raw) != next {
		t.Fatalf("非法保存不应改文件: %q", raw)
	}

	// 停用 demo：写回配置并带 enabled=false。
	toggle, _ := json.Marshal(map[string]any{
		"method": "POST", "path": "/servers/toggle",
		"body": map[string]any{"id": "demo", "enabled": false},
	})
	if _, err := asm.CallHTTPAPI(ctx, "mcp-client", "POST", "/servers/toggle", toggle, ""); err != nil {
		t.Fatalf("停用 demo: %v", err)
	}
	raw, _ := os.ReadFile(cfgPath)
	var saved struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("保存后的配置不是合法 JSON: %v", err)
	}
	for _, item := range saved.Servers {
		if item["id"] == "demo" && item["enabled"] != false {
			t.Fatalf("demo 应被停用: %v", item)
		}
	}

	// 启用 extra：应删掉 enabled 键（默认即启用）。
	enable, _ := json.Marshal(map[string]any{
		"method": "POST", "path": "/servers/toggle",
		"body": map[string]any{"id": "extra", "enabled": true},
	})
	if _, err := asm.CallHTTPAPI(ctx, "mcp-client", "POST", "/servers/toggle", enable, ""); err != nil {
		t.Fatalf("启用 extra: %v", err)
	}
	raw, _ = os.ReadFile(cfgPath)
	saved = struct {
		Servers []map[string]any `json:"servers"`
	}{}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("保存后的配置不是合法 JSON: %v", err)
	}
	for _, item := range saved.Servers {
		if item["id"] == "extra" {
			if _, ok := item["enabled"]; ok {
				t.Fatalf("启用时应删掉 enabled 键: %v", item)
			}
		}
	}

	// 未知 id 报错。
	unknown, _ := json.Marshal(map[string]any{
		"method": "POST", "path": "/servers/toggle",
		"body": map[string]any{"id": "nope", "enabled": true},
	})
	if _, err := asm.CallHTTPAPI(ctx, "mcp-client", "POST", "/servers/toggle", unknown, ""); err == nil || !strings.Contains(err.Error(), "没有 server") {
		t.Fatalf("未知 server 的错误 = %v", err)
	}
}

// TestMCPClientAdminConfigBootstrap：工作区还没有 .adag/mcp.json 时，管理页应能
// 拿到初稿并通过保存创建文件（宿主负责补建父目录）。
func TestMCPClientAdminConfigBootstrap(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm := assemble(t, testProduct(t, "mcp-client", "policy-default"), hc, nil)
	ctx := context.Background()

	out, err := asm.CallHTTPAPI(ctx, "mcp-client", "GET", "/config", nil, "")
	if err != nil {
		t.Fatalf("GET /config: %v", err)
	}
	var cfg struct {
		Exists bool   `json:"exists"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("解析 /config: %v", err)
	}
	if cfg.Exists {
		t.Fatal("空工作区不应报告配置存在")
	}
	if !strings.Contains(cfg.Text, `"servers": []`) {
		t.Fatalf("初稿应包含空 servers 数组: %q", cfg.Text)
	}

	text := `{"version":1,"servers":[]}` + "\n"
	body, _ := json.Marshal(map[string]any{
		"method": "POST", "path": "/config",
		"body": map[string]any{"text": text},
	})
	if _, err := asm.CallHTTPAPI(ctx, "mcp-client", "POST", "/config", body, ""); err != nil {
		t.Fatalf("POST /config: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ws, ".adag", "mcp.json"))
	if err != nil {
		t.Fatalf("保存应创建 .adag/mcp.json: %v", err)
	}
	if string(raw) != text {
		t.Fatalf("文件内容 = %q", raw)
	}
}

// TestMCPClientSkill：随包技能 mcp-tools 必须进入装配期技能表（只结算
// name/description/绝对 path；正文由模型用 read_file 自己读）。
func TestMCPClientSkill(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	asm := assemble(t, testProduct(t, "mcp-client", "policy-default"), hc, nil)

	found := false
	for _, skill := range asm.Skills() {
		if skill.Name != "mcp-tools" {
			continue
		}
		found = true
		if skill.PluginID != "mcp-client" || skill.Origin != host.SkillOriginPlugin {
			t.Fatalf("技能来源不符: %+v", skill)
		}
		if skill.Description == "" {
			t.Fatal("技能缺少 description")
		}
		body, err := os.ReadFile(skill.Path)
		if err != nil {
			t.Fatalf("技能正文不可读（%q）: %v", skill.Path, err)
		}
		if !strings.Contains(string(body), "mcp_<serverId>_<tool>") {
			t.Fatalf("技能正文不符合预期: %.200s", body)
		}
	}
	if !found {
		t.Fatalf("技能表里没有 mcp-tools: %+v", asm.Skills())
	}
}
