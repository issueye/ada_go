package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"browseruse/internal/browser"
)

func TestMCPPathIsNotControlAPI(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"not_mcp"`) || !strings.Contains(body, "not MCP") {
		t.Fatalf("body = %s", body)
	}
}

// TestEventsEndpointIsImplemented 钉住 /v1/events 不再是 501；无事件时也应返回
// ok、cursor 与空数组（长轮询超时是正常结果）。
func TestEventsEndpointIsImplemented(t *testing.T) {
	s := &Server{mgr: &browser.Manager{}, token: "tok"}
	req := httptest.NewRequest(http.MethodGet, "/v1/events?after=0&timeout_ms=1", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"events":[]`) || !strings.Contains(body, `"cursor":0`) {
		t.Fatalf("body = %s", body)
	}
}
