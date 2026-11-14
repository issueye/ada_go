package web_test

// /frontend-registry 与 /events/workspace 的装配面（dev-plan §6/§8.3）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adag/ada-go/internal/gateway/web"
)

type stubRegistry struct {
	gen     int64
	plugins []web.RegistryPlugin
}

func (s *stubRegistry) Registry(r *http.Request) []web.RegistryPlugin { return s.plugins }
func (s *stubRegistry) RegistryGen() int64                            { return s.gen }

func newServerWithRegistry(t *testing.T, reg web.RegistrySource, workspaceEvents http.Handler) *web.Server {
	t.Helper()
	sup := &fakeSupervisor{core: newFakeCore().on("thread.list", emptyThreadList)}
	srv, err := web.New(web.Config{
		Supervisor:      sup,
		Workspace:       "/tmp/ws",
		Workspaces:      []string{"/tmp/ws"},
		ProductName:     "测试产品",
		Locale:          "zh-CN",
		CSRF:            func(*http.Request) string { return "csrf-token" },
		Registry:        reg,
		WorkspaceEvents: workspaceEvents,
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestFrontendRegistryEmptyWhenSourceNil(t *testing.T) {
	srv := newServerWithRegistry(t, nil, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/frontend-registry", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /frontend-registry = %d, want 200", rec.Code)
	}
	var payload struct {
		Rev     int64            `json:"rev"`
		Plugins []map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if payload.Rev != 0 || len(payload.Plugins) != 0 {
		t.Fatalf("空源应返回 {rev:0, plugins:[]}，实得 %+v", payload)
	}
}

func TestFrontendRegistryFromSource(t *testing.T) {
	reg := &stubRegistry{
		gen: 7,
		plugins: []web.RegistryPlugin{{
			ID: "ui-hello", Name: "Hello SFC", Trust: "builtin", Version: "0.2.0",
			Slots: []web.RegistrySlot{{Slot: "sidebar.top", Component: "/ext/ui-hello/static/Hello.vue?v=abcd1234", Order: 90}},
			Pages: []web.RegistryPage{{Slug: "hello", Component: "/ext/ui-hello/static/HelloPage.vue?v=abcd1234", Title: "Hello SFC"}},
			Nav:   []web.RegistryNav{{Path: "/ext/ui-hello/hello", Label: "Hello", Order: 90}},
		}},
	}
	srv := newServerWithRegistry(t, reg, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/frontend-registry", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /frontend-registry = %d, want 200", rec.Code)
	}
	var payload struct {
		Rev     int64 `json:"rev"`
		Plugins []struct {
			ID    string `json:"id"`
			Trust string `json:"trust"`
			Slots []struct {
				Slot      string `json:"slot"`
				Component string `json:"component"`
				Order     int    `json:"order"`
			} `json:"slots"`
			Pages []struct {
				Slug      string `json:"slug"`
				Component string `json:"component"`
			} `json:"pages"`
			Nav []struct {
				Path  string `json:"path"`
				Label string `json:"label"`
			} `json:"nav"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if payload.Rev != 7 || len(payload.Plugins) != 1 {
		t.Fatalf("rev/plugins 不符: %+v", payload)
	}
	p := payload.Plugins[0]
	if p.ID != "ui-hello" || p.Trust != "builtin" {
		t.Fatalf("插件投影不符: %+v", p)
	}
	if len(p.Slots) != 1 || p.Slots[0].Slot != "sidebar.top" || p.Slots[0].Component != "/ext/ui-hello/static/Hello.vue?v=abcd1234" {
		t.Fatalf("插槽贡献不符: %+v", p.Slots)
	}
	if len(p.Pages) != 1 || p.Pages[0].Slug != "hello" {
		t.Fatalf("页面贡献不符: %+v", p.Pages)
	}
	if len(p.Nav) != 1 || p.Nav[0].Path != "/ext/ui-hello/hello" {
		t.Fatalf("导航投影不符: %+v", p.Nav)
	}
}

func TestWorkspaceEventsNotAssembledReturns501(t *testing.T) {
	srv := newServerWithRegistry(t, nil, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/events/workspace", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("GET /events/workspace（未装配）= %d, want 501", rec.Code)
	}
}
