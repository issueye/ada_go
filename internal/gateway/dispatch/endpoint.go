package dispatch

// 内部发送 API：由网关拉起的进程（Core、插件辅助进程）用它提交消息。
//
// 为什么不是让插件直接打 /threads/{id}/send：
//   - 那条路径面向浏览器会话（cookie + CSRF），进程没有也不该有这些；
//   - 它假设调用方是「用户」，没有身份、没有审计归属；
//   - 它把「建线程 / 排队 / busy」这些语义写进了页面处理器，进程得重写一遍。
//
// 这里的边界只有两条：token 认进程，sender 由服务端盖章。
// 请求体里的 kind/id 一律忽略——伪造的 sender 不会生效。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

const (
	// ControlTokenHeader 携带控制令牌（Bearer）。
	ControlTokenHeader = "Authorization"
	// DefaultControlBodyBytes 是内部发送请求体上限：提示词与图片参数都在这条链上。
	DefaultControlBodyBytes = 16 << 20
	// DefaultControlTimeout 是单次内部投递的服务端上限。
	DefaultControlTimeout = 60 * time.Second
)

// Identity 是一个控制令牌代表的进程。
//
// Workspace 是该 Core 服务的默认工作区；CoreID 是诊断用的实例标识
// （不参与授权判定，只进审计）；Key 是签发键，用于「同键重签作废旧令牌」。
type Identity struct {
	Workspace string `json:"workspace,omitempty"`
	CoreID    string `json:"coreId,omitempty"`
	Key       string `json:"-"`
}

// Tokens 是控制令牌表：网关签发、进程持有、插件看不到。
//
// 同一个键（工作区）重新签发会作废旧令牌：Core 重启后旧令牌不该继续可用，
// 表也不会随重启次数无限增长。
type Tokens struct {
	mu      sync.RWMutex
	byToken map[string]Identity
	byKey   map[string]string
}

// NewTokens 创建空表。
func NewTokens() *Tokens {
	return &Tokens{byToken: map[string]Identity{}, byKey: map[string]string{}}
}

// Issue 登记一个令牌。空令牌直接拒绝（否则「没有 token」会成为合法凭据）。
func (t *Tokens) Issue(token string, ident Identity) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("dispatch: 控制令牌不能为空")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byToken[token] = ident
	return nil
}

// IssueFor 为一个键签发新令牌，并作废该键此前签发的令牌。
//
// 返回的令牌只交给「刚被拉起的那个进程」；插件与脚本永远拿不到。
func (t *Tokens) IssueFor(key string, ident Identity) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("dispatch: 签发控制令牌需要键（工作区）")
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.byKey[key]; ok {
		delete(t.byToken, prev)
	}
	t.byKey[key] = token
	t.byToken[token] = ident
	return token, nil
}

// Lookup 查令牌代表的进程。
func (t *Tokens) Lookup(token string) (Identity, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Identity{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	ident, ok := t.byToken[token]
	return ident, ok
}

// Revoke 注销令牌（进程退出或网关关闭时）。
func (t *Tokens) Revoke(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if ident, ok := t.byToken[token]; ok {
		delete(t.byToken, token)
		if key, has := t.byKey[ident.Key]; has && key == token {
			delete(t.byKey, ident.Key)
		}
	}
}

// Len 返回令牌数量（诊断与测试用）。
func (t *Tokens) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byToken)
}

// ControlRequest 是 POST /internal/dispatch/send 的请求体。
//
// 只读 pluginId/taskId：kind 与 id 由服务端盖章，body 里的同名字段被忽略。
type ControlRequest struct {
	Workspace      string `json:"workspace,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	Title          string `json:"title,omitempty"`
	Text           string `json:"text,omitempty"`
	Mode           string `json:"mode,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// PluginID/TaskID 由 Core 的宿主桥按能力集盖章（不是脚本能传的字段）。
	PluginID string `json:"pluginId,omitempty"`
	TaskID   string `json:"taskId,omitempty"`
	// Images 与 turn.start 的 images 同形（type/mime/data）。
	Images []ControlImage `json:"images,omitempty"`
}

// ControlImage 是一张图片。
type ControlImage struct {
	Type string `json:"type,omitempty"`
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
}

// ControlResponse 是成功回执；失败一律是 {"error": {...}}。
type ControlResponse struct {
	Receipt Receipt `json:"receipt"`
}

// ControlError 是失败体的形状：调用方按 code 分支，不解析文案。
type ControlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EndpointConfig 装配内部发送端点。
type EndpointConfig struct {
	Dispatcher *Dispatcher
	Tokens     *Tokens
	// MaxBodyBytes 为 0 时取 DefaultControlBodyBytes。
	MaxBodyBytes int64
	// Timeout 为 0 时取 DefaultControlTimeout。
	Timeout time.Duration
	// Log 记录每次内部投递（token 身份 + sender + 结果）；nil 时静默。
	Log func(ident Identity, sender Sender, req SendRequest, receipt Receipt, err error)
}

// Endpoint 是内部发送 API 的 HTTP 面。
type Endpoint struct {
	dispatcher *Dispatcher
	tokens     *Tokens
	maxBody    int64
	timeout    time.Duration
	log        func(Identity, Sender, SendRequest, Receipt, error)
	handler    http.Handler
}

// NewEndpoint 创建端点。缺件直接失败（INV-2）。
func NewEndpoint(cfg EndpointConfig) (*Endpoint, error) {
	if cfg.Dispatcher == nil {
		return nil, fmt.Errorf("dispatch: 内部端点需要 Dispatcher")
	}
	if cfg.Tokens == nil {
		return nil, fmt.Errorf("dispatch: 内部端点需要令牌表")
	}
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultControlBodyBytes
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultControlTimeout
	}
	logf := cfg.Log
	if logf == nil {
		logf = func(Identity, Sender, SendRequest, Receipt, error) {}
	}
	return &Endpoint{dispatcher: cfg.Dispatcher, tokens: cfg.Tokens,
		maxBody: maxBody, timeout: timeout, log: logf}, nil
}

// Handler 返回内部端点自己的 handler（与 Register 同源）。
//
// 它**不套用**用户鉴权中间件：进程没有会话 cookie，也不该有。
func (e *Endpoint) Handler() http.Handler {
	if e.handler == nil {
		mux := http.NewServeMux()
		e.Register(mux)
		e.handler = mux
	}
	return e.handler
}

// Register 把两个内部路由挂到 mux 上。
//
// 它们必须挂在**用户鉴权之外**：进程没有会话 cookie，也不该有。
// 认证只靠控制令牌，且只接受回环来源。
func (e *Endpoint) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/dispatch/send", e.handleSend)
	mux.HandleFunc("GET /internal/dispatch/workspaces", e.handleWorkspaces)
}

func (e *Endpoint) handleSend(w http.ResponseWriter, r *http.Request) {
	if !loopbackRequest(r) {
		writeControlError(w, http.StatusForbidden, "forbidden", "内部端点只接受回环来源")
		return
	}
	ident, ok := e.authenticate(r)
	if !ok {
		writeControlError(w, http.StatusUnauthorized, "unauthorized", "缺少或无效的控制令牌")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, e.maxBody)
	var body ControlRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&body); err != nil {
		writeControlError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON 或超过大小上限")
		return
	}

	sender := senderOf(ident, body)
	req := SendRequest{
		Workspace:      body.Workspace,
		ThreadID:       body.ThreadID,
		Title:          body.Title,
		Text:           body.Text,
		Images:         imagePartsFrom(body.Images),
		Mode:           DeliveryMode(strings.TrimSpace(body.Mode)),
		IdempotencyKey: body.IdempotencyKey,
	}

	ctx, cancel := context.WithTimeout(r.Context(), e.timeout)
	defer cancel()
	receipt, err := e.dispatcher.Send(ctx, sender, req)
	e.log(ident, sender, req, receipt, err)
	if err != nil {
		code := CodeOf(err)
		if code == "" {
			// 传输层失败（Core 不可用之类）：502，让调用方区分「没发出去」与「被拒绝」。
			writeControlError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		writeControlError(w, controlStatus(code), code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ControlResponse{Receipt: receipt})
}

func (e *Endpoint) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !loopbackRequest(r) {
		writeControlError(w, http.StatusForbidden, "forbidden", "内部端点只接受回环来源")
		return
	}
	ident, ok := e.authenticate(r)
	if !ok {
		writeControlError(w, http.StatusUnauthorized, "unauthorized", "缺少或无效的控制令牌")
		return
	}
	list := e.dispatcher.ListWorkspaces(r.Context(), senderOf(ident, ControlRequest{}))
	if list == nil {
		list = []Workspace{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default":    e.dispatcher.DefaultWorkspace(),
		"workspaces": list,
	})
}

// authenticate 取 Bearer 令牌并查表。
func (e *Endpoint) authenticate(r *http.Request) (Identity, bool) {
	token := strings.TrimSpace(r.Header.Get(ControlTokenHeader))
	if token == "" {
		return Identity{}, false
	}
	if after, ok := strings.CutPrefix(token, "Bearer "); ok {
		token = strings.TrimSpace(after)
	}
	return e.tokens.Lookup(token)
}

// senderOf 由服务端盖章：kind 固定为 plugin/system，id 由 pluginId 派生。
// body 里的 kind/id 不被读取——伪造它们没有任何效果。
func senderOf(ident Identity, body ControlRequest) Sender {
	pluginID := strings.TrimSpace(body.PluginID)
	if pluginID == "" {
		id := "control"
		if ident.CoreID != "" {
			id = "control:" + ident.CoreID
		}
		return Sender{Kind: SenderSystem, ID: id}
	}
	return Sender{Kind: SenderPlugin, ID: "plugin:" + pluginID,
		PluginID: pluginID, TaskID: strings.TrimSpace(body.TaskID)}
}

// controlStatus 把投递业务原因映射成 HTTP 状态。
func controlStatus(code string) int {
	switch code {
	case CodeThreadBusy, CodeWaitingInput, CodeFinishing, CodeNotRunning:
		return http.StatusConflict
	case CodeEmptyMessage, CodeWorkspaceMissing, CodeThreadMissing:
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

func writeControlError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": ControlError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// loopbackRequest 只认回环来源：网关只监听回环，但内部端点不该依赖这个前提。
func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// randomToken 生成 24 字节的十六进制令牌（与网关访问令牌同形）。
func randomToken() (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("dispatch: 生成控制令牌失败: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// imagePartsFrom 把内部请求的图片转成 domain.Part（与 turn.start 同形）。
func imagePartsFrom(images []ControlImage) []domain.Part {
	if len(images) == 0 {
		return nil
	}
	out := make([]domain.Part, 0, len(images))
	for _, img := range images {
		partType := img.Type
		if partType == "" {
			partType = string(domain.PartImage)
		}
		out = append(out, domain.Part{Type: domain.PartType(partType), Mime: img.Mime, Data: img.Data})
	}
	return out
}
