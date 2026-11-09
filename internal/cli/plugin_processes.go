package cli

// 进程监督桥（capability: process.supervise，仅 builtin）的 Core 侧实现。
//
// 网关拉起 Core 时经环境变量交出控制面地址与一次性令牌（与 dispatch 桥同一组）。
// 这里用它调网关的 /internal/processes/*：
//
//   - 令牌只在本进程里，插件脚本拿不到；
//   - owner（pluginId）由宿主按能力集盖章，脚本传什么都不采信；
//   - entry 由宿主在这里解析到插件目录内并拒绝越界，网关再校验一次；
//   - 进程由网关拉起，不在 Core 进程树里，因此跨会话存活、随 Core 重签令牌回收。
//
// 没有注入控制面（CLI 直连、无网关部署）时 ctx.processes 不存在。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/plugins/host"
)

const (
	processesTimeout    = 90 * time.Second
	processesRespBytes  = 1 << 20
	processesErrorBytes = 4 << 10
)

// gatewayProcesses 是 ctx.processes 的宿主实现。
type gatewayProcesses struct {
	baseURL   string
	token     string
	workspace string
	client    *http.Client
}

// newGatewayProcesses 读取控制面环境变量；未注入时返回 nil（能力不安装）。
func newGatewayProcesses(workspace string) *gatewayProcesses {
	url := strings.TrimSpace(os.Getenv(gatewayControlURLEnv))
	token := strings.TrimSpace(os.Getenv(gatewayControlTokenEnv))
	if url == "" || token == "" {
		return nil
	}
	return &gatewayProcesses{
		baseURL:   strings.TrimRight(url, "/") + "/internal/processes/",
		token:     token,
		workspace: workspace,
		client:    &http.Client{Timeout: processesTimeout},
	}
}

// processBody 与网关侧 processmgr.ProcessRequest 同形。
type processBody struct {
	PluginID      string            `json:"pluginId,omitempty"`
	Name          string            `json:"name,omitempty"`
	Scope         string            `json:"scope,omitempty"`
	Runtime       string            `json:"runtime,omitempty"`
	Entry         string            `json:"entry,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Start         string            `json:"start,omitempty"`
	Restart       string            `json:"restart,omitempty"`
	StopTimeoutMs int               `json:"stopTimeoutMs,omitempty"`
}

// processReply 与网关侧 processmgr.ProcessResponse 同形。
type processReply struct {
	Ref *struct {
		Owner     string `json:"owner"`
		Name      string `json:"name"`
		Workspace string `json:"workspace"`
	} `json:"ref"`
	Status   *processStatusBody  `json:"status"`
	Statuses []processStatusBody `json:"statuses"`
}

type processStatusBody struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Workspace string `json:"workspace"`
	Runtime   string `json:"runtime"`
	PID       int    `json:"pid"`
	Running   bool   `json:"running"`
	Restarts  int    `json:"restarts"`
	Refs      int    `json:"refs"`
	StartMode string `json:"startMode"`
	Restart   string `json:"restart"`
	LastError string `json:"lastError"`
	StartedAt string `json:"startedAt"`
}

func (s processStatusBody) toHost() host.ProcessStatus {
	return host.ProcessStatus{
		ID: s.ID, Owner: s.Owner, Name: s.Name, Scope: s.Scope, Workspace: s.Workspace,
		Runtime: s.Runtime, PID: s.PID, Running: s.Running, Restarts: s.Restarts,
		Refs: s.Refs, StartMode: s.StartMode, Restart: s.Restart, LastError: s.LastError,
		StartedAt: s.StartedAt,
	}
}

// call 发一次内部进程请求；pluginID 由调用方（宿主安装处）盖章。
func (g *gatewayProcesses) call(ctx context.Context, op, pluginID string, body *processBody) (processReply, error) {
	if g == nil {
		return processReply{}, errors.New("cli: 未接入网关进程管理")
	}
	if body == nil {
		body = &processBody{}
	}
	body.PluginID = pluginID
	raw, err := json.Marshal(body)
	if err != nil {
		return processReply{}, fmt.Errorf("cli: 进程请求无法序列化: %w", err)
	}
	url := g.baseURL + op
	method := http.MethodPost
	if op == "status" || op == "list" {
		// 查询类操作走 GET：没有请求体，参数在 query 上。
		method = http.MethodGet
		url += "?pluginId=" + urlQueryEscape(pluginID)
		if op == "status" {
			url += "&name=" + urlQueryEscape(body.Name) + "&scope=" + urlQueryEscape(body.Scope)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var reader io.Reader
	if method == http.MethodPost {
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return processReply{}, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+g.token)

	resp, err := g.client.Do(req)
	if err != nil {
		return processReply{}, fmt.Errorf("cli: 调用网关进程管理失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, processesErrorBytes))
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(msg, &e) == nil && e.Error.Code != "" {
			return processReply{}, fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
		}
		return processReply{}, fmt.Errorf("cli: 网关返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out processReply
	dec := json.NewDecoder(io.LimitReader(resp.Body, processesRespBytes))
	if err := dec.Decode(&out); err != nil {
		return processReply{}, fmt.Errorf("cli: 进程回执不是合法 JSON: %w", err)
	}
	return out, nil
}

// processSupervisor 把网关端点适配成 host.ProcessSupervisor。
//
// 它自己不记住任何 pluginID：owner 由每次调用传入（宿主按能力集盖章），
// 因为同一个 Session 会装配多个插件，实现记住「某一次装配的 id」必然出错。
type processSupervisor struct {
	client *gatewayProcesses
}

func (p *processSupervisor) Register(ctx context.Context, pluginID string, spec host.ProcessSpec) (host.ProcessStatus, error) {
	reply, err := p.client.call(ctx, "register", pluginID, &processBody{
		Name: spec.Name, Scope: spec.Scope, Runtime: spec.Runtime, Entry: spec.Entry,
		Args: spec.Args, Env: spec.Env, Start: spec.Start, Restart: spec.Restart,
		StopTimeoutMs: spec.StopTimeoutMs,
	})
	if err != nil {
		return host.ProcessStatus{}, err
	}
	if reply.Status == nil {
		return host.ProcessStatus{}, errors.New("cli: 注册回执缺少状态")
	}
	return reply.Status.toHost(), nil
}

func (p *processSupervisor) Ensure(ctx context.Context, pluginID, name, scope string) (host.ProcessStatus, error) {
	reply, err := p.client.call(ctx, "ensure", pluginID, &processBody{Name: name, Scope: scope})
	if err != nil {
		return host.ProcessStatus{}, err
	}
	if reply.Status == nil {
		return host.ProcessStatus{}, errors.New("cli: 回执缺少状态")
	}
	return reply.Status.toHost(), nil
}

func (p *processSupervisor) Stop(ctx context.Context, pluginID, name, scope string) (host.ProcessStatus, error) {
	reply, err := p.client.call(ctx, "stop", pluginID, &processBody{Name: name, Scope: scope})
	if err != nil {
		return host.ProcessStatus{}, err
	}
	if reply.Status == nil {
		return host.ProcessStatus{}, errors.New("cli: 回执缺少状态")
	}
	return reply.Status.toHost(), nil
}

func (p *processSupervisor) Status(ctx context.Context, pluginID, name, scope string) (host.ProcessStatus, bool) {
	reply, err := p.client.call(ctx, "status", pluginID, &processBody{Name: name, Scope: scope})
	if err != nil || reply.Status == nil {
		return host.ProcessStatus{}, false
	}
	return reply.Status.toHost(), true
}

func (p *processSupervisor) Unregister(ctx context.Context, pluginID, name, scope string) error {
	_, err := p.client.call(ctx, "unregister", pluginID, &processBody{Name: name, Scope: scope})
	return err
}

func (p *processSupervisor) List(ctx context.Context, pluginID string) ([]host.ProcessStatus, error) {
	reply, err := p.client.call(ctx, "list", pluginID, nil)
	if err != nil {
		return nil, err
	}
	out := make([]host.ProcessStatus, 0, len(reply.Statuses))
	for _, st := range reply.Statuses {
		out = append(out, st.toHost())
	}
	return out, nil
}

// wireProcesses 把进程监督桥接进宿主上下文（能力未授予或未注入控制面时不安装）。
func (s *Session) wireProcesses(hc *host.Context) {
	if hc == nil {
		return
	}
	client := newGatewayProcesses(s.cfg.Workspace)
	if client == nil {
		return
	}
	s.processesClient = client
	hc.Processes = &processSupervisor{client: client}
}

// urlQueryEscape 是 GET 查询参数的转义（不引入额外依赖）。
func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "%", "%25"), " ", "%20")
}
