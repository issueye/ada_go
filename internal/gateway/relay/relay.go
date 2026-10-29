// Package relay 是 Gateway 侧到 Core 的 WebSocket JSON-RPC 客户端（设计 8.1）。
//
// 一个 Client 复用一个 WebSocket 连接：
//   - Call 并发调用，用 JSON-RPC id 关联响应；
//   - 读循环把 method 非空且 id=0 的消息当作 evt.* 通知，分发给所有订阅者；
//   - 连接断开后所有在途 Call 失败、订阅通道关闭，后续 Call 立即失败。
//
// 本包刻意不做自动重连：重启由 supervisor 负责，relay 只暴露 Close/Err
// 让上层判断连接是否还活着。
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/proto"
)

const (
	// dialTimeout 是 Dial 自身的上限；调用方 context 已有 deadline 时不覆盖。
	dialTimeout = 10 * time.Second
	// writeTimeout 是单条请求写出的上限。
	writeTimeout = 10 * time.Second
)

var (
	// ErrClosed 表示客户端已被主动关闭。
	ErrClosed = errors.New("relay: 连接已关闭")
	// ErrConnectionLost 表示与 Core 的连接意外断开。
	ErrConnectionLost = errors.New("relay: 与 Core 的连接已断开")
)

// RPCError 是 Core 返回的业务错误信封。
//
// Data 保留服务端原文，避免在 relay 层按某一种 data 形状解码而丢字段。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error 让 RPCError 满足 error，便于调用方用 errors.As 取业务码。
func (e *RPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("relay: RPC 错误 %d: %s", e.Code, e.Message)
}

// Client 是一个到 Core 的 JSON-RPC 连接。
type Client struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	writeMu sync.Mutex
	nextID  atomic.Int64
	dropped atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan *wireResponse
	subs     map[*subscriber]struct{}
	closed   bool
	terminal error

	done      chan struct{}
	closeOnce sync.Once
}

type subscriber struct {
	ch chan contracts.Event
}

// wireResponse 是读循环与 Call 之间传递的最小响应。
type wireResponse struct {
	Result json.RawMessage
	Error  *RPCError
}

// 组合根的编译期断言：Dial 可直接作为 contracts.DialFunc 注入 supervisor。
var (
	_ contracts.DialFunc   = Dial
	_ contracts.CoreClient = (*Client)(nil)
)

// Dial 建立到 Core 的 WebSocket 连接。
//
// endpoint 接受 "127.0.0.1:port"、"http://host:port"、"ws://host:port/rpc" 等形式；
// 没有路径时自动补 /rpc。token 非空时通过 Authorization: Bearer 发送。
func Dial(ctx context.Context, endpoint, token string) (contracts.CoreClient, error) {
	target, err := rpcURL(endpoint)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dialCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, dialTimeout)
		defer cancel()
	}

	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, fmt.Errorf("relay: 连接 Core 失败: %w", err)
	}
	// coder/websocket 默认只读 32KB，而单条 JSON-RPC 消息允许 2MB（08 §8.1）。
	// 不放开的话，带工具输出的 thread.get 响应就会把连接直接读断。
	conn.SetReadLimit(domain.RPCMessageMaxBytes)

	connCtx, cancel := context.WithCancel(context.Background())
	c := &Client{
		conn:    conn,
		ctx:     connCtx,
		cancel:  cancel,
		pending: map[int64]chan *wireResponse{},
		subs:    map[*subscriber]struct{}{},
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// rpcURL 把各种 endpoint 形状归一成 ws(s)://host/rpc。
func rpcURL(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", errors.New("relay: 需要 Core endpoint")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "ws://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("relay: endpoint 无法解析: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("relay: 不支持的 endpoint 协议 %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("relay: endpoint 缺少 host")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/rpc"
	}
	return u.String(), nil
}

// Call 发起一次 JSON-RPC 调用；并发调用按 id 关联。
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.isClosed() {
		return nil, c.Err()
	}

	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("relay: params 编码失败: %w", err)
		}
		if string(encoded) != "null" {
			raw = encoded
		}
	}
	req := proto.Request{JSONRPC: "2.0", ID: c.nextID.Add(1), Method: method, Params: raw}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("relay: 请求编码失败: %w", err)
	}

	respCh := make(chan *wireResponse, 1)
	c.mu.Lock()
	if c.closed {
		err := c.terminal
		c.mu.Unlock()
		return nil, err
	}
	c.pending[req.ID] = respCh
	c.mu.Unlock()

	c.writeMu.Lock()
	writeCtx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	err = c.conn.Write(writeCtx, websocket.MessageText, data)
	cancel()
	c.writeMu.Unlock()
	if err != nil {
		c.removePending(req.ID)
		c.terminate(fmt.Errorf("%w: %v", ErrConnectionLost, err))
		return nil, fmt.Errorf("relay: 请求写出失败: %w", err)
	}

	select {
	case <-ctx.Done():
		c.removePending(req.ID)
		return nil, ctx.Err()
	case <-c.done:
		c.removePending(req.ID)
		return nil, c.Err()
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, resp.Error
		}
		if len(resp.Result) == 0 {
			return nil, nil
		}
		return resp.Result, nil
	}
}

// Subscribe 注册一个事件订阅者。通道关闭表示连接结束。
//
// 慢订阅者不会阻塞读循环：队列满时丢弃该条事件并计入 Dropped。
func (c *Client) Subscribe(buffer int) (<-chan contracts.Event, func()) {
	if buffer < 0 {
		buffer = 0
	}
	s := &subscriber{ch: make(chan contracts.Event, buffer)}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		close(s.ch)
		return s.ch, func() {}
	}
	c.subs[s] = struct{}{}
	c.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.mu.Lock()
			if _, ok := c.subs[s]; ok {
				delete(c.subs, s)
				close(s.ch)
			}
			c.mu.Unlock()
		})
	}
	return s.ch, cancel
}

// Close 幂等：关闭连接、取消读循环并关闭所有订阅通道。
func (c *Client) Close() error {
	c.terminate(ErrClosed)
	return nil
}

// Err 返回连接终止原因；连接仍然可用时返回 nil。
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		return nil
	}
	return c.terminal
}

// Dropped 返回因订阅队列满而丢弃的事件总数（观测用）。
func (c *Client) Dropped() int64 { return c.dropped.Load() }

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Client) readLoop() {
	for {
		typ, data, err := c.conn.Read(c.ctx)
		if err != nil {
			c.terminate(fmt.Errorf("%w: %v", ErrConnectionLost, err))
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		// 事件是 JSON-RPC notification：method 非空且无 id（8.1）。
		if msg.Method != "" && msg.ID == 0 {
			c.dispatch(contracts.Event{Method: msg.Method, Params: msg.Params})
			continue
		}

		c.mu.Lock()
		ch := c.pending[msg.ID]
		if ch != nil {
			delete(c.pending, msg.ID)
		}
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		select {
		case ch <- &wireResponse{Result: msg.Result, Error: msg.Error}:
		default:
		}
	}
}

func (c *Client) dispatch(evt contracts.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for s := range c.subs {
		select {
		case s.ch <- evt:
		default:
			c.dropped.Add(1)
		}
	}
}

func (c *Client) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// terminate 是唯一的终止路径：Close、写失败与读循环断开都汇到这里。
func (c *Client) terminate(err error) {
	c.closeOnce.Do(func() {
		if err == nil {
			err = ErrConnectionLost
		}
		c.cancel()

		c.mu.Lock()
		c.closed = true
		c.terminal = err
		subs := make([]*subscriber, 0, len(c.subs))
		for s := range c.subs {
			subs = append(subs, s)
		}
		c.subs = map[*subscriber]struct{}{}
		c.pending = map[int64]chan *wireResponse{}
		c.mu.Unlock()

		for _, s := range subs {
			close(s.ch)
		}
		close(c.done)
		_ = c.conn.Close(websocket.StatusNormalClosure, "relay closing")
	})
}
