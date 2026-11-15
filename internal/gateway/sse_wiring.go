package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/sse"
)

// workspaceRequestResolver 允许 SSE 与前端插件桥在 web.Server 创建后
// 复用同一套动态工作区解析（含 FilePicker 登记的工作区）。创建前先静态兜底。
type workspaceRequestResolver struct {
	mu      sync.RWMutex
	resolve func(*http.Request) string
}

func newWorkspaceRequestResolver(resolve func(*http.Request) string) *workspaceRequestResolver {
	return &workspaceRequestResolver{resolve: resolve}
}

func (r *workspaceRequestResolver) Resolve(req *http.Request) string {
	r.mu.RLock()
	resolve := r.resolve
	r.mu.RUnlock()
	if resolve == nil {
		return ""
	}
	return resolve(req)
}

func (r *workspaceRequestResolver) Set(resolve func(*http.Request) string) {
	r.mu.Lock()
	r.resolve = resolve
	r.mu.Unlock()
}

// eventHandler 为每条 SSE 连接取一个 Core 句柄，连接结束即 Release。
// 每个连接一个 Hub 意味着断线后 Last-Event-ID 回放只在本次连接内有界；
// 跨连接重同步由客户端调用 thread.get 完成。
type eventHandler struct {
	sup          contracts.Supervisor
	workspaceFor func(*http.Request) string
	opts         sse.Options
}

func (h *eventHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := ""
	if h.workspaceFor != nil {
		workspace = h.workspaceFor(r)
	}
	core, err := h.sup.Acquire(r.Context(), workspace)
	if err != nil {
		http.Error(w, "Core 不可用: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer h.sup.Release(workspace)
	hub := sse.New(core, h.opts)
	defer hub.Close()
	hub.ServeHTTP(w, r)
}

// workspaceEventsHandler 是 GET /events/workspace：工作区级事件
// （evt.ready / evt.plugin.changed，docs/08 §8.2）的 SSE 流。
//
// 线程级事件不进这条流（sse.Hub 按 threadId 分发并跳过无 threadId 的事件，
// hub.go 的 run()）；反过来工作区事件也独立成流，seq 取 params.workspaceSeq。
// 事件稀疏：不做合并窗口，15s 心跳；不做历史回放——断线重连的客户端以
// GET /frontend-registry 全量重同步为准（dev-plan §8.3）。
type workspaceEventsHandler struct {
	sup          contracts.Supervisor
	workspaceFor func(*http.Request) string
}

func (h *workspaceEventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := ""
	if h.workspaceFor != nil {
		workspace = h.workspaceFor(r)
	}
	core, err := h.sup.Acquire(r.Context(), workspace)
	if err != nil {
		http.Error(w, "Core 不可用: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer h.sup.Release(workspace)

	events, cancel := core.Subscribe(64)
	defer cancel()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "连接不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case evt, alive := <-events:
			if !alive {
				return
			}
			if !isWorkspaceEvent(evt.Method) {
				continue
			}
			var buf bytes.Buffer
			if seq := workspaceSeq(evt.Params); seq > 0 {
				fmt.Fprintf(&buf, "id: %d\n", seq)
			}
			fmt.Fprintf(&buf, "event: %s\ndata: %s\n\n", evt.Method, evt.Params)
			if _, err := w.Write(buf.Bytes()); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func isWorkspaceEvent(method string) bool {
	return method == string(domain.EvtReady) || method == string(domain.EvtPluginChanged)
}

func workspaceSeq(params json.RawMessage) int64 {
	var meta struct {
		WorkspaceSeq int64 `json:"workspaceSeq"`
	}
	_ = json.Unmarshal(params, &meta)
	return meta.WorkspaceSeq
}
