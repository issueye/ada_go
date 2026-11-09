package app_test

// cron-manager 管理面：路由形状（create/update 的请求体不同，update 的任务定义
// 嵌在 { id, task } 里）、立即运行走调度层、调度进程注册、以及管理工具是 discovery 形态。
//
// 这里钉住管理页实际提交的形状，避免“保存失败: 需要任务名”。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
)

// cronProduct 是 cron-manager 的最小装配集合：它注册 write/exec 工具，
// 所以必须同时装配审批策略插件（否则装配期直接拒绝，见 5.4 失败关闭）。
func cronProduct(t *testing.T) *coreapp.Product {
	t.Helper()
	return testProduct(t, "cron-manager", "policy-default")
}

// cronHarness 记录 stub 掉的调度/进程桥调用。
type cronHarness struct {
	dispatched []host.DispatchRequest
	registered []host.ProcessSpec
	ensured    []string
	procs      host.ProcessSupervisor
}

func newCronHarness(t *testing.T) *cronHarness {
	t.Helper()
	h := &cronHarness{procs: &stubSupervisor{h: nil}}
	h.procs = &stubSupervisor{h: h}
	return h
}

// stubSupervisor 实现 host.ProcessSupervisor：注册时记一笔并返回运行中状态。
type stubSupervisor struct{ h *cronHarness }

func (s *stubSupervisor) Register(_ context.Context, pluginID string, spec host.ProcessSpec) (host.ProcessStatus, error) {
	if s.h != nil {
		s.h.registered = append(s.h.registered, spec)
	}
	return host.ProcessStatus{
		ID: pluginID + "/" + spec.Name, Owner: pluginID, Name: spec.Name, Scope: spec.Scope,
		Runtime: spec.Runtime, PID: 1234, Running: true, Refs: 1,
		StartMode: spec.Start, Restart: spec.Restart,
	}, nil
}

func (s *stubSupervisor) Ensure(_ context.Context, pluginID, name, _ string) (host.ProcessStatus, error) {
	if s.h != nil {
		s.h.ensured = append(s.h.ensured, name)
	}
	return host.ProcessStatus{ID: pluginID + "/" + name, Owner: pluginID, Name: name, Running: true, PID: 1234}, nil
}

func (s *stubSupervisor) Stop(_ context.Context, pluginID, name, _ string) (host.ProcessStatus, error) {
	return host.ProcessStatus{ID: pluginID + "/" + name, Owner: pluginID, Name: name}, nil
}

func (s *stubSupervisor) Status(_ context.Context, pluginID, name, _ string) (host.ProcessStatus, bool) {
	return host.ProcessStatus{ID: pluginID + "/" + name, Owner: pluginID, Name: name, Running: true, PID: 1234}, true
}

func (s *stubSupervisor) Unregister(_ context.Context, _, _, _ string) error { return nil }

func (s *stubSupervisor) List(_ context.Context, pluginID string) ([]host.ProcessStatus, error) {
	return []host.ProcessStatus{{ID: pluginID + "/scheduler", Owner: pluginID, Name: "scheduler"}}, nil
}

// TestCronManagerUpdateRouteReadsNestedTask 钉住管理页的 create/update 形状与
// 「立即运行」走 ctx.dispatch（不再走 turn.invoke）。
func TestCronManagerUpdateRouteReadsNestedTask(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	ctx := context.Background()
	h := newCronHarness(t)
	asm := assemble(t, cronProduct(t), host.Context{
		Workspace: t.TempDir(),
		ConfigDir: t.TempDir(),
		Dispatch: func(_ context.Context, req host.DispatchRequest) (host.DispatchResult, error) {
			h.dispatched = append(h.dispatched, req)
			return host.DispatchResult{Workspace: "/ws", ThreadID: "th_cron", RunID: "run_cron", Mode: "start"}, nil
		},
		Processes: h.procs,
	}, nil)

	// 装配期就该注册单例调度进程（on_enable）。
	if len(h.registered) != 1 {
		t.Fatalf("装配期应注册 1 个调度进程，实际 %+v", h.registered)
	}
	spec := h.registered[0]
	// entry 由宿主解析成插件目录内的绝对路径（相对路径不出宿主桥）。
	if !filepath.IsAbs(spec.Entry) || filepath.Base(spec.Entry) != "scheduler.js" {
		t.Fatalf("entry 应是插件目录内的绝对路径，实得 %q", spec.Entry)
	}
	if !strings.Contains(filepath.ToSlash(spec.Entry), "cron-manager/dist/scheduler.js") {
		t.Fatalf("entry 应落在 cron-manager/dist 下，实得 %q", spec.Entry)
	}
	if spec.Name != "scheduler" || spec.Scope != "global" || spec.Runtime != "node" ||
		spec.Start != "on_enable" || spec.Restart != "on_failure" {
		t.Fatalf("调度进程 spec = %+v", spec)
	}

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

	// 「立即运行」走调度层，并写一条 started 历史。
	if _, err := asm.CallHTTPAPI(ctx, "cron-manager", "POST", "/trigger",
		json.RawMessage(`{"method":"POST","path":"/trigger","body":{"id":"`+createRes.ID+`"}}`), ""); err != nil {
		t.Fatalf("POST /trigger: %v", err)
	}
	if len(h.dispatched) != 1 {
		t.Fatalf("trigger 应经调度层投递一次，实际 %d", len(h.dispatched))
	}
	got := h.dispatched[0]
	if got.Text != "获取当天热点新闻" || got.Title != "新闻" || got.Mode != "start" || got.TaskID != createRes.ID {
		t.Fatalf("投递请求 = %+v", got)
	}
	if strings.Contains(got.IdempotencyKey, "") && got.IdempotencyKey == "" {
		t.Fatal("立即运行也要带幂等键，避免重复点击触发两轮")
	}
	if got.ThreadID != "" {
		t.Fatalf("首次触发应让调度层建线程，threadId = %q", got.ThreadID)
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
	for _, want := range []string{`"name":"新闻（改）"`, `"cron":"0 9 * * *"`, `"text":"改成早报"`, `"scheduler"`} {
		if !strings.Contains(string(stateRaw), want) {
			t.Fatalf("更新未落盘或缺调度器状态，%s: %s", want, stateRaw)
		}
	}

	// 旧形状（把任务字段平铺在顶层）应给出明确错误，而不是静默成功。
	if _, err := asm.CallHTTPAPI(ctx, "cron-manager", "POST", "/update",
		json.RawMessage(`{"method":"POST","path":"/update","body":{"id":"`+createRes.ID+`","name":"平铺","cron":"0 9 * * *","action":{"type":"agent","text":"x"}}}`), ""); err == nil {
		t.Fatal("缺少 task 时应报错")
	}
}

// TestCronManagerToolsAreDiscovery：管理工具不进模型默认清单，全部 discovery；
// access 按语义给（读=read、改=write、立即执行=exec）。
func TestCronManagerToolsAreDiscovery(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	asm := assemble(t, cronProduct(t), host.Context{
		Workspace: t.TempDir(),
		ConfigDir: t.TempDir(),
	}, nil)

	want := map[string]domain.Access{
		"cron_list":        domain.AccessRead,
		"cron_get":         domain.AccessRead,
		"cron_create":      domain.AccessWrite,
		"cron_update":      domain.AccessWrite,
		"cron_delete":      domain.AccessWrite,
		"cron_set_enabled": domain.AccessWrite,
		"cron_run":         domain.AccessExec,
	}
	seen := map[string]bool{}
	for _, inst := range asm.Plugins {
		if inst.ID() != "cron-manager" {
			continue
		}
		for _, td := range inst.Contribution().Tools {
			access, ok := want[td.Name]
			if !ok {
				continue
			}
			seen[td.Name] = true
			if !td.Discovery {
				t.Fatalf("%s 必须是 discovery=true（不进模型默认清单）", td.Name)
			}
			got, ok := asm.ToolManager().Lookup(context.Background(), td.Name)
			if !ok {
				t.Fatalf("%s 没有进工具目录", td.Name)
			}
			if got.Access != access {
				t.Fatalf("%s 的 access = %q，期望 %q", td.Name, got.Access, access)
			}
			if got.TerminatesTurn {
				t.Fatalf("%s 不应终止轮次", td.Name)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("缺少工具 %s", name)
		}
	}

	// 注入式视图（模型默认清单）里不应出现任何 cron_* 工具。
	for _, d := range asm.ToolManager().View(context.Background(), domain.ToolVisibleInjected) {
		if strings.HasPrefix(d.Name, "cron_") {
			t.Fatalf("%s 不应进模型默认工具清单", d.Name)
		}
	}
	// 发现式视图里能查到。
	discovery := asm.ToolManager().View(context.Background(), domain.ToolVisibleDiscovery)
	found := false
	for _, d := range discovery {
		if d.Name == "cron_list" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cron_list 应在发现式视图里")
	}
}
