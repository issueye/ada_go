package corerpc_test

// 会话页的 token 环点开要看得到「花在哪」：thread.get 的 context 必须带出
// 系统提示词 / 工具定义 / 对话消息三段，且 tokens 是三者之和（占比以它为准）。

import (
	"encoding/json"
	"testing"
)

func TestThreadGetContextBreakdown(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")
	threadID := createThread(t, c, "分项统计")

	raw := c.call(t, "thread.get", map[string]any{"threadId": threadID, "itemLimit": 10})
	var got struct {
		Context struct {
			Tokens   int `json:"tokens"`
			Messages int `json:"messages"`
			System   int `json:"system"`
			Tools    int `json:"tools"`
			Window   int `json:"window"`
			Percent  int `json:"percent"`
		} `json:"context"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("thread.get 解析失败: %v (%s)", err, raw)
	}
	ctx := got.Context
	// 安全前言至少几百 token；空线程没有消息，但系统提示与工具声明不会为 0。
	if ctx.System <= 0 {
		t.Fatalf("system = %d，期望 > 0（安全前言应当计入）", ctx.System)
	}
	if ctx.Tools <= 0 {
		t.Fatalf("tools = %d，期望 > 0（随包工具声明应当计入）", ctx.Tools)
	}
	if ctx.Messages != 0 {
		t.Fatalf("空线程 messages = %d，期望 0", ctx.Messages)
	}
	if ctx.Tokens != ctx.Messages+ctx.System+ctx.Tools {
		t.Fatalf("tokens = %d，期望三段之和 %d", ctx.Tokens, ctx.Messages+ctx.System+ctx.Tools)
	}
	if ctx.Window <= 0 || ctx.Percent <= 0 {
		t.Fatalf("window = %d, percent = %d，期望都 > 0", ctx.Window, ctx.Percent)
	}
}
