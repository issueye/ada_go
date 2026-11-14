package canvas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func decodeErrorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Error Error `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	return body.Error.Code
}

func TestGuardRejectsForeignHost(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	req.Host = "evil.example" // 模拟 DNS rebinding：Host 是攻击者域名
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("非回环 Host 应 403, got %d", res.StatusCode)
	}
	if code := decodeErrorCode(t, res); code != "bad_host" {
		t.Fatalf("错误码 = %q, 期望 bad_host", code)
	}
}

func TestGuardRejectsCrossPortOrigin(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:1") // 同回环但跨端口
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("跨端口 Origin 应 403, got %d", res.StatusCode)
	}
	if code := decodeErrorCode(t, res); code != "bad_origin" {
		t.Fatalf("错误码 = %q, 期望 bad_origin", code)
	}
}

func TestGuardAllowsSameOriginWrite(t *testing.T) {
	_, ts := newTestServer(t)
	u, _ := url.Parse(ts.URL)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects", strings.NewReader(`{"name":"同源写入"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+u.Host)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("同源写应 201, got %d", res.StatusCode)
	}
}

func TestGuardRecoversPanic(t *testing.T) {
	srv := &Server{}
	h := srv.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:8788"
	req.Header.Set("Origin", "http://127.0.0.1:8788")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应兜底 500, got %d", rec.Code)
	}
	var body struct {
		Error Error `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("panic 响应不是 JSON: %v", err)
	}
	if body.Error.Code != "internal" {
		t.Fatalf("错误码 = %q, 期望 internal", body.Error.Code)
	}
}
