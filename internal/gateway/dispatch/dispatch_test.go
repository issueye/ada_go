package dispatch_test

// Dispatcher 是唯一发送路径：工作区解析、建线程、空闲 start / 运行中 steer、
// 以及 busy / waiting_input / finishing 这些分支都必须只有一份实现。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/dispatch"
)

type call struct {
	method string
	params map[string]any
}

// fakeCore 记录 RPC，并按剧本返回。
type fakeCore struct {
	calls    []call
	status   string
	threadID string
	steerErr error
	failWith map[string]error
}

func (c *fakeCore) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	p, _ := params.(map[string]any)
	c.calls = append(c.calls, call{method: method, params: p})
	if err, ok := c.failWith[method]; ok {
		return nil, err
	}
	switch method {
	case "thread.create":
		id := c.threadID
		if id == "" {
			id = "th_new"
		}
		return json.RawMessage(fmt.Sprintf(`{"thread":{"id":%q}}`, id)), nil
	case "turn.get":
		status := c.status
		return json.RawMessage(fmt.Sprintf(`{"run":{"status":%q}}`, status)), nil
	case "turn.start":
		return json.RawMessage(`{"run":{"id":"run_1"}}`), nil
	case "turn.steer":
		if c.steerErr != nil {
			return nil, c.steerErr
		}
		return json.RawMessage(`{"ok":true}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (c *fakeCore) Subscribe(int) (<-chan contracts.Event, func()) { return nil, func() {} }
func (c *fakeCore) Close() error                                   { return nil }

func (c *fakeCore) methods() []string {
	out := make([]string, 0, len(c.calls))
	for _, cl := range c.calls {
		out = append(out, cl.method)
	}
	return out
}

type fakeSupervisor struct {
	core     *fakeCore
	acquired []string
	released []string
	err      error
}

func (s *fakeSupervisor) Acquire(_ context.Context, workspace string) (contracts.CoreClient, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.acquired = append(s.acquired, workspace)
	return s.core, nil
}

func (s *fakeSupervisor) Release(workspace string)       { s.released = append(s.released, workspace) }
func (s *fakeSupervisor) Shutdown(context.Context) error { return nil }
func (s *fakeSupervisor) List() []contracts.WorkspaceStatus {
	return nil
}

// staticWorkspaces 是工作区解析面的测试替身。
type staticWorkspaces struct {
	def  string
	list []dispatch.Workspace
	bad  map[string]bool
}

func (w staticWorkspaces) Default() string            { return w.def }
func (w staticWorkspaces) List() []dispatch.Workspace { return w.list }
func (w staticWorkspaces) Resolve(path string) (string, bool) {
	if w.bad[path] {
		return "", false
	}
	for _, item := range w.list {
		if item.Path == path || item.Name == path {
			return item.Path, true
		}
	}
	return "", false
}

func newDispatcher(t *testing.T, core *fakeCore, ws dispatch.Workspaces) (*dispatch.Dispatcher, *fakeSupervisor) {
	t.Helper()
	sup := &fakeSupervisor{core: core}
	d, err := dispatch.New(dispatch.Config{Supervisor: sup, Workspaces: ws})
	if err != nil {
		t.Fatalf("dispatch.New: %v", err)
	}
	return d, sup
}

func twoWorkspaces() staticWorkspaces {
	return staticWorkspaces{
		def: "/ws/a",
		list: []dispatch.Workspace{
			{Name: "a", Path: "/ws/a"},
			{Name: "b", Path: "/ws/b"},
		},
	}
}

// 新建线程：thread.create 之后 turn.start，回执带线程与工作区。
func TestSendCreatesThreadAndStarts(t *testing.T) {
	t.Parallel()
	core := &fakeCore{}
	d, sup := newDispatcher(t, core, twoWorkspaces())

	got, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
		Workspace: "/ws/b", Title: "月度报告", Text: "帮我写报告",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.ThreadID != "th_new" || got.Workspace != "/ws/b" || got.Mode != dispatch.DeliveryStart {
		t.Fatalf("回执 = %+v", got)
	}
	if methods := core.methods(); len(methods) != 3 ||
		methods[0] != "thread.create" || methods[1] != "turn.get" || methods[2] != "turn.start" {
		t.Fatalf("RPC 顺序 = %v", methods)
	}
	if len(sup.released) != 1 || sup.released[0] != "/ws/b" {
		t.Fatalf("客户端必须成对释放: %v", sup.released)
	}
	if key, _ := core.calls[2].params["idempotencyKey"].(string); key == "" {
		t.Fatal("turn.start 必须带幂等键")
	}
}

// 运行中 + auto → steer；这是界面「运行中仍可输入」的语义。
func TestSendSteersRunningThread(t *testing.T) {
	t.Parallel()
	core := &fakeCore{status: "running"}
	d, _ := newDispatcher(t, core, twoWorkspaces())

	got, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
		ThreadID: "th_1", Text: "补充一句",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Mode != dispatch.DeliverySteer {
		t.Fatalf("模式 = %q，期望 steer", got.Mode)
	}
	if methods := core.methods(); len(methods) != 2 || methods[1] != "turn.steer" {
		t.Fatalf("RPC = %v", methods)
	}
}

// 运行中 + start（定时任务）→ thread_busy，不偷偷排进 steering 队列。
func TestSendStartRefusesBusyThread(t *testing.T) {
	t.Parallel()
	core := &fakeCore{status: "running"}
	d, _ := newDispatcher(t, core, twoWorkspaces())

	_, err := d.Send(context.Background(), dispatch.PluginSender("cron-manager", "task-1"), dispatch.SendRequest{
		ThreadID: "th_1", Text: "定时消息", Mode: dispatch.DeliveryStart,
	})
	if dispatch.CodeOf(err) != dispatch.CodeThreadBusy {
		t.Fatalf("err = %v，期望 thread_busy", err)
	}
	if methods := core.methods(); len(methods) != 1 || methods[0] != "turn.get" {
		t.Fatalf("busy 时不得再发 turn.steer/turn.start: %v", methods)
	}
}

// steer 在 turn.get 之后失败（轮次刚好结束）→ auto 退回 turn.start。
func TestSendFallsBackToStartWhenSteerLosesRace(t *testing.T) {
	t.Parallel()
	core := &fakeCore{status: "running", steerErr: errors.New("thread_not_running")}
	d, _ := newDispatcher(t, core, twoWorkspaces())

	got, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
		ThreadID: "th_1", Text: "还没结束吗",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Mode != dispatch.DeliveryStart {
		t.Fatalf("模式 = %q，期望退回 start", got.Mode)
	}
	methods := core.methods()
	if methods[len(methods)-1] != "turn.start" {
		t.Fatalf("RPC = %v", methods)
	}
}

// waiting_input / finishing 不能被新消息打断，也不排队。
func TestSendRefusesWaitingAndFinishing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status string
		code   string
	}{
		{"waiting_input", dispatch.CodeWaitingInput},
		{"finishing", dispatch.CodeFinishing},
	}
	for _, c := range cases {
		core := &fakeCore{status: c.status}
		d, _ := newDispatcher(t, core, twoWorkspaces())
		_, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
			ThreadID: "th_1", Text: "插话",
		})
		if dispatch.CodeOf(err) != c.code {
			t.Fatalf("状态 %s: err = %v，期望 %s", c.status, err, c.code)
		}
		for _, m := range core.methods() {
			if m == "turn.start" || m == "turn.steer" {
				t.Fatalf("状态 %s 不得投递: %v", c.status, core.methods())
			}
		}
	}
}

// 显式 steer 模式在空闲线程上失败，而不是悄悄 start。
func TestSendSteerModeRequiresRunningThread(t *testing.T) {
	t.Parallel()
	core := &fakeCore{status: ""}
	d, _ := newDispatcher(t, core, twoWorkspaces())

	_, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
		ThreadID: "th_1", Text: "追加", Mode: dispatch.DeliverySteer,
	})
	if dispatch.CodeOf(err) != dispatch.CodeNotRunning {
		t.Fatalf("err = %v，期望 not_running", err)
	}
}

// 工作区解析：空 → 默认；未知路径 → workspace_missing 且不取 Core。
func TestSendResolvesWorkspace(t *testing.T) {
	t.Parallel()
	core := &fakeCore{}
	ws := twoWorkspaces()
	ws.bad = map[string]bool{"/ws/b": true}
	d, sup := newDispatcher(t, core, ws)

	if _, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
		ThreadID: "th_1", Text: "你好",
	}); err != nil {
		t.Fatalf("默认工作区应可用: %v", err)
	}
	if len(sup.acquired) != 1 || sup.acquired[0] != "/ws/a" {
		t.Fatalf("取到的工作区 = %v", sup.acquired)
	}

	_, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
		Workspace: "/ws/b", ThreadID: "th_1", Text: "你好",
	})
	if dispatch.CodeOf(err) != dispatch.CodeWorkspaceMissing {
		t.Fatalf("err = %v，期望 workspace_missing", err)
	}
	if len(sup.acquired) != 1 {
		t.Fatalf("工作区不可用时不得取 Core: %v", sup.acquired)
	}
}

// 空消息在取 Core 之前就被拒。
func TestSendRejectsEmptyMessage(t *testing.T) {
	t.Parallel()
	core := &fakeCore{}
	d, sup := newDispatcher(t, core, twoWorkspaces())

	_, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{ThreadID: "th_1", Text: "   "})
	if dispatch.CodeOf(err) != dispatch.CodeEmptyMessage {
		t.Fatalf("err = %v，期望 empty_message", err)
	}
	if len(sup.acquired) != 0 {
		t.Fatalf("空消息不得取 Core: %v", sup.acquired)
	}
}

// 图片部件随 start 下发，且 images 与 text 同形。
func TestSendCarriesImages(t *testing.T) {
	t.Parallel()
	core := &fakeCore{}
	d, _ := newDispatcher(t, core, twoWorkspaces())

	_, err := d.Send(context.Background(), dispatch.UserSender(), dispatch.SendRequest{
		ThreadID: "th_1",
		Images:   []domain.Part{{Type: domain.PartImage, Mime: "image/png", Data: "AAAA"}},
	})
	if err != nil {
		t.Fatalf("只有图片也应可发送: %v", err)
	}
	images, ok := core.calls[1].params["images"].([]map[string]any)
	if !ok || len(images) != 1 || images[0]["mime"] != "image/png" {
		t.Fatalf("images = %#v", core.calls[1].params["images"])
	}
}

// 审计回调拿到发送者与结果（pluginId/taskId 是后续限流与归属的依据）。
func TestSendAuditsSender(t *testing.T) {
	t.Parallel()
	core := &fakeCore{}
	sup := &fakeSupervisor{core: core}
	var got []string
	d, err := dispatch.New(dispatch.Config{
		Supervisor: sup,
		Workspaces: twoWorkspaces(),
		Log: func(sender dispatch.Sender, _ dispatch.SendRequest, receipt dispatch.Receipt, sendErr error) {
			got = append(got, sender.PluginID+"/"+sender.TaskID+"@"+receipt.ThreadID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Send(context.Background(), dispatch.PluginSender("cron-manager", "task-9"),
		dispatch.SendRequest{ThreadID: "th_1", Text: "到点了", Mode: dispatch.DeliveryStart}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(got) != 1 || got[0] != "cron-manager/task-9@th_1" {
		t.Fatalf("审计 = %v", got)
	}
}

// 缺件时直接失败，不做隐式兜底。
func TestNewRequiresSupervisorAndWorkspaces(t *testing.T) {
	t.Parallel()
	if _, err := dispatch.New(dispatch.Config{Workspaces: twoWorkspaces()}); err == nil {
		t.Fatal("缺 Supervisor 应失败")
	}
	if _, err := dispatch.New(dispatch.Config{Supervisor: &fakeSupervisor{}}); err == nil {
		t.Fatal("缺 Workspaces 应失败")
	}
}
