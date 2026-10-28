package sse_test

// sse 的验收测试：用假 CoreClient 注入事件，断言 SSE 帧格式、线程过滤、
// Last-Event-ID 回放/重同步、断开释放与连接上限。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/sse"
)

type fakeCore struct {
	events chan contracts.Event
	once   sync.Once
}

func newFakeCore(buffer int) *fakeCore {
	return &fakeCore{events: make(chan contracts.Event, buffer)}
}

func (f *fakeCore) Call(context.Context, string, any) (json.RawMessage, error) {
	return nil, errors.New("fake: Call 未实现")
}

func (f *fakeCore) Subscribe(int) (<-chan contracts.Event, func()) {
	return f.events, func() { f.once.Do(func() { close(f.events) }) }
}

func (f *fakeCore) Close() error {
	f.once.Do(func() { close(f.events) })
	return nil
}

func (f *fakeCore) send(t *testing.T, threadID string, seq int64, method string) {
	t.Helper()
	params := map[string]any{"threadId": threadID}
	if seq > 0 {
		params["seq"] = seq
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	f.events <- contracts.Event{Method: method, Params: raw}
}

func (f *fakeCore) sendRaw(t *testing.T, raw string) {
	t.Helper()
	f.events <- contracts.Event{Method: "evt.message.delta", Params: json.RawMessage(raw)}
}

type sseFrame struct {
	id      string
	event   string
	data    string
	comment string
}

type streamReader struct {
	frames chan sseFrame
	errs   chan error
}

func startReader(body io.Reader) *streamReader {
	sr := &streamReader{frames: make(chan sseFrame, 64), errs: make(chan error, 1)}
	go func() {
		defer close(sr.frames)
		br := bufio.NewReader(body)
		for {
			f, err := readFrame(br)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					sr.errs <- err
				}
				return
			}
			sr.frames <- f
		}
	}()
	return sr
}

func readFrame(br *bufio.Reader) (sseFrame, error) {
	var f sseFrame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return f, nil
		}
		switch {
		case strings.HasPrefix(line, ":"):
			f.comment = line
		case strings.HasPrefix(line, "id: "):
			f.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if f.data != "" {
				f.data += "\n"
			}
			f.data += strings.TrimPrefix(line, "data: ")
		}
	}
}

func (sr *streamReader) next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case f, ok := <-sr.frames:
		if !ok {
			t.Fatal("事件流已关闭")
		}
		return f
	case err := <-sr.errs:
		t.Fatalf("读取事件流失败: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("等待 SSE 帧超时")
	}
	return sseFrame{}
}

func startHub(t *testing.T, core contracts.CoreClient, opts sse.Options) (*sse.Hub, *httptest.Server) {
	t.Helper()
	hub := sse.New(core, opts)
	srv := httptest.NewServer(hub)
	t.Cleanup(func() {
		srv.Close()
		hub.Close()
	})
	return hub, srv
}

func connect(t *testing.T, url string, header http.Header) (*http.Response, *streamReader) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连接 SSE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("SSE 状态码 %d: %s", resp.StatusCode, body)
	}
	return resp, startReader(resp.Body)
}

func waitLatest(t *testing.T, hub *sse.Hub, threadID string, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.LatestID(threadID) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 LatestID=%d 超时，当前 %d", want, hub.LatestID(threadID))
}

func TestFrameFormatAndLocalID(t *testing.T) {
	core := newFakeCore(8)
	_, srv := startHub(t, core, sse.Options{Buffer: 8, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour})

	resp, sr := connect(t, srv.URL+"/events/th_1", nil)
	defer resp.Body.Close()

	core.send(t, "th_1", 5, "evt.message.delta")
	f := sr.next(t)
	if f.id != "5" {
		t.Fatalf("id want 5, got %q", f.id)
	}
	if f.event != "evt.message.delta" {
		t.Fatalf("event want evt.message.delta, got %q", f.event)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(f.data), &data); err != nil {
		t.Fatalf("data 不是 JSON: %q (%v)", f.data, err)
	}
	if data["threadId"] != "th_1" || data["seq"].(float64) != 5 {
		t.Fatalf("data 内容错误: %v", data)
	}

	// 缺失 seq 时使用自增本地 id。
	core.send(t, "th_1", 0, "evt.notice")
	f = sr.next(t)
	if f.id != "6" {
		t.Fatalf("本地 id 期望 6，got %q", f.id)
	}
	if f.event != "evt.notice" {
		t.Fatalf("event want evt.notice, got %q", f.event)
	}
}

func TestThreadFiltering(t *testing.T) {
	core := newFakeCore(8)
	_, srv := startHub(t, core, sse.Options{Buffer: 8, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour})

	resp, sr := connect(t, srv.URL+"/events/th_1", nil)
	defer resp.Body.Close()

	core.send(t, "th_2", 1, "evt.run.status")
	core.send(t, "th_1", 2, "evt.run.status")

	f := sr.next(t)
	if f.event != "evt.run.status" {
		t.Fatalf("event want evt.run.status, got %q", f.event)
	}
	if !strings.Contains(f.data, "th_1") {
		t.Fatalf("收到其他线程的事件: %s", f.data)
	}
}

func TestLastEventIDReplay(t *testing.T) {
	core := newFakeCore(32)
	hub, srv := startHub(t, core, sse.Options{Buffer: 8, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour})

	for i := int64(1); i <= 3; i++ {
		core.send(t, "th_1", i, "evt.message.delta")
	}
	waitLatest(t, hub, "th_1", 3)

	header := http.Header{}
	header.Set("Last-Event-ID", "1")
	resp, sr := connect(t, srv.URL+"/events/th_1", header)
	defer resp.Body.Close()

	if f := sr.next(t); f.id != "2" {
		t.Fatalf("回放第一帧 id want 2, got %q", f.id)
	}
	if f := sr.next(t); f.id != "3" {
		t.Fatalf("回放第二帧 id want 3, got %q", f.id)
	}

	// 回放结束后继续接收实时事件。
	core.send(t, "th_1", 4, "evt.message.delta")
	if f := sr.next(t); f.id != "4" {
		t.Fatalf("实时帧 id want 4, got %q", f.id)
	}
}

func TestLastEventIDQueryAndResync(t *testing.T) {
	core := newFakeCore(256)
	hub, srv := startHub(t, core, sse.Options{Buffer: 8, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour})

	// 环形缓冲上限 64，先写 100 条，让 1 被挤出窗口。
	for i := int64(1); i <= 100; i++ {
		core.send(t, "th_1", i, "evt.notice")
	}
	waitLatest(t, hub, "th_1", 100)

	resp, sr := connect(t, srv.URL+"/events/th_1?lastEventId=1", nil)
	defer resp.Body.Close()
	f := sr.next(t)
	if f.event != "resync" {
		t.Fatalf("历史不足时应发 resync，got event=%q", f.event)
	}
	if !strings.Contains(f.data, "history_unavailable") {
		t.Fatalf("resync 数据缺原因: %s", f.data)
	}

	// Last-Event-ID 超前同样触发重同步。
	header := http.Header{}
	header.Set("Last-Event-ID", "999")
	resp2, sr2 := connect(t, srv.URL+"/events/th_1", header)
	defer resp2.Body.Close()
	if f := sr2.next(t); f.event != "resync" {
		t.Fatalf("超前 Last-Event-ID 应 resync，got %q", f.event)
	}
}

func TestHeartbeatComment(t *testing.T) {
	core := newFakeCore(4)
	_, srv := startHub(t, core, sse.Options{Buffer: 4, CoalesceWindow: 5 * time.Millisecond, Heartbeat: 20 * time.Millisecond})

	resp, sr := connect(t, srv.URL+"/events/th_1", nil)
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f := sr.next(t)
		if strings.Contains(f.comment, "keepalive") {
			return
		}
	}
	t.Fatal("未收到 keepalive 注释")
}

func TestDisconnectReleasesClient(t *testing.T) {
	core := newFakeCore(4)
	hub, srv := startHub(t, core, sse.Options{Buffer: 4, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour})

	resp, _ := connect(t, srv.URL+"/events/th_1", nil)
	if got := hub.Active("th_1"); got != 1 {
		t.Fatalf("连接建立后 Active want 1, got %d", got)
	}
	_ = resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.Active("th_1") == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("断开后连接未释放，Active=%d", hub.Active("th_1"))
}

func TestPerThreadLimit(t *testing.T) {
	core := newFakeCore(4)
	_, srv := startHub(t, core, sse.Options{Buffer: 4, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour, MaxPerThread: 1})

	resp, _ := connect(t, srv.URL+"/events/th_1", nil)
	defer resp.Body.Close()

	second, err := http.Get(srv.URL + "/events/th_1")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("连接超限 want 429, got %d", second.StatusCode)
	}
}

func TestPathAndQueryThreadID(t *testing.T) {
	core := newFakeCore(4)
	_, srv := startHub(t, core, sse.Options{Buffer: 4, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour})

	resp, sr := connect(t, srv.URL+"/events?threadId=th_q", nil)
	defer resp.Body.Close()
	core.send(t, "th_q", 1, "evt.notice")
	if f := sr.next(t); f.id != "1" {
		t.Fatalf("query threadId 未生效，got frame %+v", f)
	}

	// 缺少 threadId。
	bad, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺少 threadId want 400, got %d", bad.StatusCode)
	}
}

func TestNonJSONParamsStillFramed(t *testing.T) {
	core := newFakeCore(4)
	_, srv := startHub(t, core, sse.Options{Buffer: 4, CoalesceWindow: 5 * time.Millisecond, Heartbeat: time.Hour})

	resp, sr := connect(t, srv.URL+"/events/th_1", nil)
	defer resp.Body.Close()

	core.sendRaw(t, fmt.Sprintf("{\"threadId\":\"th_1\",\"seq\":7,\"text\":%q}", "line1\nline2"))
	f := sr.next(t)
	if strings.Contains(f.data, "\nline2") {
		// 数据行内的换行已替换为空格，保持单行帧。
		t.Fatalf("data 不应包含裸换行: %q", f.data)
	}
	if f.id != "7" {
		t.Fatalf("id want 7, got %q", f.id)
	}
}
