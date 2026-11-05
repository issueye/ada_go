package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"adagodesign/internal/app"
	"adagodesign/internal/canvas"
)

// newTestDeps 组装内存态依赖（不落盘）。
func newTestDeps(t *testing.T) Deps {
	t.Helper()
	store, err := canvas.OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	return Deps{
		Store:   store,
		Tasks:   canvas.NewTaskQueue(),
		BaseURL: func() string { return "http://127.0.0.1:59999" },
		DataDir: t.TempDir(),
	}
}

// newTestSession 建立一条内存 MCP 会话（工具挂载内存态依赖）。
func newTestSession(t *testing.T) (*mcp.ClientSession, Deps) {
	t.Helper()
	d := newTestDeps(t)
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := NewServer(d)
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("客户端连接失败: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess, d
}

// callTool 走真实 MCP 会话调用工具并返回 CallToolResult。
func callTool(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func contentText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb bytes.Buffer
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolsListed(t *testing.T) {
	sess, _ := newTestSession(t)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"design_systems": false, "design_tokens": false, "design_components": false,
		"design_preview": false, "canvas_add": false, "canvas_list": false,
		"canvas_move": false, "canvas_remove": false, "canvas_clear": false,
		"canvas_screenshot": false, "canvas_url": false,
		"design_compare": false, "design_screen": false, "design_search": false,
		"canvas_arrange": false, "canvas_restore": false, "canvas_export": false,
	}
	for _, tool := range tools.Tools {
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

func TestDesignSystemsAndTokens(t *testing.T) {
	sess, _ := newTestSession(t)
	res := callTool(t, sess, "design_systems", nil)
	if res.IsError {
		t.Fatalf("design_systems 失败: %s", contentText(t, res))
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("element")) {
		t.Fatal("结果应含 element 体系")
	}

	res = callTool(t, sess, "design_tokens", map[string]any{"system": "element", "category": "color"})
	if res.IsError {
		t.Fatalf("design_tokens 失败: %s", contentText(t, res))
	}
	// 结构化输出校验
	raw, _ := json.Marshal(res.StructuredContent)
	var out tokensOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content 解析失败: %v (%s)", err, raw)
	}
	if out.System != "element" || out.Theme != "light" {
		t.Fatalf("tokens out = %+v", out)
	}
	colors, ok := out.Content["colors"].(map[string]any)
	if !ok || len(colors) < 10 {
		t.Fatalf("颜色不足: %v", out.Content)
	}
	if primary, ok := colors["primary"].(map[string]any); !ok || primary["value"] != "#409EFF" {
		t.Fatalf("element primary 异常: %v", colors["primary"])
	}

	// dark 主题
	res = callTool(t, sess, "design_tokens", map[string]any{"system": "volcano", "theme": "dark", "category": "color"})
	if res.IsError {
		t.Fatalf("volcano dark 失败: %s", contentText(t, res))
	}
	// 未知体系
	res = callTool(t, sess, "design_tokens", map[string]any{"system": "nope"})
	if !res.IsError {
		t.Fatal("未知体系应 isError")
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("unknown_system")) {
		t.Fatalf("应含稳定错误码: %s", contentText(t, res))
	}
}

func TestDesignComponentsAndPreview(t *testing.T) {
	sess, d := newTestSession(t)
	res := callTool(t, sess, "design_components", map[string]any{"system": "apple"})
	if res.IsError {
		t.Fatalf("design_components 失败: %s", contentText(t, res))
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("button")) {
		t.Fatal("组件清单应含 button")
	}

	res = callTool(t, sess, "design_components", map[string]any{"system": "apple", "component": "button"})
	if res.IsError {
		t.Fatalf("单组件失败: %s", contentText(t, res))
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("ds-btn--primary")) {
		t.Fatal("单组件应含 HTML 片段")
	}

	// 预览 + 入画布
	before := len(d.Store.List())
	res = callTool(t, sess, "design_preview", map[string]any{
		"system": "douyin", "components": []string{"navbar", "card", "tabbar"},
		"title": "抖音订单页", "theme": "dark",
	})
	if res.IsError {
		t.Fatalf("design_preview 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out previewOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("preview 结构化输出解析失败: %v (%s)", err, raw)
	}
	if out.NodeID == "" || out.CanvasURL == "" {
		t.Fatalf("preview 未入画布: %+v", out)
	}
	if len(d.Store.List()) != before+1 {
		t.Fatalf("画布节点数应 +1, got %d", len(d.Store.List()))
	}
	if out.ExportPath == "" {
		t.Fatal("应导出 HTML 文件")
	}
	// 导出的 HTML 是完整文档
	node, _ := d.Store.Get(out.NodeID)
	if !bytes.Contains([]byte(node.HTML), []byte("<!DOCTYPE html>")) {
		t.Fatal("节点 HTML 应为完整文档")
	}
	if !bytes.Contains([]byte(node.HTML), []byte("douyin skin")) {
		t.Fatal("节点 HTML 应内嵌 douyin 皮肤 CSS")
	}
}

func TestCanvasToolsFlow(t *testing.T) {
	sess, d := newTestSession(t)
	// add
	res := callTool(t, sess, "canvas_add", map[string]any{
		"title": "自绘卡片", "html": "<!DOCTYPE html><html><body><p>hello</p></body></html>",
		"width": 300, "height": 200, "tags": []string{"demo"},
	})
	if res.IsError {
		t.Fatalf("canvas_add 失败: %s", contentText(t, res))
	}
	nodes := d.Store.List()
	if len(nodes) != 1 {
		t.Fatalf("节点数 = %d", len(nodes))
	}
	id := nodes[0].ID
	// list
	res = callTool(t, sess, "canvas_list", map[string]any{"tag": "demo"})
	if res.IsError || !bytes.Contains([]byte(contentText(t, res)), []byte(id)) {
		t.Fatalf("canvas_list 未找到节点: %s", contentText(t, res))
	}
	// list 过滤空
	res = callTool(t, sess, "canvas_list", map[string]any{"system": "nope"})
	if res.IsError || !bytes.Contains([]byte(contentText(t, res)), []byte("没有匹配")) {
		t.Fatalf("空过滤应提示: %s", contentText(t, res))
	}
	// move
	res = callTool(t, sess, "canvas_move", map[string]any{"id": id, "x": 500.0, "y": 260.0})
	if res.IsError {
		t.Fatalf("canvas_move 失败: %s", contentText(t, res))
	}
	n, _ := d.Store.Get(id)
	if n.X != 500 || n.Y != 260 {
		t.Fatalf("move 未生效: %+v", n)
	}
	// remove
	res = callTool(t, sess, "canvas_remove", map[string]any{"id": id})
	if res.IsError {
		t.Fatalf("canvas_remove 失败: %s", contentText(t, res))
	}
	// clear 需确认
	res = callTool(t, sess, "canvas_clear", nil)
	if !res.IsError || !bytes.Contains([]byte(contentText(t, res)), []byte("confirm_required")) {
		t.Fatalf("未确认清空应报 confirm_required: %s", contentText(t, res))
	}
	// add 错误路径
	res = callTool(t, sess, "canvas_add", map[string]any{"title": "空", "html": " "})
	if !res.IsError || !bytes.Contains([]byte(contentText(t, res)), []byte("bad_request")) {
		t.Fatalf("空 html 应报 bad_request: %s", contentText(t, res))
	}
}

func TestCanvasScreenshotTimeout(t *testing.T) {
	sess, d := newTestSession(t)
	ctx := context.Background()

	// 加一个节点，然后截图（无 UI 领取 → capture_timeout）
	d.Store.Add(canvas.Node{Title: "x", HTML: "<html></html>", Width: 100, Height: 100})
	nodes := d.Store.List()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "canvas_screenshot",
		Arguments: map[string]any{"node_id": nodes[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("无 UI 时截图应 isError")
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("capture_timeout")) {
		t.Fatalf("应报 capture_timeout: %s", contentText(t, res))
	}
}

func TestCanvasScreenshotSuccess(t *testing.T) {
	sess, d := newTestSession(t)
	ctx := context.Background()

	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	png := append(append([]byte{}, pngMagic...), []byte("fakepngbytes")...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			task := d.Tasks.Claim(ctx)
			if task == nil {
				return
			}
			d.Tasks.Complete(task.ID, canvas.CaptureResult{
				PNGBase64: base64.StdEncoding.EncodeToString(png),
			})
		}
	}()
	t.Cleanup(func() { <-done })

	d.Store.Add(canvas.Node{Title: "x", HTML: "<html></html>", Width: 100, Height: 100})
	nodes := d.Store.List()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "canvas_screenshot",
		Arguments: map[string]any{"node_id": nodes[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("有 UI 时截图应成功: %s", contentText(t, res))
	}
	var img *mcp.ImageContent
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			img = ic
			break
		}
	}
	if img == nil {
		t.Fatal("结果应含 ImageContent")
	}
	if !bytes.HasPrefix(img.Data, pngMagic) {
		t.Fatalf("ImageContent.Data 应为原始 PNG 字节（魔数校验），got %x", img.Data[:8])
	}
	if img.MIMEType != "image/png" {
		t.Fatalf("MIMEType = %s", img.MIMEType)
	}
}

func TestCatalogBridgeMatchesCanvas(t *testing.T) {
	// app.Catalog 的 ComposePreview 与 design.Compose 输出一致
	cat := app.Catalog{}
	html, w, h, err := cat.ComposePreview("element", []string{"button", "input"}, "light", "桥接测试")
	if err != nil {
		t.Fatal(err)
	}
	if w != 420 || !bytes.Contains([]byte(html), []byte("element skin")) {
		t.Fatalf("桥接预览异常: w=%d", w)
	}
	_, ok := cat.SystemDetail("xiaomi")
	if !ok {
		t.Fatal("xiaomi 详情缺失")
	}
	if _, ok := cat.SystemDetail("nope"); ok {
		t.Fatal("未知体系应 false")
	}
	if h <= 0 {
		t.Fatal("高度应>0")
	}
}
