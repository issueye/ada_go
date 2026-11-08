package corerpc_test

// 失败原因必须能查到：引擎在轮次内部失败时只返回终态原因（err 为 nil），
// turn.start 的等待者拿不到 *domain.Error，错误码与详情只能靠事件收敛。
// 这里钉住 corerpc 把它们带进 turn.get/thread.get 的那一段。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// failingModel 在建立流时直接返回供应商错误（模拟上游不可用/鉴权失败）。
type failingModel struct{}

func (failingModel) Stream(context.Context, ports.Request) (ports.Stream, error) {
	return nil, domain.NewError(domain.CodeProviderError, "上游不可用: All connection attempts failed")
}

func TestRunErrorCodeAndDetailExposed(t *testing.T) {
	sess := newSession(t, failingModel{})
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")
	threadID := createThread(t, c, "供应商失败")

	c.call(t, "turn.start", map[string]any{
		"threadId":       threadID,
		"idempotencyKey": "run-error-001",
		"text":           "继续",
	})
	c.waitEvent(t, "evt.run.status", `"terminal":"error"`)

	var state struct {
		Run struct {
			Status      string `json:"status"`
			Terminal    string `json:"terminal"`
			ErrorCode   int    `json:"errorCode"`
			ErrorDetail string `json:"errorDetail"`
		} `json:"run"`
	}
	// 等 corerpc 把内存态收敛到 terminal：终态事件由引擎在 finish 里先发，
	// 登记 running/terminal 的收尾在 turn.start 的 goroutine 里稍后完成。
	deadline := time.Now().Add(2 * time.Second)
	var lastRaw json.RawMessage
	for {
		raw := c.call(t, "turn.get", map[string]any{"threadId": threadID})
		lastRaw = raw
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("turn.get 解析失败: %v (%s)", err, raw)
		}
		if state.Run.Status == "terminal" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("轮次未收敛到 terminal: %s", raw)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state.Run.Terminal != string(domain.TerminalError) {
		t.Fatalf("terminal = %q，期望 error", state.Run.Terminal)
	}
	if state.Run.ErrorCode != int(domain.CodeProviderError) {
		t.Fatalf("errorCode = %d，期望 %d（供应商错误）；响应 %s",
			state.Run.ErrorCode, int(domain.CodeProviderError), lastRaw)
	}
	if !strings.Contains(state.Run.ErrorDetail, "上游不可用") {
		t.Fatalf("errorDetail = %q，期望包含上游错误原文", state.Run.ErrorDetail)
	}

	// thread.get 是会话页的数据源，同一份码与详情必须也在这里。
	raw := c.call(t, "thread.get", map[string]any{"threadId": threadID, "itemLimit": 10})
	if !strings.Contains(string(raw), `"errorCode":5001`) {
		t.Fatalf("thread.get 没有带出错误码: %s", raw)
	}
	if !strings.Contains(string(raw), "上游不可用") {
		t.Fatalf("thread.get 没有带出错误详情: %s", raw)
	}
}
