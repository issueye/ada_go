package host_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/plugins/host"
)

func bridgeFor(t *testing.T, srv *httptest.Server) *host.Bridge {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := host.NewHTTPBridge(host.HTTPBridgeConfig{AllowHosts: []string{u.Host}, IdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestHTTPBridgeAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	b, err := host.NewHTTPBridge(host.HTTPBridgeConfig{AllowHosts: []string{"example.invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.JSON(context.Background(), "GET", srv.URL, nil, "", 1024); err == nil {
		t.Fatal("allowlist 之外的 host 必须被拒绝")
	}

	ok := bridgeFor(t, srv)
	res, err := ok.JSON(context.Background(), "GET", srv.URL, nil, "", 1024)
	if err != nil {
		t.Fatalf("allowlist 内请求失败: %v", err)
	}
	if res.Status != 200 || string(res.Body) != `{"ok":true}` {
		t.Fatalf("响应 = %d %s", res.Status, res.Body)
	}
}

func TestHTTPBridgeJSONKeepsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	b := bridgeFor(t, srv)
	res, err := b.JSON(context.Background(), "GET", srv.URL, nil, "", 1024)
	if err != nil {
		t.Fatalf("JSON 对非 2xx 应返回结果而不是异常: %v", err)
	}
	if res.Status != http.StatusNotFound {
		t.Fatalf("status = %d", res.Status)
	}
}

func TestHTTPBridgeSSEParsesEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": heartbeat\n\n")
		fmt.Fprint(w, "event: chunk\ndata: line1\ndata: line2\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	b := bridgeFor(t, srv)
	var got []string
	res, err := b.SSE(context.Background(), "POST", srv.URL, nil, "{}", func(event, data string) error {
		got = append(got, event+"|"+data)
		return nil
	})
	if err != nil {
		t.Fatalf("SSE: %v", err)
	}
	if res.SSEEvents != 2 {
		t.Fatalf("事件数 = %d", res.SSEEvents)
	}
	if strings.Join(got, ",") != "chunk|line1\nline2,|[DONE]" {
		t.Fatalf("事件 = %q", got)
	}
}

func TestHTTPBridgeSSEErrorStatusIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "overloaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	b := bridgeFor(t, srv)
	_, err := b.SSE(context.Background(), "POST", srv.URL, nil, "{}", func(string, string) error { return nil })
	var he *host.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("期望 *HTTPError，实得 %T: %v", err, err)
	}
	if he.Status != http.StatusServiceUnavailable || !he.Retryable() {
		t.Fatalf("HTTPError = %+v", he)
	}
	if he.RetryAfter != time.Second {
		t.Fatalf("RetryAfter = %s", he.RetryAfter)
	}
}

func TestHTTPBridgeSSECallbackErrorStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: one\n\n")
		fmt.Fprint(w, "data: two\n\n")
	}))
	defer srv.Close()
	b := bridgeFor(t, srv)
	boom := errors.New("callback boom")
	var n int
	_, err := b.SSE(context.Background(), "POST", srv.URL, nil, "{}", func(string, string) error {
		n++
		return boom
	})
	if !errors.Is(err, boom) || n != 1 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}

func TestHTTPBridgeSSECancel(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	b := bridgeFor(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.SSE(ctx, "POST", srv.URL, nil, "{}", func(string, string) error { return nil })
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消后 SSE 应返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消没有贯穿到 SSE 读取")
	}
}

func TestHTTPBridgeResponseLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 2048))
	}))
	defer srv.Close()
	b := bridgeFor(t, srv)
	if _, err := b.JSON(context.Background(), "GET", srv.URL, nil, "", 1024); err == nil {
		t.Fatal("超过 maxBytes 必须报错")
	}
}
