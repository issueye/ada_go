package corerpc

// WebSocket 传输：回环校验 + token + JSON-RPC 请求/响应 + evt.* 通知广播。

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/proto"
)

const (
	// writeTimeout 是单条消息写出的上限；事件扇出不等待慢客户端。
	writeTimeout = 5 * time.Second
	// requestTimeout 是单次 RPC 处理上限（turn.start 只启动 goroutine，不在此等待）。
	requestTimeout = 30 * time.Second
	// eventQueue 是每个连接的待发事件缓冲；满时丢弃该连接的最新事件而不是阻塞轮次。
	eventQueue = 512
)

// Handler 返回 HTTP handler：/rpc 是 WebSocket，/healthz 是健康检查。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/rpc", s.handleRPC)
	return mux
}

// Serve 在给定 listener 上提供服务；ctx 取消时优雅关闭。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpSrv := &http.Server{Handler: s.Handler()}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
			return
		}
		s.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	err := httpSrv.Serve(ln)
	close(stop)
	s.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c := newClient(conn)
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	go c.writeLoop()

	for {
		typ, data, err := conn.Read(r.Context())
		if err != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		var req proto.Request
		if err := json.Unmarshal(data, &req); err != nil {
			c.enqueue(mustJSON(proto.Response{
				JSONRPC: "2.0",
				Error:   &proto.RPCError{Code: coreproto.ErrParse, Message: "JSON 解析失败: " + err.Error()},
			}))
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		resp := s.core.Handle(ctx, req)
		cancel()
		c.enqueue(mustJSON(resp))
	}
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	c.close()
}

// authorize 只接受回环连接；token 非空时还必须匹配 Bearer 或 ?token=。
func (s *Server) authorize(r *http.Request) error {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("corerpc: 只接受回环连接")
	}
	// 浏览器场景：带 Origin 的 WebSocket 只允许回环来源，避免任意网页驱动本地 Core
	// （token 非空时仍需通过下面的 token 校验）。
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return errors.New("corerpc: Origin 无法解析")
		}
		oip := net.ParseIP(u.Hostname())
		if oip == nil || !oip.IsLoopback() {
			return errors.New("corerpc: 只接受回环 Origin")
		}
	}
	if s.token == "" {
		return nil
	}
	auth := r.Header.Get("Authorization")
	if stringsHasBearer(auth, s.token) {
		return nil
	}
	if r.URL.Query().Get("token") == s.token {
		return nil
	}
	return errors.New("corerpc: token 不匹配")
}

func stringsHasBearer(auth, token string) bool {
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return false
	}
	return auth[len(prefix):] == token
}

// client 是一个 WebSocket 连接：独立写协程避免慢客户端阻塞事件发射。
type client struct {
	conn   *websocket.Conn
	send   chan []byte
	closed chan struct{}

	once sync.Once
}

func newClient(conn *websocket.Conn) *client {
	return &client{conn: conn, send: make(chan []byte, eventQueue), closed: make(chan struct{})}
}

func (c *client) enqueue(raw []byte) {
	select {
	case <-c.closed:
		return
	default:
	}
	select {
	case c.send <- raw:
	case <-c.closed:
	default:
		// 队列满：丢弃该连接的这条事件，由客户端用 thread.get 做全量重同步。
	}
}

func (c *client) writeLoop() {
	for {
		select {
		case <-c.closed:
			return
		case raw := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, raw)
			cancel()
			if err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.conn.Close(websocket.StatusNormalClosure, "core closing")
	})
}

// broadcast 把领域事件转成 evt.* 通知并扇出。
func (s *Server) broadcast(evt domain.Event) {
	note, err := notificationFor(evt)
	if err != nil {
		return
	}
	raw, err := json.Marshal(note)
	if err != nil {
		return
	}
	s.mu.Lock()
	conns := make([]*client, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.enqueue(raw)
	}
}

// notificationFor 把 domain.Event 补齐协议事件所需的 threadId/seq/at。
func notificationFor(evt domain.Event) (*proto.Notification, error) {
	payload := map[string]any{}
	if len(evt.Payload) > 0 {
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			return nil, err
		}
	}
	if evt.ThreadID != "" {
		payload["threadId"] = evt.ThreadID
	}
	if evt.Seq != 0 {
		payload["seq"] = evt.Seq
	}
	if _, ok := payload["at"]; !ok && !evt.At.IsZero() {
		payload["at"] = evt.At.UTC().Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &proto.Notification{JSONRPC: "2.0", Method: string(evt.Kind), Params: raw}, nil
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"响应编码失败"}}`)
	}
	return raw
}
