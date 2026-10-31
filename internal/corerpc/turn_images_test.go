package corerpc_test

// 图片输入（7.7.4）：turn.start / turn.steer 的 images 参数（≤4 张，
// PNG/JPEG/WebP/GIF，单张 ≤8MB），双门校验（capabilities.images + 模型 input）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/conformance/testkit"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func TestTurnStartWithImages(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")

	threadID := createThread(t, c, "图片会话")
	raw := c.call(t, "turn.start", map[string]any{
		"threadId":       threadID,
		"idempotencyKey": "img-00000001",
		"text":           "看这张图",
		"images": []map[string]any{
			{"type": "image", "mime": "image/png", "data": tinyPNG},
		},
	})
	if strings.Contains(string(raw), "error") {
		t.Fatalf("turn.start = %s", raw)
	}
	c.waitEvent(t, "evt.run.status", `"terminal":"completed"`)

	raw = c.call(t, "thread.entries", map[string]any{"threadId": threadID, "limit": 10})
	if !strings.Contains(string(raw), `"type":"image"`) ||
		!strings.Contains(string(raw), "image/png") {
		t.Fatalf("会话条目缺少图片部件: %.600s", raw)
	}
}

func TestTurnStartImagesValidation(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")
	threadID := createThread(t, c, "图片校验")

	cases := []struct {
		name   string
		images []map[string]any
		want   string
	}{
		{"MIME 不支持", []map[string]any{
			{"type": "image", "mime": "image/bmp", "data": tinyPNG}}, "PNG/JPEG/WebP/GIF"},
		{"base64 非法", []map[string]any{
			{"type": "image", "mime": "image/png", "data": "!!!not-base64!!!"}}, "base64"},
		{"超过 4 张", []map[string]any{
			{"type": "image", "mime": "image/png", "data": tinyPNG},
			{"type": "image", "mime": "image/png", "data": tinyPNG},
			{"type": "image", "mime": "image/png", "data": tinyPNG},
			{"type": "image", "mime": "image/png", "data": tinyPNG},
			{"type": "image", "mime": "image/png", "data": tinyPNG}}, "最多 4 张"},
		{"空数据", []map[string]any{
			{"type": "image", "mime": "image/png", "data": ""}}, "内容为空"},
		{"非 image 部件", []map[string]any{
			{"type": "text", "text": "不是图片"}}, "image 部件"},
	}
	for _, tc := range cases {
		resp := c.callError(t, "turn.start", map[string]any{
			"threadId":       threadID,
			"idempotencyKey": "img-invalid-" + tc.name,
			"images":         tc.images,
		})
		if !strings.Contains(string(resp.Error), tc.want) {
			t.Fatalf("%s: err = %s, want 含 %q", tc.name, resp.Error, tc.want)
		}
	}
}

func TestTurnSteerWithImages(t *testing.T) {
	// drainSteering 只在工具批次之后、下一次模型请求之前触发（engine/steer.go）：
	// 用一次真实工具调用（读工作区文件）撑出排水窗口，steer 的图片才能落成 entry。
	steps := testkit.Text("等待中")
	steps = append(steps, testkit.Idle(500*time.Millisecond))
	steps = append(steps, testkit.ToolTurn("call_1", "read_file", `{"path":"note.txt"}`)...)
	steps = append(steps, testkit.Finish("stop", "[DONE]"))
	model := testkit.NewFakeProvider(testkit.NewScriptedModel(steps))
	sess := newSession(t, model)
	workspace := sess.Workspace()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 步序：文本段（running）→ Idle 500ms（等 steer 入队）→ 工具批次
	// （批次结束的排水点把 steer 落成 entry）→ finish 收尾。
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")
	threadID := createThread(t, c, "图片 steer")

	c.call(t, "turn.start", map[string]any{
		"threadId": threadID, "idempotencyKey": "steer-img-01", "text": "开始",
	})
	c.waitEvent(t, "evt.run.status", `"status":"running"`)

	raw := c.call(t, "turn.steer", map[string]any{
		"threadId": threadID,
		"text":     "中途补充一张图",
		"images":   []map[string]any{{"type": "image", "mime": "image/png", "data": tinyPNG}},
	})
	if strings.Contains(string(raw), "error") {
		t.Fatalf("turn.steer = %s", raw)
	}

	// 轮询终态；超时时把 turn.get 的状态带出来便于排查。
	var state struct {
		Run struct {
			Status      string `json:"status"`
			Terminal    string `json:"terminal"`
			ErrorCode   int    `json:"errorCode"`
			ErrorDetail string `json:"errorDetail"`
			QueueLength int    `json:"queueLength"`
		} `json:"run"`
	}
	for i := 0; i < 60; i += 1 {
		stRaw := c.call(t, "turn.get", map[string]any{"threadId": threadID})
		if err := json.Unmarshal(stRaw, &state); err != nil {
			t.Fatalf("turn.get = %s (%v)", stRaw, err)
		}
		if state.Run.Status == "terminal" || state.Run.Terminal != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if state.Run.Status != "terminal" && state.Run.Terminal == "" {
		t.Fatalf("轮次未收尾: %+v", state.Run)
	}
	raw = c.call(t, "thread.entries", map[string]any{"threadId": threadID, "limit": 20})
	if !strings.Contains(string(raw), `"type":"image"`) {
		t.Fatalf("steer 图片没有进入会话条目: %.600s", raw)
	}
}
