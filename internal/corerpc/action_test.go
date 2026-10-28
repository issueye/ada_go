package corerpc_test

// action.invoke 的数据面验收：ui-task-plan 的 thread.tray 绑定指向 tools-plan 的
// plan 读动作； Gateway 渲染桥预取的就是这条路（M4，7.5）。

import (
	"encoding/json"
	"testing"
)

func TestActionInvokePlanAction(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	var created struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	raw := c.call(t, "thread.create", map[string]any{"title": "action"})
	if err := json.Unmarshal(raw, &created); err != nil || created.Thread.ID == "" {
		t.Fatalf("thread.create = %s", raw)
	}

	// 未知动作：ok=false + 人话错误（不是 RPC 异常）。
	raw = c.call(t, "action.invoke", map[string]any{
		"pluginId": "tools-plan", "action": "nope",
	})
	var miss struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &miss); err != nil || miss.OK {
		t.Fatalf("未知动作 = %s (%v)", raw, err)
	}
	if miss.Error == "" {
		t.Fatal("ok=false 必须带 error")
	}

	// plan 读动作：空计划 total=0（前端据此不画卡）。
	raw = c.call(t, "action.invoke", map[string]any{
		"pluginId": "tools-plan", "action": "plan", "threadId": created.Thread.ID,
	})
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			Plan struct {
				Total int `json:"total"`
			} `json:"plan"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || !res.OK {
		t.Fatalf("plan 动作 = %s (%v)", raw, err)
	}
	if res.Result.Plan.Total != 0 {
		t.Fatalf("空计划 total = %d", res.Result.Plan.Total)
	}

	// 参数缺失：invalid_params。
	resp := c.callError(t, "action.invoke", map[string]any{"action": "plan"})
	if resp.Error == nil {
		t.Fatal("缺参必须返回 error")
	}
	var rpcErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(resp.Error, &rpcErr); err != nil || rpcErr.Code != -32602 {
		t.Fatalf("缺参必须 invalid_params: %s", resp.Error)
	}
}
