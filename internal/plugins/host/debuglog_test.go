package host_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/plugins/host"
)

// debugBridge 创建把调试输出写到 buf 的桥。
func debugBridge(t *testing.T, srv *httptest.Server, buf *bytes.Buffer) *host.Bridge {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := host.NewHTTPBridge(host.HTTPBridgeConfig{
		AllowHosts:  []string{u.Host},
		IdleTimeout: time.Second,
		DebugWriter: buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestHTTPBridgeDebugLogsRequestAndRedacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	b := debugBridge(t, srv, &buf)
	headers := map[string]string{
		"Authorization": "Bearer sk-live-secret-123456",
		"x-api-key":     "sk-ant-secret-abcdef",
		"Content-Type":  "application/json",
	}
	res, err := b.JSON(context.Background(), "POST", srv.URL,
		headers, `{"model":"gpt-4o","api_key":"sk-body-secret-999"}`, 1<<20)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d", res.Status)
	}

	out := buf.String()
	for _, want := range []string{
		">>> #1 POST " + srv.URL,
		"Authorization: ***",
		"X-Api-Key: ***",
		"Content-Type: application/json",
		`"model": "gpt-4o"`,
		"<<< #1 200 OK",
		`"ok": true`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("调试输出缺少 %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{"sk-live-secret", "sk-ant-secret", "sk-body-secret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("调试输出泄漏密钥 %q:\n%s", leak, out)
		}
	}
}

func TestHTTPBridgeDebugLogsSSEStatusWithoutBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"delta\":\"hi\"}\n\n"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	b := debugBridge(t, srv, &buf)
	var events int
	if _, err := b.SSE(context.Background(), "POST", srv.URL,
		map[string]string{"Authorization": "Bearer sk-stream-secret-1"}, `{"stream":true}`,
		func(event, data string) error {
			events++
			return nil
		}); err != nil {
		t.Fatalf("SSE 失败: %v", err)
	}
	if events != 1 {
		t.Fatalf("events = %d", events)
	}
	out := buf.String()
	if !strings.Contains(out, ">>> #1 POST "+srv.URL) || !strings.Contains(out, "<<< #1 200 OK") {
		t.Fatalf("缺少 SSE 请求/响应摘要:\n%s", out)
	}
	if strings.Contains(out, "sk-stream-secret") {
		t.Fatalf("调试输出泄漏密钥:\n%s", out)
	}
	if strings.Contains(out, "delta") {
		t.Fatalf("SSE 成功响应不应把事件流复制进调试输出:\n%s", out)
	}
}

func TestHTTPBridgeDebugLogsNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u, _ := url.Parse(srv.URL)
	srv.Close() // 立即关闭，制造连接失败

	var buf bytes.Buffer
	b, err := host.NewHTTPBridge(host.HTTPBridgeConfig{
		AllowHosts:  []string{u.Host},
		DebugWriter: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.JSON(context.Background(), "GET", srv.URL, nil, "", 1024); err == nil {
		t.Fatal("连接已关闭的服务器应失败")
	}
	out := buf.String()
	if !strings.Contains(out, ">>> #1 GET") || !strings.Contains(out, "!!! #1 failed after") {
		t.Fatalf("缺少失败摘要:\n%s", out)
	}
}

func TestHTTPBridgeDebugDisabledByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	b := bridgeFor(t, srv) // 不带 DebugWriter
	if _, err := b.JSON(context.Background(), "GET", srv.URL, nil, "", 1024); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
}
