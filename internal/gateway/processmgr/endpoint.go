package processmgr

// 内部进程端点：Core 的宿主桥用回环 + 控制令牌把插件注册的长驻进程交给 Gateway
// 的 ProcessManager。
//
// 边界与 dispatch 端点同源：
//   - 只接受回环来源 + Bearer 控制令牌（令牌由网关签发给本 Core 进程，插件看不到）；
//   - 身份（pluginId）由 Core 宿主按能力集盖章后写入请求体，网关把它当 owner；
//   - scope=workspace 的工作区只能取令牌绑定的那个工作区，插件不能注册到别的目录；
//   - entry 必须是绝对路径（Core 宿主已解析到插件目录并拒绝越界），网关再校验可读；
//   - 不提供 stdin/stdout、不提供任意命令行：启动形态只有 node/binary，禁止 shell。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/gateway/dispatch"
)

const (
	// DefaultProcessBodyBytes 是内部进程请求体上限：只有 spec 文本，不需要图片级上限。
	DefaultProcessBodyBytes = 1 << 20
	// DefaultProcessTimeout 是单次内部进程调用的服务端上限（启动可能慢）。
	DefaultProcessTimeout = 90 * time.Second
)

// 内部进程操作名（与 Core 侧 ctx.processes 一一对应）。
const (
	OpRegister   = "register"
	OpUnregister = "unregister"
	OpEnsure     = "ensure"
	OpStop       = "stop"
	OpStatus     = "status"
	OpList       = "list"
)

// ProcessRequest 是内部进程端点的请求体。
//
// 操作名来自 URL 路径（/internal/processes/<op>），不在请求体里重复：
// 请求体只描述「哪个进程、什么形态」，避免两处操作名不一致。
//
// PluginID 由 Core 宿主盖章（不是脚本能传的字段）；网关把它当作 owner。
type ProcessRequest struct {
	PluginID string            `json:"pluginId,omitempty"`
	Name     string            `json:"name,omitempty"`
	Scope    string            `json:"scope,omitempty"`
	Runtime  string            `json:"runtime,omitempty"`
	Entry    string            `json:"entry,omitempty"`
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Start    string            `json:"start,omitempty"`
	Restart  string            `json:"restart,omitempty"`
	// StopTimeoutMs 是停止等待上限；0 表示用管理器默认值。
	StopTimeoutMs int `json:"stopTimeoutMs,omitempty"`
}

// ProcessRef 是回执里的进程引用（owner/name/workspace）。
type ProcessRef struct {
	Owner     string `json:"owner"`
	Name      string `json:"name"`
	Workspace string `json:"workspace,omitempty"`
}

// ProcessResponse 是成功回执。
type ProcessResponse struct {
	Ref      *ProcessRef `json:"ref,omitempty"`
	Status   *Status     `json:"status,omitempty"`
	Statuses []Status    `json:"statuses,omitempty"`
}

// ProcessError 是失败体：调用方按 code 分支，不解析文案。
type ProcessError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EndpointConfig 装配内部进程端点。
type EndpointConfig struct {
	Manager *Manager
	Tokens  *dispatch.Tokens
	// MaxBodyBytes 为 0 时取 DefaultProcessBodyBytes。
	MaxBodyBytes int64
	// Timeout 为 0 时取 DefaultProcessTimeout。
	Timeout time.Duration
	// Log 记录每次内部进程调用；nil 时静默。
	Log func(ident dispatch.Identity, op string, req ProcessRequest, status *Status, err error)
}

// Endpoint 是内部进程 API 的 HTTP 面。
type Endpoint struct {
	manager *Manager
	tokens  *dispatch.Tokens
	maxBody int64
	timeout time.Duration
	log     func(dispatch.Identity, string, ProcessRequest, *Status, error)

	// mu 保护 refs：按令牌键（Core 会话）记录它注册过哪些进程，
	// 供 Core 退出/重启时清理，避免悬挂引用。
	mu   sync.Mutex
	refs map[string][]Ref

	handler http.Handler
}

// NewEndpoint 创建端点。缺件直接失败（INV-2）。
func NewEndpoint(cfg EndpointConfig) (*Endpoint, error) {
	if cfg.Manager == nil {
		return nil, errors.New("processmgr: 内部端点需要 Manager")
	}
	if cfg.Tokens == nil {
		return nil, errors.New("processmgr: 内部端点需要控制令牌表")
	}
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultProcessBodyBytes
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultProcessTimeout
	}
	logf := cfg.Log
	if logf == nil {
		logf = func(dispatch.Identity, string, ProcessRequest, *Status, error) {}
	}
	return &Endpoint{
		manager: cfg.Manager, tokens: cfg.Tokens, maxBody: maxBody, timeout: timeout,
		log: logf, refs: map[string][]Ref{},
	}, nil
}

// Handler 返回内部端点自己的 handler；不套用用户鉴权。
func (e *Endpoint) Handler() http.Handler {
	if e.handler != nil {
		return e.handler
	}
	mux := http.NewServeMux()
	e.Register(mux)
	e.handler = mux
	return e.handler
}

// Register 把内部进程路由挂到 mux 上（必须挂在用户鉴权之外）。
func (e *Endpoint) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/processes/"+OpRegister, e.handle)
	mux.HandleFunc("POST /internal/processes/"+OpUnregister, e.handle)
	mux.HandleFunc("POST /internal/processes/"+OpEnsure, e.handle)
	mux.HandleFunc("POST /internal/processes/"+OpStop, e.handle)
	mux.HandleFunc("GET /internal/processes/"+OpStatus, e.handle)
	mux.HandleFunc("GET /internal/processes/"+OpList, e.handle)
}

// CleanupCore 清理一次 Core 会话注册过的全部进程（Core 退出/重启时调用）。
//
// 按「这个 Core 注册过什么」清理：别的 Core 注册的同名进程不受影响。
func (e *Endpoint) CleanupCore(ident dispatch.Identity) int {
	return e.CleanupKey(tokenKeyOf(ident))
}

// CleanupKey 按令牌签发键清理（网关在给同一工作区重签令牌时用它回收旧 Core 的进程）。
func (e *Endpoint) CleanupKey(key string) int {
	if e == nil || strings.TrimSpace(key) == "" {
		return 0
	}
	e.mu.Lock()
	refs := e.refs[key]
	delete(e.refs, key)
	e.mu.Unlock()

	n := 0
	// 每条记录对应一次成功的 Register：逐条释放，引用计数才对得上。
	for _, ref := range refs {
		if err := e.manager.Unregister(ref); err != nil && CodeOf(err) != CodeNotFound {
			continue
		}
		n++
	}
	return n
}

// CleanupAll 丢弃全部登记索引（网关关闭时用）。
//
// 不清 Manager：Gateway.Shutdown 会调 Manager.Shutdown 回收进程本身。这里只负责
// 让「哪些 Core 注册过什么」这张表不跨实例残留。
func (e *Endpoint) CleanupAll() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	n := len(e.refs)
	e.refs = map[string][]Ref{}
	e.mu.Unlock()
	return n
}

func (e *Endpoint) handle(w http.ResponseWriter, r *http.Request) {
	if !loopbackRequest(r) {
		writeProcessError(w, http.StatusForbidden, "forbidden", "内部端点只接受回环来源")
		return
	}
	ident, ok := e.authenticate(r)
	if !ok {
		writeProcessError(w, http.StatusUnauthorized, "unauthorized", "缺少或无效的控制令牌")
		return
	}
	if !ident.HasScope(dispatch.ScopeProcessControl) {
		// 被管进程拿到的令牌只有发送范围：它不能反向注册/注销进程。
		writeProcessError(w, http.StatusForbidden, "forbidden", "该控制令牌没有被授予进程范围")
		return
	}
	op := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/processes/"), "/")
	var req ProcessRequest
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, e.maxBody)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeProcessError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON 或超过大小上限")
			return
		}
	} else {
		q := r.URL.Query()
		req.PluginID = q.Get("pluginId")
		req.Name = q.Get("name")
		req.Scope = q.Get("scope")
	}

	ctx, cancel := context.WithTimeout(r.Context(), e.timeout)
	defer cancel()
	res, status, err := e.dispatchOp(ctx, ident, op, req)
	e.log(ident, op, req, status, err)
	if err != nil {
		code := CodeOf(err)
		if code == "" {
			writeProcessError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		writeProcessError(w, processStatus(code), string(code), err.Error())
		return
	}
	writeProcessJSON(w, http.StatusOK, res)
}

// dispatchOp 执行一次内部进程操作；status 只在单进程操作时非空。
func (e *Endpoint) dispatchOp(_ context.Context, ident dispatch.Identity, op string,
	req ProcessRequest) (ProcessResponse, *Status, error) {

	switch op {
	case OpRegister:
		ref, err := e.register(ident, req)
		if err != nil {
			return ProcessResponse{}, nil, err
		}
		st, _ := e.manager.Status(ref)
		return ProcessResponse{Ref: processRefOf(ref), Status: &st}, &st, nil
	case OpEnsure:
		ref, err := e.refOf(ident, req)
		if err != nil {
			return ProcessResponse{}, nil, err
		}
		st, err := e.manager.Ensure(ref)
		if err != nil {
			return ProcessResponse{Ref: processRefOf(ref)}, &st, err
		}
		return ProcessResponse{Ref: processRefOf(ref), Status: &st}, &st, nil
	case OpStop:
		ref, err := e.refOf(ident, req)
		if err != nil {
			return ProcessResponse{}, nil, err
		}
		if err := e.manager.Stop(ref); err != nil {
			return ProcessResponse{Ref: processRefOf(ref)}, nil, err
		}
		st, _ := e.manager.Status(ref)
		return ProcessResponse{Ref: processRefOf(ref), Status: &st}, &st, nil
	case OpUnregister:
		ref, err := e.refOf(ident, req)
		if err != nil {
			return ProcessResponse{}, nil, err
		}
		if err := e.manager.Unregister(ref); err != nil {
			return ProcessResponse{}, nil, err
		}
		e.forget(ref)
		return ProcessResponse{Ref: processRefOf(ref)}, nil, nil
	case OpStatus:
		ref, err := e.refOf(ident, req)
		if err != nil {
			return ProcessResponse{}, nil, err
		}
		st, ok := e.manager.Status(ref)
		if !ok {
			return ProcessResponse{}, nil, newError(CodeNotFound, "没有找到进程 %s", ref.ID())
		}
		return ProcessResponse{Ref: processRefOf(ref), Status: &st}, &st, nil
	case OpList:
		pluginID := strings.TrimSpace(req.PluginID)
		if !pluginapi.ValidID(pluginID) {
			return ProcessResponse{}, nil, newError(CodeInvalidSpec, "list 需要合法的 pluginId")
		}
		out := make([]Status, 0, 8)
		for _, st := range e.manager.List() {
			if st.Owner == pluginID {
				out = append(out, st)
			}
		}
		return ProcessResponse{Statuses: out}, nil, nil
	default:
		return ProcessResponse{}, nil, newError(CodeInvalidSpec, "未知的进程操作 %q", op)
	}
}

// register 校验并登记一条进程。
//
// 「重复注册」只有一种合法情形：同名同形态（Register 内部只加引用且返回 nil）。
// 同名但形态不同（换了 entry/runtime/scope）必须原样报 process_exists ——
// 静默吞掉会让插件改完 entry 后以为生效，实际还在跑旧脚本。
func (e *Endpoint) register(ident dispatch.Identity, req ProcessRequest) (Ref, error) {
	pluginID, err := ownerOf(req)
	if err != nil {
		return Ref{}, err
	}
	spec, err := specOf(ident, req)
	if err != nil {
		return Ref{}, err
	}
	ref, regErr := e.manager.Register(pluginID, spec)
	if regErr != nil {
		return ref, regErr
	}
	e.track(ident, ref)
	return ref, nil
}

// specOf 把请求转成受约束的 ProcessSpec；owner 由 register 盖章，这里不写。
func specOf(ident dispatch.Identity, req ProcessRequest) (ProcessSpec, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return ProcessSpec{}, newError(CodeInvalidSpec, "register 需要 name")
	}
	scope := Scope(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = ScopeGlobal
	}
	switch scope {
	case ScopeGlobal, ScopeWorkspace:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "scope 只能是 global|workspace")
	}
	workspace := ""
	if scope == ScopeWorkspace {
		workspace = strings.TrimSpace(ident.Workspace)
		if workspace == "" {
			return ProcessSpec{}, newError(CodeInvalidSpec, "workspace scope 需要令牌绑定工作区")
		}
	}

	entry := strings.TrimSpace(req.Entry)
	if entry == "" || !filepath.IsAbs(entry) {
		// Core 宿主负责把插件内相对路径解析成绝对路径并拒绝越界；
		// 网关只接受解析后的绝对路径，避免自己再实现一遍目录规则。
		return ProcessSpec{}, newError(CodeInvalidSpec, "entry 必须是宿主解析后的绝对路径")
	}
	info, statErr := os.Stat(entry)
	if statErr != nil || info.IsDir() {
		return ProcessSpec{}, newError(CodeInvalidSpec, "entry %q 不可读或不是文件", entry)
	}

	rt := Runtime(strings.TrimSpace(req.Runtime))
	if rt == "" {
		rt = RuntimeBinary
	}
	switch rt {
	case RuntimeBinary, RuntimeNode:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "runtime 只能是 node|binary")
	}

	start := StartMode(strings.TrimSpace(req.Start))
	if start == "" {
		start = StartLazy
	}
	switch start {
	case StartLazy, StartOnEnable:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "start 只能是 on_enable|lazy")
	}
	restart := RestartPolicy(strings.TrimSpace(req.Restart))
	if restart == "" {
		restart = RestartNo
	}
	switch restart {
	case RestartNo, RestartOnFailure, RestartAlways:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "restart 只能是 no|on_failure|always")
	}
	var stopTimeout time.Duration
	if req.StopTimeoutMs > 0 {
		if req.StopTimeoutMs > 60000 {
			return ProcessSpec{}, newError(CodeInvalidSpec, "stopTimeoutMs 上限 60000")
		}
		stopTimeout = time.Duration(req.StopTimeoutMs) * time.Millisecond
	}
	if len(req.Args) > 32 {
		return ProcessSpec{}, newError(CodeInvalidSpec, "args 最多 32 项")
	}
	if len(req.Env) > 32 {
		return ProcessSpec{}, newError(CodeInvalidSpec, "env 最多 32 项")
	}

	return ProcessSpec{
		Name: name, Scope: scope, Workspace: workspace,
		Runtime: rt, Entry: entry, Command: strings.TrimSpace(req.Command),
		Args: append([]string(nil), req.Args...), Env: copyEnv(req.Env),
		StartMode: start, Restart: restart, StopTimeout: stopTimeout,
	}, nil
}

// ownerOf 取请求里的插件 id（由 Core 宿主盖章）并校验形状。
func ownerOf(req ProcessRequest) (string, error) {
	pluginID := strings.TrimSpace(req.PluginID)
	if !pluginapi.ValidID(pluginID) {
		return "", newError(CodeInvalidSpec, "pluginId %q 非法（由 Core 宿主盖章）", pluginID)
	}
	return pluginID, nil
}

// refOf 解析请求对应的引用：global 进程的 workspace 恒为空，
// workspace 进程用令牌绑定的工作区（插件不能指定别人的目录）。
func (e *Endpoint) refOf(ident dispatch.Identity, req ProcessRequest) (Ref, error) {
	pluginID, err := ownerOf(req)
	if err != nil {
		return Ref{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Ref{}, newError(CodeInvalidSpec, "需要 name")
	}
	ref := Ref{Owner: pluginID, Name: name}
	if strings.TrimSpace(req.Scope) == string(ScopeWorkspace) && strings.TrimSpace(ident.Workspace) != "" {
		ref.Workspace = strings.TrimSpace(ident.Workspace)
	}
	return ref, nil
}

// track 记住本次 Core 注册过的一次引用，供 CleanupCore 使用。
//
// 每次成功注册都要记一条（不去重）：Register 每调用一次就加一个引用，清理时
// 也必须按同样的次数释放，否则同一个 Core 注册两次、清理一次，就会永久漏掉
// 一个引用，进程再也停不下来。
func (e *Endpoint) track(ident dispatch.Identity, ref Ref) {
	key := tokenKeyOf(ident)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refs[key] = append(e.refs[key], ref)
}

// forget 在插件主动注销后清掉一条记录（与一次 Unregister 对应）。
//
// 只删第一条匹配项：同一个 key 可能被记录多次（重复注册各加一个引用），
// 每注销一次就少一条，与 Manager 的引用计数保持同步。
func (e *Endpoint) forget(ref Ref) {
	entryKey := refKey(ref)
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, list := range e.refs {
		for i, existing := range list {
			if refKey(existing) != entryKey {
				continue
			}
			e.refs[key] = append(list[:i], list[i+1:]...)
			if len(e.refs[key]) == 0 {
				delete(e.refs, key)
			}
			return
		}
	}
}

// tokenKeyOf 用令牌身份做会话键；缺 Key 时退回工作区 + CoreID。
func tokenKeyOf(ident dispatch.Identity) string {
	if key := strings.TrimSpace(ident.Key); key != "" {
		return key
	}
	return strings.TrimSpace(ident.Workspace) + "\x1f" + strings.TrimSpace(ident.CoreID)
}

// authenticate 取 Bearer 令牌并查表。
func (e *Endpoint) authenticate(r *http.Request) (dispatch.Identity, bool) {
	token := strings.TrimSpace(r.Header.Get(dispatch.ControlTokenHeader))
	if token == "" {
		return dispatch.Identity{}, false
	}
	if after, ok := strings.CutPrefix(token, "Bearer "); ok {
		token = strings.TrimSpace(after)
	}
	return e.tokens.Lookup(token)
}

// loopbackRequest 只认回环来源（与 dispatch 端点的判据一致）。
func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func processRefOf(ref Ref) *ProcessRef {
	return &ProcessRef{Owner: ref.Owner, Name: ref.Name, Workspace: ref.Workspace}
}

// processStatus 把进程业务码映射成 HTTP 状态。
func processStatus(code ErrorCode) int {
	switch code {
	case CodeInvalidSpec:
		return http.StatusBadRequest
	case CodeNotFound, CodeExists, CodeLimit, CodeStartFailed, CodeStopFailed:
		return http.StatusConflict
	default:
		return http.StatusBadGateway
	}
}

func writeProcessError(w http.ResponseWriter, status int, code, message string) {
	writeProcessJSON(w, status, map[string]any{"error": ProcessError{Code: code, Message: message}})
}

func writeProcessJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func copyEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// 编译期确保 fmt 仍被使用（错误文案由 newError 组装）。
var _ = fmt.Sprintf
