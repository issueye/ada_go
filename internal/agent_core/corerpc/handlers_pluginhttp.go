package corerpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/agent_core/coreproto"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/domain"
)

// ---- 插件 HTTP API（route）----

// handlePluginHTTP 调用插件注册的动态 HTTP 路由。
// 返回 {ok,result} 或 {ok:false,error,notFound|methodNotAllowed}，由 Gateway 映射 HTTP 状态。
func (s *Server) handlePluginHTTP(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		PluginID string            `json:"pluginId"`
		Method   string            `json:"method"`
		Path     string            `json:"path"`
		Query    map[string]string `json:"query"`
		Body     json.RawMessage   `json:"body"`
		ThreadID string            `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("plugin.http: " + err.Error())
	}
	pluginID := strings.TrimSpace(p.PluginID)
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	path := strings.TrimSpace(p.Path)
	if pluginID == "" || path == "" || (method != "GET" && method != "POST") {
		return nil, invalid("plugin.http: 需要 pluginId、method(GET/POST) 与 path")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, invalid("plugin.http: path 必须以 / 开头")
	}
	envelope := map[string]any{"method": method, "path": path, "query": p.Query}
	if len(p.Body) > 0 && string(p.Body) != "null" {
		var body any
		if err := json.Unmarshal(p.Body, &body); err != nil {
			return nil, invalid("plugin.http: body 必须是合法 JSON")
		}
		envelope["body"] = body
	}
	envRaw, err := json.Marshal(envelope)
	if err != nil {
		return nil, invalid("plugin.http: 请求无法编码")
	}
	out, err := s.sess.CallHTTPAPI(ctx, pluginID, method, path, envRaw, p.ThreadID)
	if err != nil {
		var he *corecontracts.HTTPAPIError
		if errors.As(err, &he) {
			res := map[string]any{"ok": false, "error": he.Message, "status": he.Status}
			switch he.Status {
			case 404:
				res["notFound"] = true
			case 405:
				res["methodNotAllowed"] = true
			}
			return res, nil
		}
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	var result any
	if len(out) > 0 && string(out) != "null" {
		if err := json.Unmarshal(out, &result); err != nil {
			return map[string]any{"ok": false, "error": "插件返回的不是合法 JSON"}, nil
		}
	}
	return map[string]any{"ok": true, "result": result}, nil
}

// handlePluginHTTPStream 管理 SSE 路由的三段式生命周期：
//
//	prepare（登记，不启动）→ Gateway 写完 SSE 响应头 → start（启动）→ cancel（断开时取消）
//
// prepare 与 start 分开是为了消除「订阅建立前插件已经开始推帧」的丢帧窗口。
func (s *Server) handlePluginHTTPStream(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Action   string            `json:"action"`
		StreamID string            `json:"streamId"`
		PluginID string            `json:"pluginId"`
		Method   string            `json:"method"`
		Path     string            `json:"path"`
		Query    map[string]string `json:"query"`
		ThreadID string            `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("plugin.httpStream: " + err.Error())
	}
	action := strings.TrimSpace(p.Action)
	streamID := strings.TrimSpace(p.StreamID)
	switch action {
	case "prepare":
		pluginID := strings.TrimSpace(p.PluginID)
		method := strings.ToUpper(strings.TrimSpace(p.Method))
		path := strings.TrimSpace(p.Path)
		if pluginID == "" || path == "" || method != "GET" {
			return nil, invalid("plugin.httpStream.prepare: 需要 pluginId、method=GET 与 path")
		}
		if !strings.HasPrefix(path, "/") {
			return nil, invalid("plugin.httpStream.prepare: path 必须以 / 开头")
		}
		if !strings.HasPrefix(streamID, "ps_") || len(streamID) < 4 {
			return nil, invalid("plugin.httpStream.prepare: streamId 必须以 ps_ 开头")
		}
		envelope, err := json.Marshal(map[string]any{"method": method, "path": path, "query": p.Query})
		if err != nil {
			return nil, invalid("plugin.httpStream.prepare: 请求无法编码")
		}
		// 同步校验路由存在且是 SSE：Gateway 在写响应头之前就能返回 404/405/406。
		if err := s.sess.CheckHTTPAPIStream(pluginID, method, path); err != nil {
			status, message := 502, err.Error()
			var he *corecontracts.HTTPAPIError
			if errors.As(err, &he) {
				status, message = he.Status, he.Message
			}
			return map[string]any{"prepared": false, "status": status, "error": message}, nil
		}
		s.streamMu.Lock()
		// 回收「prepare 后连接就断了、从未 start」的登记，避免长时间运行积累。
		for id, stale := range s.streams {
			if !stale.started && !stale.prepared.IsZero() && time.Since(stale.prepared) > time.Minute {
				delete(s.streams, id)
			}
		}
		if _, exists := s.streams[streamID]; exists {
			s.streamMu.Unlock()
			return nil, invalid("plugin.httpStream.prepare: streamId 已存在")
		}
		s.streams[streamID] = &pluginStream{
			pluginID: pluginID, method: method, path: path,
			threadID: strings.TrimSpace(p.ThreadID), req: envelope, prepared: time.Now(),
		}
		s.streamMu.Unlock()
		return map[string]any{"streamId": streamID, "prepared": true}, nil
	case "start":
		s.streamMu.Lock()
		st := s.streams[streamID]
		if st == nil || st.started {
			s.streamMu.Unlock()
			return nil, invalid("plugin.httpStream.start: 未 prepare 或已启动")
		}
		ctx, cancel := context.WithCancel(s.root)
		st.cancel = cancel
		st.started = true
		snapshot := *st
		s.streamMu.Unlock()
		go s.runPluginStream(ctx, streamID, snapshot)
		return map[string]any{"streamId": streamID, "started": true}, nil
	case "cancel":
		s.streamMu.Lock()
		st := s.streams[streamID]
		if st != nil {
			delete(s.streams, streamID)
			if st.cancel != nil {
				st.cancel()
			}
		}
		s.streamMu.Unlock()
		return map[string]any{"streamId": streamID, "cancelled": st != nil}, nil
	default:
		return nil, invalid("plugin.httpStream: action 只能是 prepare / start / cancel")
	}
}

func (s *Server) runPluginStream(ctx context.Context, streamID string, st pluginStream) {
	defer func() {
		s.streamMu.Lock()
		delete(s.streams, streamID)
		s.streamMu.Unlock()
	}()
	err := s.sess.CallHTTPAPIStream(ctx, st.pluginID, st.method, st.path, st.req, st.threadID,
		func(ev host.RouteEvent) error {
			return s.emitPluginStream(streamID, ev.Event, ev.Data, false, "")
		})
	if err != nil {
		if ctx.Err() == nil { // 客户端主动断开时不额外报错
			_ = s.emitPluginStream(streamID, "", nil, true, err.Error())
		}
		return
	}
	_ = s.emitPluginStream(streamID, "", nil, true, "")
}

// emitPluginStream 把一帧/终止帧转成 evt.plugin.stream 通知并扇出。
// data 保持 JSON 原文；超过单条消息上限时 NewEvent 拒绝，调用方据此中断流。
func (s *Server) emitPluginStream(streamID, event string, data json.RawMessage, done bool, errMsg string) error {
	payload := map[string]any{"streamId": streamID}
	if event != "" {
		payload["event"] = event
	}
	if len(data) > 0 && string(data) != "null" {
		payload["data"] = data
	}
	if done {
		payload["done"] = true
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	note, err := coreproto.NewEvent(string(domain.EvtPluginStream), payload)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(note)
	if err != nil {
		return err
	}
	s.fanout(raw)
	return nil
}
