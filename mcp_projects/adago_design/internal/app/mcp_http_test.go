package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"adagodesign/internal/config"
	"adagodesign/internal/mcpserver"
)

// HTTP MCP 端点契约：<BaseURL>/mcp 必须能握手并列出工具。
// Agent 接入优先走它（无需 spawn stdio 子进程，也不与已运行的画布服务互斥）。

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func postRPC(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, raw
}

func TestHTTPMCPEndpointInitializeAndList(t *testing.T) {
	cfg := &config.Config{Port: 0, DataDir: t.TempDir(), Theme: "light"}
	srv, err := Start(context.Background(), cfg, WithMCP(func(s *Server) http.Handler {
		return mcpserver.NewHTTPHandler(mcpserver.Deps{
			Store:   s.Store(),
			Tasks:   s.Tasks(),
			BaseURL: func() string { return s.BaseURL },
			DataDir: cfg.DataDir,
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestServer(t, srv) })

	url := srv.BaseURL + "/mcp"

	res, raw := postRPC(t, url, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"test","version":"0"}}}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("initialize HTTP %d: %s", res.StatusCode, raw)
	}
	var init rpcResp
	if err := json.Unmarshal(raw, &init); err != nil {
		t.Fatalf("initialize 响应不是 JSON-RPC: %v (%s)", err, raw)
	}
	if init.Error != nil {
		t.Fatalf("initialize 报错: %+v", init.Error)
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(init.Result, &initResult); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if initResult.ServerInfo.Name != "adago-design" {
		t.Fatalf("serverInfo.name = %q", initResult.ServerInfo.Name)
	}

	_, raw = postRPC(t, url, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	var list rpcResp
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("tools/list 响应不是 JSON-RPC: %v (%s)", err, raw)
	}
	var listResult struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &listResult); err != nil {
		t.Fatalf("tools/list result: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range listResult.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"design_systems", "canvas_list", "project_create"} {
		if !names[want] {
			t.Fatalf("tools/list 缺少 %q（共 %d 个工具）", want, len(listResult.Tools))
		}
	}
	if len(names) != len(listResult.Tools) {
		t.Fatalf("工具名重复：%d 个名字 / %d 个工具", len(names), len(listResult.Tools))
	}

	// 调一个纯读工具，验证 tools/call 也通了。
	_, raw = postRPC(t, url, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"design_systems","arguments":{}}}`)
	var call rpcResp
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatalf("tools/call 响应不是 JSON-RPC: %v (%s)", err, raw)
	}
	if call.Error != nil || len(call.Result) == 0 {
		t.Fatalf("tools/call 失败: err=%+v body=%s", call.Error, raw)
	}
}

// 未注入 MCP 时 /mcp 必须不存在：serve 的旧行为不能被悄悄改写。
func TestHTTPMCPAbsentWhenNotInjected(t *testing.T) {
	cfg := &config.Config{Port: 0, DataDir: t.TempDir(), Theme: "light"}
	srv, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestServer(t, srv) })

	res, raw := postRPC(t, srv.BaseURL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if res.StatusCode == http.StatusOK {
		t.Fatalf("未注入 MCP 时 /mcp 不应成功: %d %s", res.StatusCode, raw)
	}
}
