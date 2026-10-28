package security_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/security"
)

const testToken = "test-token"

func newMiddleware(t *testing.T, cfg security.Config) *security.Middleware {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	m, err := security.New(cfg)
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}
	return m
}

func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if called != nil {
			*called = true
		}
		w.WriteHeader(http.StatusOK)
	})
}

func addSession(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: security.DefaultCookieName, Value: token})
	return r
}

func addCSRF(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: security.DefaultCSRFName, Value: token})
	return r
}

func TestUnauthenticatedPageRedirectsToLogin(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	called := false
	h := m.Wrap(okHandler(&called))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/threads", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态码 want 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("Location want /login, got %q", loc)
	}
	if called {
		t.Fatal("未登录不应进入下游 handler")
	}
}

func TestUnauthenticatedAPIReturns401JSON(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	h := m.Wrap(okHandler(nil))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/api/threads", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 want 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type want JSON, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"unauthorized"`) {
		t.Fatalf("响应体缺少错误信息: %s", rec.Body.String())
	}
}

func TestBearerAuthenticatesAPI(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	called := false
	h := m.Wrap(okHandler(&called))

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/threads", strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("Bearer 应通过，got status=%d called=%v", rec.Code, called)
	}
}

func TestCookieWithCSRFPasses(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	called := false
	h := m.Wrap(okHandler(&called))

	req := addCSRF(addSession(httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/echo", strings.NewReader(`{}`)), testToken), "csrf-value")
	req.Header.Set("X-CSRF-Token", "csrf-value")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("Cookie+CSRF 应通过，got status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}

func TestCookieFormCSRFPasses(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	called := false
	h := m.Wrap(okHandler(&called))

	form := url.Values{"csrf_token": {"form-csrf"}, "text": {"hi"}}.Encode()
	req := addCSRF(addSession(httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/message", strings.NewReader(form)), testToken), "form-csrf")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("表单 CSRF 应通过，got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMissingOrWrongCSRFIs403(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	h := m.Wrap(okHandler(nil))

	cases := []struct {
		name   string
		header string
		cookie string
	}{
		{"缺少 header 与表单", "", "csrf-value"},
		{"header 不匹配", "wrong", "csrf-value"},
		{"缺少 cookie", "csrf-value", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := addSession(httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/echo", strings.NewReader(`{}`)), testToken)
			if tc.cookie != "" {
				addCSRF(req, tc.cookie)
			}
			if tc.header != "" {
				req.Header.Set("X-CSRF-Token", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("want 403, got %d", rec.Code)
			}
		})
	}
}

func TestCrossSiteOriginIs403(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	h := m.Wrap(okHandler(nil))

	req := addCSRF(addSession(httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/echo", strings.NewReader(`{}`)), testToken), "csrf-value")
	req.Header.Set("X-CSRF-Token", "csrf-value")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站 Origin want 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "origin_denied") {
		t.Fatalf("响应体应说明 origin_denied: %s", rec.Body.String())
	}
}

func TestOriginAllowances(t *testing.T) {
	m := newMiddleware(t, security.Config{AllowedOrigins: []string{"https://ui.example"}})
	h := m.Wrap(okHandler(nil))

	cases := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"回环 Origin", "127.0.0.1:9000", "http://127.0.0.1:8080", http.StatusOK},
		{"localhost Origin", "127.0.0.1:9000", "http://localhost:8080", http.StatusOK},
		{"白名单 Origin", "127.0.0.1:9000", "https://ui.example", http.StatusOK},
		{"同源 Origin", "app.local:9000", "http://app.local:9000", http.StatusOK},
		{"异源 Origin", "app.local:9000", "https://other.example", http.StatusForbidden},
		{"无 Origin", "app.local:9000", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/api/echo", strings.NewReader(`{}`))
			req.Host = tc.host
			req.Header.Set("Authorization", "Bearer "+testToken)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d body=%s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	h := m.Wrap(okHandler(nil))

	req := addSession(httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9000/threads", nil), testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s want %q, got %q", k, v, got)
		}
	}
}

func TestBodyLimit413(t *testing.T) {
	m := newMiddleware(t, security.Config{MaxBodyBytes: 16})
	h := m.Wrap(okHandler(nil))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/upload", strings.NewReader(strings.Repeat("a", 64)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", rec.Code)
	}
}

func TestBypassPaths(t *testing.T) {
	m := newMiddleware(t, security.Config{})
	h := m.Wrap(okHandler(nil))

	for _, path := range []string{"/healthz", "/login", "/static/app.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s 应放行，got %d", path, rec.Code)
		}
	}
}

func TestIssueSessionAndCSRFToken(t *testing.T) {
	m := newMiddleware(t, security.Config{Secure: true})

	rec := httptest.NewRecorder()
	m.IssueSession(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/login", nil))
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("期望两个 cookie，got %d", len(cookies))
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/echo", nil)
	var csrf string
	for _, c := range cookies {
		req.AddCookie(c)
		if c.Name == security.DefaultCSRFName {
			csrf = c.Value
		}
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie %s 属性错误: %+v", c.Name, c)
		}
	}
	if csrf == "" {
		t.Fatal("缺少 CSRF cookie")
	}
	if got := m.CSRFToken(req); got != csrf {
		t.Fatalf("CSRFToken want %q, got %q", csrf, got)
	}

	// IssueSession 之后，带 cookie 的写请求应可直接用 CSRF 通过。
	ok := false
	h := m.Wrap(okHandler(&ok))
	req.Header.Set("X-CSRF-Token", csrf)
	r2 := req.Clone(req.Context())
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusOK || !ok {
		t.Fatalf("登录后 CSRF 应可用，got status=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := security.New(security.Config{}); err == nil {
		t.Fatal("空 Token 应报错")
	}
	if _, err := security.New(security.Config{Token: "t", LoginPath: "/"}); err == nil {
		t.Fatal("LoginPath 为 / 应报错")
	}
	if _, err := security.New(security.Config{Token: "t", AllowedOrigins: []string{"://bad"}}); err == nil {
		t.Fatal("非法 AllowedOrigins 应报错")
	}
}
