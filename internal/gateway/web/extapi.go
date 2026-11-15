package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleExtAPI 是插件动态 HTTP API 的 Gateway 入口（route）。
// 请求经 Core RPC plugin.http 转发到插件路由；返回插件结果 JSON。
// 认证由外部安全中间件负责（cookie/Bearer）；写方法额外要求 CSRF（同一中间件）。
func (s *Server) handleExtAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIError(w, http.StatusNotFound, "缺少插件 id")
		return
	}
	if wantsEventStream(r) {
		s.handleExtAPIStream(w, r, id)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "只支持 GET / POST")
		return
	}
	apiPath := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	if len(apiPath) > 1 {
		apiPath = strings.TrimSuffix(apiPath, "/")
	}
	query := map[string]string{}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}
	var body any
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "请求体读取失败（上限 1MB）")
			return
		}
		if text := strings.TrimSpace(string(raw)); text != "" {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "请求体必须是合法 JSON")
				return
			}
		}
	}
	params := map[string]any{
		"pluginId": id,
		"method":   r.Method,
		"path":     apiPath,
		"query":    query,
	}
	// 线程上下文：显式 query 优先；否则从页面路径/Referer 推断。
	// 插件 route 的 ctx.state 按这个线程命名空间读写（见 host.StateOp.ThreadID）。
	if threadID := ThreadIDFromRequest(r); threadID != "" {
		params["threadId"] = threadID
	}
	if body != nil {
		params["body"] = body
	}
	raw, err := s.call(r, "plugin.http", params)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "插件接口暂时不可用："+publicReason(err))
		return
	}
	var res struct {
		Ok               bool            `json:"ok"`
		Result           json.RawMessage `json:"result"`
		Error            string          `json:"error"`
		Status           int             `json:"status"`
		NotFound         bool            `json:"notFound"`
		MethodNotAllowed bool            `json:"methodNotAllowed"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		writeAPIError(w, http.StatusBadGateway, "Core 返回了无法识别的接口结果")
		return
	}
	if !res.Ok {
		status := http.StatusBadGateway
		switch {
		case res.Status >= 400 && res.Status <= 599:
			status = res.Status
		case res.NotFound:
			status = http.StatusNotFound
		case res.MethodNotAllowed:
			status = http.StatusMethodNotAllowed
		}
		msg := strings.TrimSpace(res.Error)
		if msg == "" {
			msg = "插件接口调用失败"
		}
		writeAPIError(w, status, msg)
		return
	}
	if len(res.Result) == 0 {
		res.Result = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Result)
}

// writeAPIError 输出与 action.invoke 一致的 JSON 错误形状（前端 fetch 可统一解析）。
func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": message},
	})
}

// wantsEventStream 判断浏览器是否希望以 SSE 消费插件接口（EventSource 会带 Accept）。
func wantsEventStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// handleExtAPIStream 是 SSE 路由的 Gateway 端：
//
//	订阅 Core 事件 → prepare 登记 → 写 SSE 头 → start → 转发 evt.plugin.stream
//
// prepare/start 分开保证「插件开始推帧」发生在订阅与响应头都就绪之后。
func (s *Server) handleExtAPIStream(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "SSE 接口只支持 GET")
		return
	}
	apiPath := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	if len(apiPath) > 1 {
		apiPath = strings.TrimSuffix(apiPath, "/")
	}
	query := map[string]string{}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}

	core, release, err := s.acquire(r.Context(), r)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "Core 暂时不可用："+publicReason(err))
		return
	}
	defer release()

	events, unsubscribe := core.Subscribe(1024)
	defer unsubscribe()

	streamID := newPluginStreamID()
	prepare := map[string]any{
		"action": "prepare", "streamId": streamID, "pluginId": id,
		"method": http.MethodGet, "path": apiPath, "query": query,
	}
	if threadID := ThreadIDFromRequest(r); threadID != "" {
		prepare["threadId"] = threadID
	}
	raw, err := core.Call(r.Context(), "plugin.httpStream", prepare)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "插件接口暂时不可用："+publicReason(err))
		return
	}
	var prepared struct {
		Prepared bool   `json:"prepared"`
		Status   int    `json:"status"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &prepared); err != nil {
		writeAPIError(w, http.StatusBadGateway, "Core 返回了无法识别的流结果")
		return
	}
	if !prepared.Prepared {
		status := prepared.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		message := strings.TrimSpace(prepared.Error)
		if message == "" {
			message = "无法打开插件事件流"
		}
		writeAPIError(w, status, message)
		return
	}

	// 断开时通知 Core 取消插件处理器；即使已自然结束，cancel 也是幂等的。
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = core.Call(ctx, "plugin.httpStream", map[string]any{"action": "cancel", "streamId": streamID})
	}()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}

	startCtx, cancelStart := context.WithTimeout(r.Context(), 5*time.Second)
	_, err = core.Call(startCtx, "plugin.httpStream", map[string]any{"action": "start", "streamId": streamID})
	cancelStart()
	if err != nil {
		_ = writeSSE(w, "error", map[string]any{"error": map[string]any{"message": publicReason(err)}})
		_ = writeSSE(w, "done", map[string]any{})
		_ = rc.Flush()
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Method != "evt.plugin.stream" {
				continue
			}
			var frame struct {
				StreamID string          `json:"streamId"`
				Event    string          `json:"event"`
				Data     json.RawMessage `json:"data"`
				Done     bool            `json:"done"`
				Error    string          `json:"error"`
			}
			if json.Unmarshal(ev.Params, &frame) != nil || frame.StreamID != streamID {
				continue
			}
			if frame.Done {
				if frame.Error != "" {
					_ = writeSSE(w, "error", map[string]any{"error": map[string]any{"message": frame.Error}})
				}
				_ = writeSSE(w, "done", map[string]any{})
				_ = rc.Flush()
				return
			}
			data := frame.Data
			if len(data) == 0 {
				data = json.RawMessage("null")
			}
			if err := writeSSE(w, frame.Event, data); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// writeSSE 写一帧 EventSource 数据；data 按行拆成多个 data: 字段。
func writeSSE(w io.Writer, event string, data any) error {
	name := strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "", ":", "").Replace(event))
	if name == "" {
		name = "message"
	}
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("null")
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", name); err != nil {
		return err
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err = io.WriteString(w, "\n")
	return err
}

func newPluginStreamID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("ps_%d", time.Now().UnixNano())
	}
	return "ps_" + hex.EncodeToString(buf[:])
}
