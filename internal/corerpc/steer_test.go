package corerpc_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/conformance/testkit"
)

// TestTurnSteerQueuesDuringRunningTurn 用阻塞模型验证 turn.steer：
// 运行中入队返回 position；轮次结束后不再接受；未排空队列在结束时可观测为 0。
func TestTurnSteerQueuesDuringRunningTurn(t *testing.T) {
	steps := append(testkit.Text("等待中"), testkit.Idle(250*time.Millisecond))
	steps = append(steps, testkit.Finish("stop", "[DONE]"))
	model := testkit.NewFakeProvider(testkit.NewScriptedModel(steps))
	sess := newSession(t, model)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")
	threadID := createThread(t, c, "steer")

	// 没有运行中轮次：必须明确失败（core_unavailable 1002），而不是静默入队。
	resp := c.callError(t, "turn.steer", map[string]any{"threadId": threadID, "text": "还没开始"})
	if !strings.Contains(string(resp.Error), `"code":1002`) {
		t.Fatalf("无运行中轮次应返回 core_unavailable: %s", resp.Error)
	}

	c.call(t, "turn.start", map[string]any{
		"threadId": threadID, "idempotencyKey": "steer-0001", "text": "开始",
	})
	c.waitEvent(t, "evt.run.status", `"status":"running"`)

	var queued struct {
		Queued   bool `json:"queued"`
		Position int  `json:"position"`
	}
	raw := c.call(t, "turn.steer", map[string]any{"threadId": threadID, "text": "中途补充"})
	if err := json.Unmarshal(raw, &queued); err != nil || !queued.Queued || queued.Position != 1 {
		t.Fatalf("turn.steer = %s (%v)，期望 queued=true position=1", raw, err)
	}
	raw = c.call(t, "turn.steer", map[string]any{"threadId": threadID, "text": "第二条"})
	if err := json.Unmarshal(raw, &queued); err != nil || !queued.Queued || queued.Position != 2 {
		t.Fatalf("第二次 turn.steer = %s (%v)，期望 position=2", raw, err)
	}

	var state struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
		QueueLength int `json:"queueLength"`
	}
	raw = c.call(t, "turn.get", map[string]any{"threadId": threadID})
	if err := json.Unmarshal(raw, &state); err != nil || state.QueueLength != 2 {
		t.Fatalf("turn.get queueLength = %s (%v)，期望 2", raw, err)
	}

	c.waitEvent(t, "evt.run.status", `"terminal":"completed"`)

	// 等 corerpc 把 runs 表收敛到 terminal，再断言结束后的 steer 失败。
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw = c.call(t, "turn.get", map[string]any{"threadId": threadID})
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("turn.get = %s (%v)", raw, err)
		}
		if state.Run.Status == "terminal" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("轮次未收敛到 terminal: %s", raw)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state.QueueLength != 0 {
		t.Fatalf("轮次结束后 queueLength = %d，未排空消息应被丢弃", state.QueueLength)
	}
	resp = c.callError(t, "turn.steer", map[string]any{"threadId": threadID, "text": "结束后"})
	if !strings.Contains(string(resp.Error), `"code":1002`) {
		t.Fatalf("轮次结束后应返回 core_unavailable: %s", resp.Error)
	}
}

// TestTurnSteerRejectsInvalidParams 覆盖参数校验：空 text、图片输入。
func TestTurnSteerRejectsInvalidParams(t *testing.T) {
	steps := append(testkit.Text("等待"), testkit.Idle(250*time.Millisecond))
	steps = append(steps, testkit.Finish("stop", "[DONE]"))
	model := testkit.NewFakeProvider(testkit.NewScriptedModel(steps))
	sess := newSession(t, model)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")
	threadID := createThread(t, c, "steer-invalid")

	c.call(t, "turn.start", map[string]any{
		"threadId": threadID, "idempotencyKey": "steer-bad-1", "text": "开始",
	})
	c.waitEvent(t, "evt.run.status", `"status":"running"`)

	c.callError(t, "turn.steer", map[string]any{"threadId": threadID, "text": "   "})
	c.callError(t, "turn.steer", map[string]any{
		"threadId": threadID, "text": "带图", "images": []any{map[string]any{"type": "image"}},
	})
	c.callError(t, "turn.steer", map[string]any{"threadId": "", "text": "没有线程"})

	c.call(t, "turn.cancel", map[string]any{"threadId": threadID})
}
