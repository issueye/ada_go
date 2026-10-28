package relay_test

// relay 的验收测试：用 httptest + coder/websocket 起一个假 Core，
// 覆盖并发 Call 的 id 匹配、业务错误、事件多订阅、取消、关闭与 token 头。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/relay"
	"github.com/adag/ada-go/internal/proto"
)

type fakeCore struct {
	t     *testing.T
	token string

	mu   sync.Mutex
	auth []string
}

func startFakeCore(t *testing.T, token string) (*fakeCore, string) {
	t.Helper()
	f := &fakeCore{t: t, token: token}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, "ws" + strings.TrimPrefix(srv.URL, "http") + "/rpc"
}

func (f *fakeCore) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	var writeMu sync.Mutex
	write := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.Write(context.Background(), websocket.MessageText, raw)
	}

	for {
		typ, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var req proto.Request
		if err := json.Unmarshal(data, &req); err != nil {
			continue
		}
		go func(req proto.Request) {
			switch req.Method {
			case "boom":
				write(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{
						"code": 4001, "message": "业务错误",
						"data": map[string]any{"name": "bad_request", "retryable": false},
					},
				})
			case "emit":
				var p struct {
					N int `json:"n"`
				}
				_ = json.Unmarshal(req.Params, &p)
				for i := 1; i <= p.N; i++ {
					write(map[string]any{
						"jsonrpc": "2.0", "method": "evt.tick",
						"params": map[string]any{"seq": i, "threadId": "th_1"},
					})
				}
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"sent": p.N}})
			default:
				// echo：回显 params，并按 n 制造乱序响应。
				var p struct {
					N int `json:"n"`
				}
				_ = json.Unmarshal(req.Params, &p)
				time.Sleep(time.Duration(p.N%5) * time.Millisecond)
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(req.Params)})
			}
		}(req)
	}
}

func (f *fakeCore) lastAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auth) == 0 {
		return ""
	}
	return f.auth[len(f.auth)-1]
}

func dial(t *testing.T, endpoint, token string) *relay.Client {
	t.Helper()
	client, err := relay.Dial(context.Background(), endpoint, token)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	c := client.(*relay.Client)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCallConcurrentIDMatching(t *testing.T) {
	_, endpoint := startFakeCore(t, "")
	c := dial(t, endpoint, "")

	const n = 64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			params := map[string]any{"n": i, "tag": fmt.Sprintf("t-%d", i)}
			raw, err := c.Call(context.Background(), "echo", params)
			if err != nil {
				errs <- err
				return
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				errs <- err
				return
			}
			if int(got["n"].(float64)) != i || got["tag"] != fmt.Sprintf("t-%d", i) {
				errs <- fmt.Errorf("响应错配: want n=%d, got %v", i, got)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCallRPCError(t *testing.T) {
	_, endpoint := startFakeCore(t, "")
	c := dial(t, endpoint, "")

	_, err := c.Call(context.Background(), "boom", nil)
	if err == nil {
		t.Fatal("期望业务错误")
	}
	var rpcErr *relay.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("错误类型不是 *relay.RPCError: %T", err)
	}
	if rpcErr.Code != 4001 || rpcErr.Message != "业务错误" {
		t.Fatalf("业务错误未保留: %+v", rpcErr)
	}
	var data map[string]any
	if err := json.Unmarshal(rpcErr.Data, &data); err != nil {
		t.Fatalf("data 原文应可解析: %v", err)
	}
	if data["name"] != "bad_request" {
		t.Fatalf("data 字段丢失: %v", data)
	}
}

func TestSubscribeMultipleAndCancel(t *testing.T) {
	_, endpoint := startFakeCore(t, "")
	c := dial(t, endpoint, "")

	sub1, cancel1 := c.Subscribe(16)
	sub2, cancel2 := c.Subscribe(16)
	defer cancel2()

	if _, err := c.Call(context.Background(), "emit", map[string]int{"n": 5}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if evt := recvEvent(t, sub1); evt.Method != "evt.tick" {
			t.Fatalf("sub1 收到 %q", evt.Method)
		}
		if evt := recvEvent(t, sub2); evt.Method != "evt.tick" {
			t.Fatalf("sub2 收到 %q", evt.Method)
		}
	}

	// 取消幂等，取消后通道关闭。
	cancel1()
	cancel1()
	if _, ok := <-sub1; ok {
		t.Fatal("取消后订阅通道应关闭")
	}

	if _, err := c.Call(context.Background(), "emit", map[string]int{"n": 2}); err != nil {
		t.Fatalf("emit2: %v", err)
	}
	for i := 0; i < 2; i++ {
		recvEvent(t, sub2)
	}
}

func TestSlowSubscriberDropsWithoutBlocking(t *testing.T) {
	_, endpoint := startFakeCore(t, "")
	c := dial(t, endpoint, "")

	// 无缓冲且不消费：读循环必须继续工作，事件被丢弃。
	_, cancel := c.Subscribe(0)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Call(context.Background(), "emit", map[string]int{"n": 32})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("慢订阅者阻塞了读循环")
	}
	if c.Dropped() == 0 {
		t.Fatal("期望有事件被丢弃")
	}
}

func TestCloseFailsCallsAndSubscriptions(t *testing.T) {
	_, endpoint := startFakeCore(t, "")
	c := dial(t, endpoint, "")

	sub, _ := c.Subscribe(4)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close 应幂等: %v", err)
	}
	if _, ok := <-sub; ok {
		t.Fatal("关闭后订阅通道应关闭")
	}
	if _, err := c.Call(context.Background(), "echo", nil); !errors.Is(err, relay.ErrClosed) {
		t.Fatalf("关闭后 Call 应返回 ErrClosed，实际 %v", err)
	}
	if !errors.Is(c.Err(), relay.ErrClosed) {
		t.Fatalf("Err 应报告 ErrClosed，实际 %v", c.Err())
	}
}

func TestTokenHeaderAndEndpointForms(t *testing.T) {
	f, wsURL := startFakeCore(t, "s3cret")

	// 完整 ws URL。
	c := dial(t, wsURL, "s3cret")
	if _, err := c.Call(context.Background(), "echo", map[string]int{"n": 0}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := f.lastAuth(); got != "Bearer s3cret" {
		t.Fatalf("Authorization 头错误: %q", got)
	}

	// 裸 host:port（从 ws URL 截取）与 http:// 形式。
	hostPort := strings.TrimSuffix(strings.TrimPrefix(wsURL, "ws://"), "/rpc")
	c2 := dial(t, hostPort, "s3cret")
	if _, err := c2.Call(context.Background(), "echo", map[string]int{"n": 1}); err != nil {
		t.Fatalf("裸 host:port Call: %v", err)
	}
	c3 := dial(t, "http://"+hostPort, "s3cret")
	if _, err := c3.Call(context.Background(), "echo", map[string]int{"n": 2}); err != nil {
		t.Fatalf("http:// Call: %v", err)
	}

	// 错误 token 在握手阶段失败。
	if _, err := relay.Dial(context.Background(), wsURL, "wrong"); err == nil {
		t.Fatal("错误 token 应握手失败")
	}
}

func recvEvent(t *testing.T, ch <-chan contracts.Event) contracts.Event {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("订阅通道已关闭")
		}
		return evt
	case <-time.After(2 * time.Second):
		t.Fatal("等待事件超时")
		return contracts.Event{}
	}
}
