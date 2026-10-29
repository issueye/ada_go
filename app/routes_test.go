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
