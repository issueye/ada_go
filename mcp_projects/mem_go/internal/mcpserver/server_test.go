package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mem_go/internal/memory"
)

// newTestSession 建立一条内存 MCP 会话（服务端挂载真实引擎）。
func newTestSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	engine, err := memory.Open(memory.Config{
		DBPath:   filepath.Join(t.TempDir(), "mcp_test.db"),
		Embedder: memory.NewHashEmbedder(128),
	})
	if err != nil {
		t.Fatalf("打开引擎失败: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := NewServer(engine)
	go func() { _ = srv.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("客户端连接失败: %v", err)
	}
	return sess
}

// callTool 调用工具并返回解析后的结构化输出（附带 _text 字段为文本内容）。
func callTool(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s 返回工具错误: %+v", name, res.Content)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	out := map[string]any{"_text": text}
	if res.StructuredContent != nil {
		data, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func TestListTools(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	want := map[string]bool{
		"memory_store": false, "memory_search": false, "memory_list": false,
		"memory_get": false, "memory_delete": false, "memory_forget": false,
		"memory_stats": false,
	}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("缺少工具 %s", name)
		}
	}
}

func TestStoreThenSearchOverMCP(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	storeOut := callTool(t, sess, "memory_store", map[string]any{
		"content":    "用户的主力开发语言是 Go",
		"kind":       "fact",
		"importance": 0.9,
		"tags":       []string{"lang"},
	})
	if storeOut["action"] != "created" {
		t.Fatalf("首次存储应为 created: %v", storeOut["action"])
	}

	searchOut := callTool(t, sess, "memory_search", map[string]any{"query": "用户会什么语言"})
	if searchOut["count"] == float64(0) {
		t.Fatalf("搜索应命中: %s", searchOut["_text"])
	}
	if !strings.Contains(searchOut["_text"].(string), "Go") {
		t.Fatalf("搜索文本应包含 Go: %s", searchOut["_text"])
	}
}

func TestDedupOverMCP(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	callTool(t, sess, "memory_store", map[string]any{"content": "部署前必须跑完整测试"})
	second := callTool(t, sess, "memory_store", map[string]any{"content": "部署前必须跑完整测试!"})
	if second["action"] != "updated" {
		t.Fatalf("近重复内容应合并为 updated: %s", second["_text"])
	}
	// 合并可审计：结构化输出携带被覆盖的旧记忆
	prev, ok := second["previous"].(map[string]any)
	if !ok {
		t.Fatalf("updated 应携带 previous: %v", second)
	}
	if prev["content"] != "部署前必须跑完整测试" {
		t.Fatalf("previous.content 应为被覆盖的旧内容: %v", prev["content"])
	}
	stats := callTool(t, sess, "memory_stats", map[string]any{})
	if stats["total"] != float64(1) {
		t.Fatalf("去重后应为 1 条: %s", stats["_text"])
	}
}

func TestForgetAndDeleteOverMCP(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	callTool(t, sess, "memory_store", map[string]any{"content": "临时密钥 sk-temp-111 已失效"})
	// 不传 threshold，验证哈希提供者的自适应默认阈值（0.5）
	forgetOut := callTool(t, sess, "memory_forget", map[string]any{"query": "临时密钥 sk-temp-111 已经失效"})
	if forgetOut["deleted"] != float64(1) {
		t.Fatalf("遗忘应删除 1 条: %s", forgetOut["_text"])
	}

	storeOut := callTool(t, sess, "memory_store", map[string]any{"content": "保留这条记忆"})
	memID := storeOut["memory"].(map[string]any)["id"].(string)
	delOut := callTool(t, sess, "memory_delete", map[string]any{"ids": []string{memID}})
	if delOut["deleted"] != float64(1) {
		t.Fatalf("按 ID 删除应成功: %s", delOut["_text"])
	}
}

func TestListGetStatsOverMCP(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	stored := callTool(t, sess, "memory_store", map[string]any{"content": "列出并读取我", "kind": "fact"})
	if stored["action"] != "created" {
		t.Fatalf("存储应为 created: %s", stored["_text"])
	}
	memID := stored["memory"].(map[string]any)["id"].(string)

	listOut := callTool(t, sess, "memory_list", map[string]any{})
	if listOut["count"] != float64(1) {
		t.Fatalf("列表应有 1 条: %s", listOut["_text"])
	}
	getOut := callTool(t, sess, "memory_get", map[string]any{"id": memID})
	if getOut["content"] != "列出并读取我" {
		t.Fatalf("按 ID 读取失败: %s", getOut["_text"])
	}
	statsOut := callTool(t, sess, "memory_stats", map[string]any{})
	if statsOut["total"] != float64(1) {
		t.Fatalf("统计应为 1 条: %s", statsOut["_text"])
	}
}

func TestToolErrorFeedback(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	// 空内容 → 工具级错误（IsError），而不是协议崩溃
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "memory_store", Arguments: map[string]any{"content": "  "},
	})
	if err != nil {
		t.Fatalf("协议层不应报错: %v", err)
	}
	if !res.IsError {
		t.Fatalf("空内容应触发工具错误: %+v", res.Content)
	}
}
