package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"browseruse/internal/control"
)

type fakeApp struct {
	result   json.RawMessage
	err      error
	calls    []string
	lastArgs []byte
}

func (f *fakeApp) Health(context.Context) error { return nil }
func (f *fakeApp) SetClientID(string)           {}
func (f *fakeApp) ClientID() string             { return "fake" }
func (f *fakeApp) Call(_ context.Context, name string, args any) (json.RawMessage, error) {
	f.calls = append(f.calls, name)
	if raw, err := json.Marshal(args); err == nil {
		f.lastArgs = raw
	}
	return f.result, f.err
}

func newTestService(result string) (*Service, *fakeApp) {
	fake := &fakeApp{result: json.RawMessage(result)}
	svc := NewService()
	svc.client = fake
	return svc, fake
}

func newInProcessClient(t *testing.T, svc *Service) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(newServer(svc))
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

// TestServerExposesControlTools：tools/list 与 control.Tools 一一对应，schema
// 原样暴露（raw schema，不做反射生成）。
func TestServerExposesControlTools(t *testing.T) {
	svc, _ := newTestService(`{"ok":true,"data":{}}`)
	c := newInProcessClient(t, svc)
	res, err := c.ListTools(t.Context(), mcpgo.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != len(control.Tools) {
		t.Fatalf("tools = %d，期望 %d", len(res.Tools), len(control.Tools))
	}
	byName := map[string]mcpgo.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, want := range control.Tools {
		got, ok := byName[want.Name]
		if !ok {
			t.Fatalf("缺少工具 %q", want.Name)
		}
		if got.Description != want.Description {
			t.Fatalf("%s 描述不符: %q", want.Name, got.Description)
		}
		if got.InputSchema.Type != "object" {
			t.Fatalf("%s schema type = %q", want.Name, got.InputSchema.Type)
		}
	}
	tabs := byName["tabs"]
	if tabs.InputSchema.Properties["action"] == nil {
		t.Fatalf("tabs 缺少 action 属性: %+v", tabs.InputSchema)
	}
	if len(tabs.InputSchema.Required) != 1 || tabs.InputSchema.Required[0] != "action" {
		t.Fatalf("tabs required = %v", tabs.InputSchema.Required)
	}
	if got, _ := tabs.InputSchema.AdditionalProperties.(bool); got {
		t.Fatalf("tabs additionalProperties 应为 false: %+v", tabs.InputSchema.AdditionalProperties)
	}
}

// TestCallToolStructuredAndErrors：结果映射为 text+structuredContent；应用错误
// 变成 isError 内容而不是协议错误；截图保留 image 内容。
func TestCallToolStructuredAndErrors(t *testing.T) {
	t.Run("structured", func(t *testing.T) {
		svc, fake := newTestService(`{"ok":true,"data":{"active":"t1","tabs":2}}`)
		c := newInProcessClient(t, svc)
		req := mcpgo.CallToolRequest{}
		req.Params.Name = "status"
		req.Params.Arguments = map[string]any{}
		res, err := c.CallTool(t.Context(), req)
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if res.IsError {
			t.Fatalf("不应是错误: %+v", res)
		}
		obj, ok := res.StructuredContent.(map[string]any)
		if !ok || obj["active"] != "t1" {
			t.Fatalf("structuredContent = %#v", res.StructuredContent)
		}
		if len(res.Content) != 1 {
			t.Fatalf("content = %+v", res.Content)
		}
		if text, ok := res.Content[0].(mcpgo.TextContent); !ok || !strings.Contains(text.Text, `"active":"t1"`) {
			t.Fatalf("text content = %#v", res.Content[0])
		}
		if len(fake.calls) != 1 || fake.calls[0] != "status" {
			t.Fatalf("调用记录 = %v", fake.calls)
		}
	})

	t.Run("app error", func(t *testing.T) {
		svc, _ := newTestService(`{"ok":false,"error":{"code":"bad_request","message":"boom","hint":"try again"}}`)
		c := newInProcessClient(t, svc)
		req := mcpgo.CallToolRequest{}
		req.Params.Name = "navigate"
		req.Params.Arguments = map[string]any{"action": "goto"}
		res, err := c.CallTool(t.Context(), req)
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !res.IsError {
			t.Fatalf("应为工具错误: %+v", res)
		}
		text := res.Content[0].(mcpgo.TextContent).Text
		if !strings.Contains(text, "bad_request") || !strings.Contains(text, "hint: try again") {
			t.Fatalf("错误文本 = %q", text)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		svc := NewService()
		svc.client = &fakeApp{err: context.DeadlineExceeded}
		c := newInProcessClient(t, svc)
		req := mcpgo.CallToolRequest{}
		req.Params.Name = "status"
		res, err := c.CallTool(t.Context(), req)
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !res.IsError || !strings.Contains(res.Content[0].(mcpgo.TextContent).Text, "deadline") {
			t.Fatalf("传输错误映射不符: %+v", res)
		}
	})

	t.Run("screenshot", func(t *testing.T) {
		svc, fake := newTestService(`{"ok":true,"data":{"path":"/tmp/a.png","data":"aGVsbG8=","mime_type":"image/png","width":10,"height":20,"annotated":false}}`)
		c := newInProcessClient(t, svc)
		req := mcpgo.CallToolRequest{}
		req.Params.Name = "screenshot"
		req.Params.Arguments = map[string]any{}
		res, err := c.CallTool(t.Context(), req)
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if res.IsError {
			t.Fatalf("截图不应是错误: %+v", res)
		}
		if !strings.Contains(string(fake.lastArgs), `"inline":true`) {
			t.Fatalf("截图应请求 inline 数据: %s", fake.lastArgs)
		}
		var image *mcpgo.ImageContent
		for _, content := range res.Content {
			if img, ok := content.(mcpgo.ImageContent); ok {
				image = &img
			}
		}
		if image == nil || image.Data != "aGVsbG8=" || image.MIMEType != "image/png" {
			t.Fatalf("image content = %+v", image)
		}
		meta, _ := res.StructuredContent.(map[string]any)
		if meta["path"] != "/tmp/a.png" {
			t.Fatalf("structuredContent = %#v", res.StructuredContent)
		}
	})
}

// TestHTTPStreamableTransport：/mcp 走 mcp-go 的无状态 Streamable HTTP，
// 模拟 ada-go mcp-client 的请求顺序（initialize → initialized → tools/list → call）。
func TestHTTPStreamableTransport(t *testing.T) {
	svc, _ := newTestService(`{"ok":true,"data":{"active":"t1"}}`)
	ts := httptest.NewServer(newHTTPMux(newServer(svc)))
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
			req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /mcp: %v", err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		return r.StatusCode, parseRPCBody(t, raw)
	}

	status, init := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"check","version":"0"}}}`, false)
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d", status)
	}
	result, _ := init["result"].(map[string]any)
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "browser-use" {
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
	if len(tools) != len(control.Tools) {
		t.Fatalf("HTTP tools = %d，期望 %d", len(tools), len(control.Tools))
	}

	status, call := post(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{}}}`, true)
	if status != http.StatusOK {
		t.Fatalf("tools/call status = %d", status)
	}
	callResult, _ := call["result"].(map[string]any)
	structured, _ := callResult["structuredContent"].(map[string]any)
	if structured["active"] != "t1" {
		t.Fatalf("structuredContent = %#v", callResult)
	}
}

// TestCallToolCompactTextAndErrorDetails 钉住模型可见文本的形态：
// snapshot 只出现一次（不是整个 JSON 信封），ambiguous 的候选详情保留在错误里。
func TestCallToolCompactTextAndErrorDetails(t *testing.T) {
	t.Run("snapshot text is not raw json", func(t *testing.T) {
		svc, _ := newTestService(`{"ok":true,"data":{
			"snapshot":"[e1] button \"Go\"",
			"url":"http://x/","title":"X","truncated":false,
			"nodes":[{"ref":"e1","role":"button","name":"Go","rect":{"x":0,"y":0,"w":1,"h":1}}]
		}}`)
		c := newInProcessClient(t, svc)
		req := mcpgo.CallToolRequest{}
		req.Params.Name = "snapshot"
		req.Params.Arguments = map[string]any{}
		res, err := c.CallTool(t.Context(), req)
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		text := res.Content[0].(mcpgo.TextContent).Text
		if !strings.Contains(text, `[e1] button "Go"`) {
			t.Fatalf("text = %q", text)
		}
		if strings.Contains(text, `"nodes"`) {
			t.Fatalf("text still contains the raw JSON envelope: %q", text)
		}
		obj, _ := res.StructuredContent.(map[string]any)
		if obj["snapshot"] != `[e1] button "Go"` {
			t.Fatalf("structuredContent = %#v", res.StructuredContent)
		}
	})

	t.Run("error keeps details", func(t *testing.T) {
		svc, _ := newTestService(`{"ok":false,"error":{
			"code":"ambiguous","message":"2 elements match",
			"details":{"candidates":[{"ref":"e1","name":"Go"}]}
		}}`)
		c := newInProcessClient(t, svc)
		req := mcpgo.CallToolRequest{}
		req.Params.Name = "click"
		req.Params.Arguments = map[string]any{"selector": ".go"}
		res, err := c.CallTool(t.Context(), req)
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !res.IsError {
			t.Fatalf("expected tool error: %+v", res)
		}
		text := res.Content[0].(mcpgo.TextContent).Text
		if !strings.Contains(text, "details:") || !strings.Contains(text, "candidates") {
			t.Fatalf("error text lost details: %q", text)
		}
	})
}

// parseRPCBody 同时接受 application/json 与 SSE（data: 行）。
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
