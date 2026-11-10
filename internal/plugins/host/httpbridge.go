package host

// 宿主出网桥（03 §5.5、09 §9.1）：provider 与 ctx.fetch 的唯一出口。
//
// 这里集中执行：协议检查、重定向逐跳检查、建连/空闲/总时长、取消贯穿、
// 响应大小上限与 SSE 帧解析。插件侧看不到 socket、DNS 或 TLS 细节。
// host 黑白名单暂未启用（后续以插件形式实现，见 AGENTS.md）：
// 当前只要协议合法即可出网，边界由响应上限与超时兜底。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// HTTPBridgeConfig 是桥的边界参数；零值取安全默认。
type HTTPBridgeConfig struct {
	// ConnectTimeout 覆盖建连与响应头；IdleTimeout 是 SSE 两次事件之间的上限；
	// TotalTimeout 是整次请求的上限。
	ConnectTimeout time.Duration
	IdleTimeout    time.Duration
	TotalTimeout   time.Duration
	// MaxJSONBytes / MaxSSEBytes 是响应体上限；0 取 16MB / 32MB。
	MaxJSONBytes int
	MaxSSEBytes  int
	// Transport 允许测试注入；nil 时使用带代理与超时的默认 Transport。
	Transport http.RoundTripper
	// DebugWriter 非空时把每次出网请求/响应摘要写到其中（密钥已脱敏）。
	// 用于 adag --debug；运行时不可切换。
	DebugWriter io.Writer
	// DebugLog 可直接注入已存在的调试器（与调用方共享请求序号）；
	// 非空时忽略 DebugWriter。
	DebugLog *DebugLog
}

// Bridge 实现 host.HTTPBridge（出网唯一出口）。
type Bridge struct {
	connect time.Duration
	idle    time.Duration
	total   time.Duration
	maxJSON int
	maxSSE  int
	client  *http.Client
	debug   *DebugLog

	baseTransport *http.Transport
	proxyMu       sync.Mutex
	proxyClients  map[string]*http.Client
}

var _ HTTPBridge = (*Bridge)(nil)

// NewHTTPBridge 创建出网桥。
// host 黑白名单暂未启用（后续以插件形式实现）：当前不限制目标主机，
// 协议（http/https）、重定向跳数、响应上限与超时仍然生效。
func NewHTTPBridge(cfg HTTPBridgeConfig) (*Bridge, error) {
	connect := cfg.ConnectTimeout
	if connect <= 0 {
		connect = domain.ProviderConnectTimeout
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = domain.ProviderIdleTimeout
	}
	total := cfg.TotalTimeout
	if total <= 0 {
		total = domain.ProviderTotalTimeout
	}
	maxJSON := cfg.MaxJSONBytes
	if maxJSON <= 0 {
		maxJSON = 16 << 20
	}
	maxSSE := cfg.MaxSSEBytes
	if maxSSE <= 0 {
		maxSSE = 32 << 20
	}
	debugLog := cfg.DebugLog
	if debugLog == nil {
		debugLog = NewDebugLog(cfg.DebugWriter)
	}
	b := &Bridge{
		connect: connect, idle: idle, total: total, maxJSON: maxJSON, maxSSE: maxSSE,
		debug:        debugLog,
		proxyClients: map[string]*http.Client{},
	}
	transport := cfg.Transport
	if transport == nil {
		transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   connect,
			ResponseHeaderTimeout: connect,
			ExpectContinueTimeout: time.Second,
		}
	}
	if base, ok := transport.(*http.Transport); ok {
		b.baseTransport = base
	}
	b.client = &http.Client{Transport: transport, CheckRedirect: b.checkRedirect}
	return b, nil
}

func (b *Bridge) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("host: HTTP 重定向超过 5 跳")
	}
	return b.checkURL(req.URL)
}

// clientForProxy 返回按供应商代理配置复用的 HTTP 客户端。
// 空代理复用默认客户端；代理 URL 只允许 http/https/socks5。
func (b *Bridge) clientForProxy(proxy string) (*http.Client, error) {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return b.client, nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" {
		return nil, domain.NewError(domain.CodeProviderError, "代理地址不合法: "+proxy)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5":
	default:
		return nil, domain.NewError(domain.CodeProviderError,
			"代理只支持 http/https/socks5，得到 "+u.Scheme)
	}
	b.proxyMu.Lock()
	defer b.proxyMu.Unlock()
	if c := b.proxyClients[proxy]; c != nil {
		return c, nil
	}
	var transport http.RoundTripper
	if b.baseTransport != nil {
		clone := b.baseTransport.Clone()
		clone.Proxy = http.ProxyURL(u)
		transport = clone
	} else {
		transport = &http.Transport{
			Proxy:               http.ProxyURL(u),
			DialContext:         (&net.Dialer{Timeout: b.connect, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        16,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: b.connect,
		}
	}
	c := &http.Client{Transport: transport, CheckRedirect: b.checkRedirect}
	b.proxyClients[proxy] = c
	return c, nil
}

// Close 释放连接池（测试与 Core 退出时调用）。
func (b *Bridge) Close() {
	if b == nil {
		return
	}
	if b.client != nil {
		b.client.CloseIdleConnections()
	}
	b.proxyMu.Lock()
	defer b.proxyMu.Unlock()
	for _, c := range b.proxyClients {
		c.CloseIdleConnections()
	}
	b.proxyClients = map[string]*http.Client{}
}

// JSON 发一次请求并返回原始响应体；非 2xx 也返回结果，由调用方决定语义
// （ctx.fetch 与 fetch 语义一致：HTTP 错误是响应，不是异常）。
func (b *Bridge) JSON(ctx context.Context, method, rawURL string, headers map[string]string, body string, maxBytes int) (HTTPResult, error) {
	return b.jsonWithClient(ctx, b.client, method, rawURL, headers, body, maxBytes)
}

// JSONWithProxy 与 JSON 相同，但用供应商配置的代理发请求。
func (b *Bridge) JSONWithProxy(ctx context.Context, method, rawURL string, headers map[string]string, body string, maxBytes int, proxy string) (HTTPResult, error) {
	client, err := b.clientForProxy(proxy)
	if err != nil {
		return HTTPResult{}, err
	}
	return b.jsonWithClient(ctx, client, method, rawURL, headers, body, maxBytes)
}

func (b *Bridge) jsonWithClient(ctx context.Context, client *http.Client, method, rawURL string, headers map[string]string, body string, maxBytes int) (HTTPResult, error) {
	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(method)
	if method != http.MethodGet && method != http.MethodPost {
		return HTTPResult{}, domain.NewError(domain.CodeProviderError,
			"HTTP 桥只允许 GET/POST，收到 "+method)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return HTTPResult{}, domain.WrapError(domain.NewError(domain.CodeProviderError,
			"HTTP URL 无法解析: "+rawURL), err)
	}
	if err := b.checkURL(u); err != nil {
		return HTTPResult{}, err
	}
	limit := maxBytes
	if limit <= 0 || limit > b.maxJSON {
		limit = b.maxJSON
	}
	reqCtx, cancel := context.WithTimeout(ctx, b.total)
	defer cancel()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, u.String(), rdr)
	if err != nil {
		return HTTPResult{}, domain.WrapError(domain.NewError(domain.CodeProviderError, "构造 HTTP 请求失败"), err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	debugID := b.debug.NextID()
	b.debug.Request(debugID, method, u.String(), req.Header, []byte(body))
	resp, err := client.Do(req)
	if err != nil {
		b.debug.Error(debugID, err, time.Since(start))
		return HTTPResult{}, wrapNetworkErr(err)
	}
	defer resp.Body.Close()
	raw, err := readLimited(resp.Body, limit)
	if err != nil {
		b.debug.Error(debugID, err, time.Since(start))
		return HTTPResult{}, domain.WrapError(domain.NewError(domain.CodeProviderError,
			fmt.Sprintf("读取 HTTP 响应失败（上限 %d 字节）", limit)), err)
	}
	b.debug.Response(debugID, resp.Status, resp.Header, time.Since(start), raw)
	return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, Body: json.RawMessage(raw)}, nil
}

// SSE 逐事件回调，直到显式终止（由插件识别）或 EOF/取消/错误。
//
// 非 2xx 返回 *HTTPError（带状态与 Retry-After），provider 桥据此决定是否重试。
func (b *Bridge) SSE(ctx context.Context, method, rawURL string, headers map[string]string, body string, onEvent func(event, data string) error) (HTTPResult, error) {
	return b.sseWithClient(ctx, b.client, method, rawURL, headers, body, onEvent)
}

// SSEWithProxy 与 SSE 相同，但用供应商配置的代理发请求。
func (b *Bridge) SSEWithProxy(ctx context.Context, method, rawURL string, headers map[string]string, body string, onEvent func(event, data string) error, proxy string) (HTTPResult, error) {
	client, err := b.clientForProxy(proxy)
	if err != nil {
		return HTTPResult{}, err
	}
	return b.sseWithClient(ctx, client, method, rawURL, headers, body, onEvent)
}

func (b *Bridge) sseWithClient(ctx context.Context, client *http.Client, method, rawURL string, headers map[string]string, body string, onEvent func(event, data string) error) (HTTPResult, error) {
	if onEvent == nil {
		return HTTPResult{}, errors.New("host: SSE 需要 onEvent 回调")
	}
	if method == "" {
		method = http.MethodPost
	}
	method = strings.ToUpper(method)
	if method != http.MethodGet && method != http.MethodPost {
		return HTTPResult{}, domain.NewError(domain.CodeProviderError,
			"HTTP 桥只允许 GET/POST，收到 "+method)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return HTTPResult{}, domain.WrapError(domain.NewError(domain.CodeProviderError,
			"SSE URL 无法解析: "+rawURL), err)
	}
	if err := b.checkURL(u); err != nil {
		return HTTPResult{}, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, b.total)
	defer cancel()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, u.String(), rdr)
	if err != nil {
		return HTTPResult{}, domain.WrapError(domain.NewError(domain.CodeProviderError, "构造 SSE 请求失败"), err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	start := time.Now()
	debugID := b.debug.NextID()
	b.debug.Request(debugID, method, u.String(), req.Header, []byte(body))
	resp, err := client.Do(req)
	if err != nil {
		b.debug.Error(debugID, err, time.Since(start))
		return HTTPResult{}, wrapNetworkErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := readLimited(resp.Body, 64<<10)
		b.debug.Response(debugID, resp.Status, resp.Header, time.Since(start), raw)
		return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, Body: json.RawMessage(raw)},
			newHTTPError(resp, strings.TrimSpace(string(raw)))
	}
	b.debug.Response(debugID, resp.Status, resp.Header, time.Since(start), nil)

	// 空闲期限：每次从连接读到数据就重置；到期取消请求。
	idleCtx, idleCancel := context.WithCancel(reqCtx)
	defer idleCancel()
	idleTimer := time.AfterFunc(b.idle, idleCancel)
	defer idleTimer.Stop()
	resetIdle := func() { idleTimer.Reset(b.idle) }

	br := bufio.NewReaderSize(resp.Body, 64<<10)
	var (
		event   string
		data    []string
		written int
		events  int
	)
	dispatch := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		payload := strings.Join(data, "\n")
		ev := event
		event = ""
		data = data[:0]
		events++
		if err := onEvent(ev, payload); err != nil {
			return err
		}
		return nil
	}

	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			written += len(line)
			if written > b.maxSSE {
				err := domain.NewError(domain.CodeProviderError,
					fmt.Sprintf("SSE 响应超过上限 %d 字节", b.maxSSE))
				b.debug.Error(debugID, err, time.Since(start))
				return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, SSEEvents: events}, err
			}
			resetIdle()
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if derr := dispatch(); derr != nil {
					return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, SSEEvents: events}, derr
				}
			case strings.HasPrefix(line, ":"):
				// 注释/心跳行。
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				v := line[len("data:"):]
				if strings.HasPrefix(v, " ") {
					v = v[1:]
				}
				data = append(data, v)
			case strings.HasPrefix(line, "id:"), strings.HasPrefix(line, "retry:"):
				// 断线重连提示由调用方自行决定；v1 不自动续传。
			default:
				// 不认识的字段按 SSE 规范忽略。
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// 规范要求以空行结束最后一条事件；容忍缺尾部空行，但不能丢事件。
				if derr := dispatch(); derr != nil {
					return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, SSEEvents: events}, derr
				}
				return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, SSEEvents: events}, nil
			}
			if idleCtx.Err() != nil {
				err := domain.NewError(domain.CodeProviderError,
					fmt.Sprintf("SSE 空闲超过 %s", b.idle))
				b.debug.Error(debugID, err, time.Since(start))
				return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, SSEEvents: events}, err
			}
			err = wrapNetworkErr(err)
			b.debug.Error(debugID, err, time.Since(start))
			return HTTPResult{Status: resp.StatusCode, Headers: resp.Header, SSEEvents: events}, err
		}
	}
}

// checkURL 做协议检查（也被重定向回调复用）。
// host 黑白名单暂未启用（后续以插件形式实现）：当前不限目标主机。
func (b *Bridge) checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return domain.NewError(domain.CodeProviderError, "HTTP 桥只允许 http(s)，收到 "+u.Scheme)
	}
	if u.User != nil {
		return domain.NewError(domain.CodeProviderError, "HTTP URL 不允许内嵌用户信息")
	}
	return nil
}

// HTTPError 是 SSE/JSON 的非 2xx 结果；provider 桥按 Status 决定是否重试。
type HTTPError struct {
	Status     int
	StatusText string
	RetryAfter time.Duration
	Body       string
}

func newHTTPError(resp *http.Response, body string) *HTTPError {
	e := &HTTPError{Status: resp.StatusCode, StatusText: resp.Status, Body: body}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("provider HTTP %s", e.StatusText)
	if e.Body != "" {
		b := e.Body
		if len(b) > 300 {
			b = b[:300] + "…"
		}
		msg += ": " + b
	}
	return msg
}

// Retryable 报告该状态是否属于 429/5xx（03 §5.5 只对这两类与网络错误重试）。
func (e *HTTPError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// RetryAfterDuration 是服务端建议的等待时长（可为 0）。
func (e *HTTPError) RetryAfterDuration() time.Duration { return e.RetryAfter }

// wrapNetworkErr 把传输层错误归一成 provider_error（可重试）。
func wrapNetworkErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return domain.WrapError(domain.NewError(domain.CodeTurnAborted, "HTTP 请求已取消"), err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.WrapError(domain.NewError(domain.CodeProviderError, "HTTP 请求超时"), err)
	}
	return domain.WrapError(domain.NewError(domain.CodeProviderError, "HTTP 请求失败: "+err.Error()), err)
}

// readLimited 读取至多 limit 字节；超出返回错误而不是静默截断。
func readLimited(r io.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("响应体超过 %d 字节", limit)
	}
	return raw, nil
}
