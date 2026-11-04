package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWithToken 钉住 MCP HTTP 的可选 Bearer 鉴权：/mcp 必须带正确 token，
// /healthz 保持开放（进程探活不需要密钥）。
func TestWithToken(t *testing.T) {
	h := withToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "secret")

	cases := []struct {
		path   string
		token  string
		status int
	}{
		{"/mcp", "", http.StatusUnauthorized},
		{"/mcp", "wrong", http.StatusUnauthorized},
		{"/mcp", "secret", http.StatusNoContent},
		{"/healthz", "", http.StatusNoContent},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, c.path, nil)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.status {
			t.Fatalf("%s token=%q status = %d, want %d", c.path, c.token, w.Code, c.status)
		}
	}
}
