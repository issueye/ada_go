package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

// routeFixtureScript 是一个只登记 HTTP 路由的后端插件夹具。
func routeFixtureScript() host.Script {
	src := "export default function (agent) {\n" +
		"  agent.route({ method: 'POST', path: '/echo', handler: function (req) {\n" +
		"    return { method: req.method, path: req.path, query: req.query, body: req.body };\n" +
		"  } });\n" +
		"  agent.route('GET', '/items/*', function (req) { return { prefix: req.path }; });\n" +
		"}"
	m := &plugins.Manifest{
		ID: "route-fixture", Name: "路由夹具", Version: "0.1.0",
		APIVersion: plugins.APIVersionMajor, Trust: plugins.TrustBuiltin,
		Backend: &plugins.Backend{Entry: "index.js"},
	}
	return host.Script{Manifest: m, EntryName: host.EntryScheme + "://route-fixture/index.js", Source: src}
}

func assembleRouteFixture(t *testing.T) *app.Assembly {
	t.Helper()
	p := testProduct(t, "route-fixture")
	asm, err := app.Assemble(context.Background(), p, []host.Script{routeFixtureScript()},
		app.Deps{Context: host.Context{Workspace: t.TempDir()}})
	if err != nil {
		t.Fatalf("app.Assemble: %v", err)
	}
	t.Cleanup(func() { _ = asm.Close(context.Background()) })
	return asm
}

// 精确路由、对象/旧名两种登记、前缀路由与 404/405 语义。
func TestPluginHTTPAPIRouteDispatch(t *testing.T) {
	asm := assembleRouteFixture(t)
	ctx := context.Background()

	out, err := asm.CallHTTPAPI(ctx, "route-fixture", "POST", "/echo",
		json.RawMessage(`{"method":"POST","path":"/echo","query":{"x":"1"},"body":{"n":2}}`), "th_1")
	if err != nil {
		t.Fatalf("POST /echo: %v", err)
	}
	for _, want := range []string{`"n":2`, `"x":"1"`, `"/echo"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("POST /echo 结果缺少 %s: %s", want, out)
		}
	}

	out, err = asm.CallHTTPAPI(ctx, "route-fixture", "GET", "/items/42",
		json.RawMessage(`{"method":"GET","path":"/items/42"}`), "")
	if err != nil {
		t.Fatalf("GET /items/42: %v", err)
	}
	if !strings.Contains(string(out), `"/items/42"`) {
		t.Fatalf("前缀路由结果 = %s", out)
	}

	if _, err := asm.CallHTTPAPI(ctx, "route-fixture", "GET", "/missing", nil, ""); !errors.Is(err, app.ErrRouteNotFound) {
		t.Fatalf("未知路径错误 = %v, want ErrRouteNotFound", err)
	}
	if _, err := asm.CallHTTPAPI(ctx, "route-fixture", "GET", "/echo", nil, ""); !errors.Is(err, app.ErrRouteMethodNotAllowed) {
		t.Fatalf("错误方法 = %v, want ErrRouteMethodNotAllowed", err)
	}
	if _, err := asm.CallHTTPAPI(ctx, "no-such-plugin", "GET", "/echo", nil, ""); !errors.Is(err, app.ErrRouteNotFound) {
		t.Fatalf("未知插件 = %v, want ErrRouteNotFound", err)
	}
}

// route 调用携带的 threadId 要一路传到 ctx.state 的 StateOp，
// 否则线程级 KV 会落回会话的“当前线程”，在并发/后台调用里读错数据。
func TestPluginRouteCarriesThreadIntoState(t *testing.T) {
	ctx := context.Background()
	var seen string
	hc := host.Context{
		Workspace: t.TempDir(),
		State: func(_ context.Context, op host.StateOp) (json.RawMessage, error) {
			seen = op.ThreadID
			return json.RawMessage(`{"thread":"` + op.ThreadID + `"}`), nil
		},
	}
	src := "export default function (agent) {\n" +
		"  agent.route('GET', '/state', function (req, ctx) { return ctx.state.get('k'); });\n" +
		"}"
	m := &plugins.Manifest{
		ID: "route-state", Name: "状态路由夹具", Version: "0.1.0",
		APIVersion: plugins.APIVersionMajor, Trust: plugins.TrustBuiltin,
		Backend: &plugins.Backend{Entry: "index.js", Capabilities: []plugins.Capability{plugins.CapState}},
	}
	script := host.Script{Manifest: m, EntryName: host.EntryScheme + "://route-state/index.js", Source: src}
	asm, err := app.Assemble(ctx, testProduct(t, "route-state"), []host.Script{script}, app.Deps{Context: hc})
	if err != nil {
		t.Fatalf("app.Assemble: %v", err)
	}
	t.Cleanup(func() { _ = asm.Close(context.Background()) })

	out, err := asm.CallHTTPAPI(ctx, "route-state", "GET", "/state",
		json.RawMessage(`{"method":"GET","path":"/state"}`), "th_42")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	if seen != "th_42" {
		t.Fatalf("StateOp.ThreadID = %q, want th_42", seen)
	}
	if !strings.Contains(string(out), `"thread":"th_42"`) {
		t.Fatalf("路由结果 = %s", out)
	}
}

// SSE 路由：普通调用被 406 拒绝，流式调用按帧回调；类型不匹配也有明确错误。
func TestPluginHTTPAPIStream(t *testing.T) {
	ctx := context.Background()
	src := "export default function (agent) {\n" +
		"  agent.route({ method: 'GET', path: '/events', sse: true, handler: function (req, ctx) {\n" +
		"    ctx.sse.send({ n: 1 });\n" +
		"    ctx.sse.send({ n: 2 }, 'update');\n" +
		"  } });\n" +
		"  agent.route('GET', '/plain', function () { return { ok: true }; });\n" +
		"}"
	m := &plugins.Manifest{
		ID: "route-sse", Name: "SSE 路由夹具", Version: "0.1.0",
		APIVersion: plugins.APIVersionMajor, Trust: plugins.TrustBuiltin,
		Backend: &plugins.Backend{Entry: "index.js"},
	}
	script := host.Script{Manifest: m, EntryName: host.EntryScheme + "://route-sse/index.js", Source: src}
	asm, err := app.Assemble(ctx, testProduct(t, "route-sse"), []host.Script{script}, app.Deps{Context: host.Context{Workspace: t.TempDir()}})
	if err != nil {
		t.Fatalf("app.Assemble: %v", err)
	}
	t.Cleanup(func() { _ = asm.Close(context.Background()) })

	if _, err := asm.CallHTTPAPI(ctx, "route-sse", "GET", "/events", nil, ""); !errors.Is(err, app.ErrRouteStreaming) {
		t.Fatalf("普通调用 SSE 路由 = %v, want ErrRouteStreaming", err)
	}
	if err := asm.CheckHTTPAPIStream("route-sse", "GET", "/events"); err != nil {
		t.Fatalf("CheckHTTPAPIStream(/events) = %v", err)
	}
	if err := asm.CheckHTTPAPIStream("route-sse", "GET", "/plain"); !errors.Is(err, app.ErrRouteNotStreaming) {
		t.Fatalf("CheckHTTPAPIStream(/plain) = %v, want ErrRouteNotStreaming", err)
	}
	err = asm.StreamHTTPAPI(ctx, "route-sse", "GET", "/plain", nil, "", func(host.RouteEvent) error { return nil })
	if !errors.Is(err, app.ErrRouteNotStreaming) {
		t.Fatalf("流式调用普通路由 = %v, want ErrRouteNotStreaming", err)
	}

	var frames []host.RouteEvent
	err = asm.StreamHTTPAPI(ctx, "route-sse", "GET", "/events", nil, "",
		func(ev host.RouteEvent) error {
			frames = append(frames, ev)
			return nil
		})
	if err != nil {
		t.Fatalf("StreamHTTPAPI: %v", err)
	}
	if len(frames) != 2 || frames[1].Event != "update" || !strings.Contains(string(frames[1].Data), `"n":2`) {
		t.Fatalf("帧 = %+v", frames)
	}
}
