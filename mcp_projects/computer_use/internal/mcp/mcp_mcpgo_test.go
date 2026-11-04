package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// newInProcessClient 用 mcp-go 的进程内传输连接 newMCPServer，不经过 stdio。
func newInProcessClient(t *testing.T, s *Server) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(newMCPServer(s))
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	init := mcpgo.InitializeRequest{}
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "test", Version: "0"}
	if _, err := c.Initialize(t.Context(), init); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return c
}

// TestServerToolsAndResults：工具集合与 toolList 一致；调用转发到 HTTP API，
// 截图返回 image 内容，应用错误以 isError 内容返回。
func TestServerToolsAndResults(t *testing.T) {
	var mu sync.Mutex
	var lastAction []byte
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/state":
			w.Write([]byte(`{"screens":[{"index":0}]}`))
		case "/v1/actions":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			lastAction = body
			mu.Unlock()
			w.Write([]byte(`{"ok":true}`))
		case "/v1/screenshot":
			if r.URL.Query().Get("max_width") != "800" || r.URL.Query().Get("grid") != "100" {
				t.Errorf("screenshot query = %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"media_type":"image/png","image_base64":"aGVsbG8=","meta":{"width":10,"height":20}}`))
		case "/v1/script":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"script is not allowed"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	s := New(api.URL, "", "test")
	c := newInProcessClient(t, s)

	list, err := c.ListTools(t.Context(), mcpgo.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(list.Tools) != len(toolList()) {
		t.Fatalf("tools = %d，期望 %d", len(list.Tools), len(toolList()))
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
		if tool.InputSchema.Type != "object" {
			t.Fatalf("%s schema type = %q", tool.Name, tool.InputSchema.Type)
		}
	}
	if !names["screen_info"] || !names["screenshot"] || !names["click"] {
		t.Fatalf("缺少工具: %v", names)
	}

	// text 结果：screen_info 原样返回 HTTP 响应。
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "screen_info"
	req.Params.Arguments = map[string]any{}
	res, err := c.CallTool(t.Context(), req)
	if err != nil {
		t.Fatalf("CallTool screen_info: %v", err)
	}
	if res.IsError {
		t.Fatalf("screen_info 不应失败: %+v", res)
	}
	if text := res.Content[0].(mcpgo.TextContent).Text; !strings.Contains(text, `"screens"`) {
		t.Fatalf("screen_info 文本 = %q", text)
	}

	// 截图：image + text。
	req = mcpgo.CallToolRequest{}
	req.Params.Name = "screenshot"
	req.Params.Arguments = map[string]any{"max_width": 800, "grid": 100}
	res, err = c.CallTool(t.Context(), req)
	if err != nil {
		t.Fatalf("CallTool screenshot: %v", err)
	}
	var image *mcpgo.ImageContent
	var text string
	for _, content := range res.Content {
		switch c := content.(type) {
		case mcpgo.ImageContent:
			image = &c
		case mcpgo.TextContent:
			text = c.Text
		}
	}
	if image == nil || image.Data != "aGVsbG8=" || image.MIMEType != "image/png" {
		t.Fatalf("截图 image = %+v", image)
	}
	if !strings.Contains(text, `"width":10`) {
		t.Fatalf("截图文本 = %q", text)
	}

	// 动作：click 变成 POST /v1/actions {"type":"click",...}。
	req = mcpgo.CallToolRequest{}
	req.Params.Name = "click"
	req.Params.Arguments = map[string]any{"x": 3, "y": 4}
	if _, err := c.CallTool(t.Context(), req); err != nil {
		t.Fatalf("CallTool click: %v", err)
	}
	mu.Lock()
	body := string(lastAction)
	mu.Unlock()
	if !strings.Contains(body, `"type":"click"`) || !strings.Contains(body, `"x":3`) {
		t.Fatalf("actions body = %s", body)
	}

	// 应用错误 → isError 内容。
	req = mcpgo.CallToolRequest{}
	req.Params.Name = "run_script"
	req.Params.Arguments = map[string]any{"source": "1"}
	res, err = c.CallTool(t.Context(), req)
	if err != nil {
		t.Fatalf("CallTool run_script: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(mcpgo.TextContent).Text, "script is not allowed") {
		t.Fatalf("run_script 错误映射不符: %+v", res)
	}
}

// TestHandshakeWire 检查真实 stdio 字节流上的 initialize / tools/list。
func TestHandshakeWire(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
`)
	out := &strings.Builder{}
	if err := New(api.URL, "", "test").Serve(in, out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("answers = %d:\n%s", len(lines), out.String())
	}
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatal(err)
	}
	if init.Result.ProtocolVersion != "2025-03-26" || init.Result.ServerInfo.Name != "computer-use" {
		t.Fatalf("initialize = %+v", init.Result)
	}
	var list struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Result.Tools) != len(toolList()) {
		t.Fatalf("tools = %d，期望 %d", len(list.Result.Tools), len(toolList()))
	}
}

// TestHTTPHandler：/mcp 走 mcp-go 的无状态 Streamable HTTP，/healthz 可用；
// 请求顺序与 ada-go mcp-client 一致（initialize → initialized → tools/list → call）。
func TestHTTPHandler(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/state":
			w.Write([]byte(`{"ok":true,"paused":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	ts := httptest.NewServer(New(api.URL, "", "test").HTTPHandler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "mcp-http") {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}

	post := func(payload string, withVersion bool) (int, map[string]any) {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if withVersion {
			req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /mcp: %v", err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		return r.StatusCode, parseRPCBody(t, raw)
	}

	status, init := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"check","version":"0"}}}`, false)
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d", status)
	}
	result, _ := init["result"].(map[string]any)
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "computer-use" {
		t.Fatalf("serverInfo = %#v", result)
	}

	status, _ = post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, true)
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("initialized status = %d", status)
	}

	status, list := post(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, true)
	if status != http.StatusOK {
		t.Fatalf("tools/list status = %d", status)
	}
	listResult, _ := list["result"].(map[string]any)
	tools, _ := listResult["tools"].([]any)
	if len(tools) != len(toolList()) {
		t.Fatalf("HTTP tools = %d，期望 %d", len(tools), len(toolList()))
	}

	status, call := post(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"screen_info","arguments":{}}}`, true)
	if status != http.StatusOK {
		t.Fatalf("tools/call status = %d", status)
	}
	callResult, _ := call["result"].(map[string]any)
	content, _ := callResult["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("content = %#v", callResult)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, `"paused":false`) {
		t.Fatalf("screen_info text = %q", text)
	}
}

// parseRPCBody accepts application/json and SSE (data: lines).
func parseRPCBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return map[string]any{}
	}
	var whole map[string]any
	if json.Unmarshal([]byte(text), &whole) == nil {
		return whole
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var msg map[string]any
		if json.Unmarshal([]byte(payload), &msg) == nil {
			return msg
		}
	}
	t.Fatalf("无法解析 JSON-RPC 响应: %q", raw)
	return nil
}
