// Package sse 是 Gateway 的 Server-Sent Events hub（设计 7.4）。
//
// 每个线程一个逻辑 hub：订阅 Core 的 evt.* 通知，按 params.threadId 路由，
// 合并 40ms 窗口后写成浏览器 EventSource 帧；断线时用有界环形缓冲补发，
// 无法补齐则发 event: resync 让客户端走 thread.get 全量重同步。
package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
)

// 默认值（设计 7.4、9.5）。
const (
	DefaultBuffer         = 256
	DefaultCoalesceWindow = 40 * time.Millisecond
	DefaultHeartbeat      = 15 * time.Second
	DefaultMaxPerThread   = 64

	minHistory = 64
	maxHistory = 4096
)

// Options 是 Hub 的装配输入；零值使用默认值。
type Options struct {
	// Buffer 是每个 SSE 客户端与 Core 订阅通道的缓冲。
	Buffer int
	// CoalesceWindow 是同线程事件的合并窗口。
	CoalesceWindow time.Duration
	// Heartbeat 是 SSE 注释心跳间隔。
	Heartbeat time.Duration
	// MaxPerThread 是单线程连接上限，超出返回 429。
	MaxPerThread int
}

// Hub 把 Core 事件按线程路由到 EventSource 客户端。
type Hub struct {
	core contracts.CoreClient
	opts Options

	mu       sync.Mutex
	threads  map[string]*threadState
	closed   bool
	history  int
	unsub    func()
	stopOnce sync.Once
}

type threadState struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	history []frame
	nextID  int64
}

type client struct {
	ch chan frame
}

type frame struct {
	id     int64
	method string
	data   json.RawMessage
}

// Hub 可直接挂到 http.ServeMux 的 /events/ 路由上。
var _ http.Handler = (*Hub)(nil)

// New 创建 Hub 并订阅 Core 事件。core 为空时 Hub 只保留回放能力。
func New(core contracts.CoreClient, opts Options) *Hub {
	opts = normalizeOptions(opts)
	history := opts.Buffer * 2
	if history < minHistory {
		history = minHistory
	}
	if history > maxHistory {
		history = maxHistory
	}
	h := &Hub{
		core:    core,
		opts:    opts,
		threads: map[string]*threadState{},
		history: history,
	}
	if core != nil {
		events, cancel := core.Subscribe(opts.Buffer)
		h.unsub = cancel
		go h.run(events)
	}
	return h
}

// Close 取消 Core 订阅；已建立的 SSE 连接继续读到连接结束。
func (h *Hub) Close() {
	h.stopOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		unsub := h.unsub
		h.mu.Unlock()
		if unsub != nil {
			unsub()
		}
	})
}

// Active 返回指定线程当前的 SSE 连接数（观测用）。
func (h *Hub) Active(threadID string) int {
	h.mu.Lock()
	th := h.threads[threadID]
	h.mu.Unlock()
	if th == nil {
		return 0
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	return len(th.clients)
}

// LatestID 返回指定线程环形缓冲里最新事件的 id；没有事件时返回 0。
func (h *Hub) LatestID(threadID string) int64 {
	h.mu.Lock()
	th := h.threads[threadID]
	h.mu.Unlock()
	if th == nil {
		return 0
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	if len(th.history) == 0 {
		return 0
	}
	return th.history[len(th.history)-1].id
}

// ServeHTTP 处理 GET /events/{threadId}（也接受 /events?threadId=）。
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "只支持 GET", http.StatusMethodNotAllowed)
		return
	}
	threadID := threadIDFromRequest(r)
	if threadID == "" {
		http.Error(w, "需要 threadId", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "事件流已关闭", http.StatusServiceUnavailable)
		return
	}
	th := h.threads[threadID]
	if th == nil {
		th = &threadState{clients: map[*client]struct{}{}}
		h.threads[threadID] = th
	}
	h.mu.Unlock()

	c := &client{ch: make(chan frame, h.opts.Buffer)}
	lastRaw := lastEventID(r)
	last, hasLast := parseLastEventID(lastRaw)

	th.mu.Lock()
	if len(th.clients) >= h.opts.MaxPerThread {
		th.mu.Unlock()
		http.Error(w, "连接数超限", http.StatusTooManyRequests)
		return
	}
	th.clients[c] = struct{}{}
	replay, resync := th.replay(last, hasLast)
	th.mu.Unlock()

	defer func() {
		th.mu.Lock()
		delete(th.clients, c)
		th.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}

	// 先补发历史，再进入实时循环；注册与快照在同一把锁内，避免重复。
	if resync {
		if err := writeResync(w, threadID, lastRaw); err != nil {
			return
		}
	}
	for _, f := range replay {
		if err := writeFrame(w, f); err != nil {
			return
		}
	}
	if len(replay) > 0 || resync {
		if err := rc.Flush(); err != nil {
			return
		}
	}

	heartbeat := time.NewTicker(h.opts.Heartbeat)
	defer heartbeat.Stop()

	var pending []frame
	var timer *time.Timer
	var timerC <-chan time.Time
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}
	defer stopTimer()

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		for _, f := range pending {
			if err := writeFrame(w, f); err != nil {
				return err
			}
		}
		pending = pending[:0]
		return rc.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case f := <-c.ch:
			pending = append(pending, f)
			if timer == nil {
				timer = time.NewTimer(h.opts.CoalesceWindow)
				timerC = timer.C
			}
		case <-timerC:
			stopTimer()
			if err := flush(); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := flush(); err != nil {
				return
			}
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func (h *Hub) run(events <-chan contracts.Event) {
	for evt := range events {
		threadID, seq := parseEventMeta(evt.Params)
		if threadID == "" {
			// 工作区级事件（evt.ready/evt.plugin.changed/evt.error）不进线程流。
			continue
		}
		h.dispatch(threadID, evt, seq)
	}
}

func (h *Hub) dispatch(threadID string, evt contracts.Event, seq int64) {
	h.mu.Lock()
	th := h.threads[threadID]
	if th == nil {
		th = &threadState{clients: map[*client]struct{}{}}
		h.threads[threadID] = th
	}
	h.mu.Unlock()

	th.mu.Lock()
	defer th.mu.Unlock()

	f := frame{method: evt.Method, data: evt.Params}
	if seq > 0 {
		f.id = seq
		if seq > th.nextID {
			th.nextID = seq
		}
	} else {
		th.nextID++
		f.id = th.nextID
	}
	th.append(f, h.history)
	for c := range th.clients {
		select {
		case c.ch <- f:
		default:
			// 客户端积压：丢弃本帧，前端按 seq 断档自行走 thread.get。
		}
	}
}

func (th *threadState) append(f frame, limit int) {
	if limit <= 0 || len(th.history) < limit {
		th.history = append(th.history, f)
		return
	}
	copy(th.history, th.history[len(th.history)-limit+1:])
	th.history[len(th.history)-1] = f
}

// replay 计算 Last-Event-ID 之后的补发窗口；无法满足时返回 resync。
func (th *threadState) replay(last int64, hasLast bool) ([]frame, bool) {
	if !hasLast {
		return nil, false
	}
	if len(th.history) == 0 {
		return nil, true
	}
	oldest := th.history[0].id
	newest := th.history[len(th.history)-1].id
	if last > newest || last < oldest-1 {
		return nil, true
	}
	out := make([]frame, 0, len(th.history))
	for _, f := range th.history {
		if f.id > last {
			out = append(out, f)
		}
	}
	return out, false
}

func writeFrame(w io.Writer, f frame) error {
	method := sanitize(f.method)
	data := sanitizeBytes(f.data)
	if len(data) == 0 {
		data = []byte("{}")
	}
	_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.id, method, data)
	return err
}

func writeResync(w io.Writer, threadID, lastRaw string) error {
	body, _ := json.Marshal(map[string]string{
		"threadId":    threadID,
		"reason":      "history_unavailable",
		"lastEventId": lastRaw,
	})
	_, err := fmt.Fprintf(w, "event: resync\ndata: %s\n\n", body)
	return err
}

func sanitize(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func sanitizeBytes(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	if !strings.ContainsAny(string(raw), "\r\n") {
		return raw
	}
	return []byte(sanitize(string(raw)))
}

func parseEventMeta(raw json.RawMessage) (string, int64) {
	if len(raw) == 0 {
		return "", 0
	}
	var meta struct {
		ThreadID string `json:"threadId"`
		Seq      int64  `json:"seq"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", 0
	}
	return meta.ThreadID, meta.Seq
}

func threadIDFromRequest(r *http.Request) string {
	path := r.URL.EscapedPath()
	if strings.HasPrefix(path, "/events/") {
		id, err := url.PathUnescape(strings.TrimPrefix(path, "/events/"))
		if err == nil && id != "" && !strings.Contains(id, "/") {
			return id
		}
	}
	return r.URL.Query().Get("threadId")
}

func lastEventID(r *http.Request) string {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		return v
	}
	return r.URL.Query().Get("lastEventId")
}

func parseLastEventID(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, true
	}
	return n, true
}

func normalizeOptions(opts Options) Options {
	if opts.Buffer <= 0 {
		opts.Buffer = DefaultBuffer
	}
	if opts.CoalesceWindow <= 0 {
		opts.CoalesceWindow = DefaultCoalesceWindow
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = DefaultHeartbeat
	}
	if opts.MaxPerThread <= 0 {
		opts.MaxPerThread = DefaultMaxPerThread
	}
	return opts
}
