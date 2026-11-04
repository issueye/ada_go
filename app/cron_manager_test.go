package app_test

// cron-manager 管理面路由：create/update 的请求形状不同，update 的任务定义
// 嵌在 { id, task } 里；这里钉住管理页实际提交的形状，避免“保存失败: 需要任务名”。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins/host"
)

func TestCronManagerUpdateRouteReadsNestedTask(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	ctx := context.Background()
	p := testProduct(t, "cron-manager")
	asm := assemble(t, p, host.Context{Workspace: t.TempDir()}, nil)

	createBody := json.RawMessage(`{"method":"POST","path":"/create","body":{
		"name": "新闻",
		"cron": "0 * * * *",
		"workspace": "",
		"enabled": true,
		"action": {"type": "agent", "text": "获取当天热点新闻"}
	}}`)
	created, err := asm.CallHTTPAPI(ctx, "cron-manager", "POST", "/create", createBody, "")
	if err != nil {
		t.Fatalf("POST /create: %v", err)
	}
	var createRes struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created, &createRes); err != nil || strings.TrimSpace(createRes.ID) == "" {
		t.Fatalf("create 返回缺少 id: %s (%v)", created, err)
	}

	// 管理页的“保存修改”就是 { id, task: {...} }。
	updateBody := json.RawMessage(`{"method":"POST","path":"/update","body":{
		"id": "` + createRes.ID + `",
		"task": {
			"name": "新闻（改）",
			"cron": "0 9 * * *",
			"workspace": "",
			"action": {"type": "agent", "text": "改成早报"}
		}
	}}`)
	if _, err := asm.CallHTTPAPI(ctx, "cron-manager", "POST", "/update", updateBody, ""); err != nil {
		t.Fatalf("POST /update: %v", err)
	}

	stateRaw, err := asm.CallHTTPAPI(ctx, "cron-manager", "GET", "/state", nil, "")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	for _, want := range []string{`"name":"新闻（改）"`, `"cron":"0 9 * * *"`, `"text":"改成早报"`} {
		if !strings.Contains(string(stateRaw), want) {
			t.Fatalf("更新未落盘，缺少 %s: %s", want, stateRaw)
		}
	}

	// 旧形状（把任务字段平铺在顶层）应给出明确错误，而不是静默成功。
	if _, err := asm.CallHTTPAPI(ctx, "cron-manager", "POST", "/update",
		json.RawMessage(`{"method":"POST","path":"/update","body":{"id":"`+createRes.ID+`","name":"平铺","cron":"0 9 * * *","action":{"type":"agent","text":"x"}}}`), ""); err == nil {
		t.Fatal("缺少 task 时应报错")
	}
}
