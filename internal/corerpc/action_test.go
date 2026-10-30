package corerpc_test

// action.invoke 的数据面验收：tools-plan 前端段的 slot_chat_corner 绑定指向自身
// 的 plan 读动作； Gateway 渲染桥预取的就是这条路（M4，7.5）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

	// 计划文件按会话命名：写入 .adag/todo/TODO_<threadId>.json 后，plan 动作应读到它。
	todo := filepath.Join(sess.Workspace(), ".adag", "todo", "TODO_"+created.Thread.ID+".json")
	body := fmt.Sprintf(`{"threadId":%q,"steps":[{"step":"写文件","status":"done"},{"step":"读卡片","status":"pending"}]}`, created.Thread.ID)
	if err := os.MkdirAll(filepath.Dir(todo), 0o755); err != nil {
		t.Fatalf("建 TODO 目录: %v", err)
	}
	if err := os.WriteFile(todo, []byte(body), 0o644); err != nil {
		t.Fatalf("写 TODO 文件: %v", err)
	}
	raw = c.call(t, "action.invoke", map[string]any{
		"pluginId": "tools-plan", "action": "plan", "threadId": created.Thread.ID,
	})
	var filled struct {
		OK     bool `json:"ok"`
		Result struct {
			Plan struct {
				Done  int `json:"done"`
				Total int `json:"total"`
			} `json:"plan"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &filled); err != nil || !filled.OK {
		t.Fatalf("读计划文件 = %s (%v)", raw, err)
	}
	if filled.Result.Plan.Total != 2 || filled.Result.Plan.Done != 1 {
		t.Fatalf("计划文件没有生效: %+v", filled.Result.Plan)
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
