package mcpserver

import (
	"strings"
	"testing"

	"adagodesign/internal/canvas"
)

// canvas_resync_artboards：显式画板重同步的 MCP 契约。
func TestResyncArtboardsTool(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)
	n := seedNode(t, d, canvas.Node{
		Title: "组件", Kind: canvas.KindComponent, Artboard: "iphone-15",
		Width: 420, Height: 320, HTML: "<p>x</p>",
	})

	res := callTool(t, sess, "canvas_resync_artboards", map[string]any{"project": pid})
	if res.IsError {
		t.Fatalf("工具失败: %s", contentText(t, res))
	}
	txt := contentText(t, res)
	if !strings.Contains(txt, "393×320") {
		t.Fatalf("文本缺少调整结果: %s", txt)
	}
	got, err := d.Store.Get(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 393 || got.Height != 320 {
		t.Fatalf("组件尺寸 = %dx%d，期望 393x320（高度保持客户端实测）", got.Width, got.Height)
	}

	// 再调一次应幂等：没有需要调整的节点
	res = callTool(t, sess, "canvas_resync_artboards", map[string]any{"project": pid})
	if res.IsError || !strings.Contains(contentText(t, res), "没有需要调整") {
		t.Fatalf("幂等调用异常: %s", contentText(t, res))
	}

	// project 省略 = 全部项目
	res = callTool(t, sess, "canvas_resync_artboards", map[string]any{})
	if res.IsError {
		t.Fatalf("全局调用失败: %s", contentText(t, res))
	}
}

func TestResyncArtboardsToolListed(t *testing.T) {
	sess, _ := newTestSession(t)
	tools, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "canvas_resync_artboards" {
			return
		}
	}
	t.Fatal("缺少工具 canvas_resync_artboards")
}
