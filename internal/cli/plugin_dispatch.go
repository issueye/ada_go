package cli

// 内部发送桥（capability: dispatch.send，仅 builtin）的 Core 侧实现。
//
// 网关拉起 Core 时经环境变量交出控制面地址与一次性令牌（ADAGO_GATEWAY_CONTROL_URL /
// ADAGO_GATEWAY_CONTROL_TOKEN）。这里用它们调网关的 /internal/dispatch/send：
//
//   - 令牌只在本进程里，插件脚本拿不到（ctx.dispatch 只有 send 一个成员）；
//   - 身份（pluginId/taskId）由这里按能力集盖章，脚本传的任何身份字段都不采信；
//   - 投递语义（建线程 / 空闲 start / 运行中 steer / busy）由网关调度层决定，
//     Core 不重复实现一份。
//
// 没有注入控制面（CLI 直连、无网关部署）时 ctx.dispatch 不存在，而不是给个假实现。

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
	// 环境变量名与网关签发侧同源（supervisor.SpawnSpec 写入）。
	gatewayControlURLEnv   = "ADAGO_GATEWAY_CONTROL_URL"
	gatewayControlTokenEnv = "ADAGO_GATEWAY_CONTROL_TOKEN"

	dispatchTimeout    = 60 * time.Second
	dispatchRespBytes  = 1 << 20
	dispatchErrorBytes = 4 << 10
)

// gatewayDispatch 是 ctx.dispatch 的宿主实现。
type gatewayDispatch struct {
	url        string
	token      string
	workspace  string
	httpClient *http.Client
}

// newGatewayDispatch 读取控制面环境变量；未注入时返回 nil（能力不安装）。
func newGatewayDispatch(workspace string) *gatewayDispatch {
	url := strings.TrimSpace(os.Getenv(gatewayControlURLEnv))
	token := strings.TrimSpace(os.Getenv(gatewayControlTokenEnv))
	if url == "" || token == "" {
		return nil
	}
	return &gatewayDispatch{
		url:   strings.TrimRight(url, "/") + "/internal/dispatch/send",
		token: token, workspace: workspace,
		httpClient: &http.Client{Timeout: dispatchTimeout},
	}
}

// dispatchBody 是控制端点的请求体（形状与网关侧 ControlRequest 一致）。
type dispatchBody struct {
	Workspace      string `json:"workspace,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	Title          string `json:"title,omitempty"`
	Text           string `json:"text,omitempty"`
	Mode           string `json:"mode,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	PluginID       string `json:"pluginId,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
}

// send 投递一条消息。pluginID 由调用方（宿主 ctx 安装处）按能力集盖章。
func (g *gatewayDispatch) send(ctx context.Context, pluginID string, req host.DispatchRequest) (host.DispatchResult, error) {
	if g == nil {
		return host.DispatchResult{}, errors.New("cli: 未接入网关调度层")
	}
	workspace := strings.TrimSpace(req.Workspace)
	if workspace == "" {
		// 没有显式工作区时落到本 Core 自己的工作区：插件通常只关心「本工作区」。
		workspace = g.workspace
	}
	body := dispatchBody{
		Workspace: workspace, ThreadID: req.ThreadID, Title: req.Title, Text: req.Text,
		Mode: req.Mode, IdempotencyKey: req.IdempotencyKey,
		PluginID: pluginID, TaskID: req.TaskID,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return host.DispatchResult{}, fmt.Errorf("cli: 投递请求无法序列化: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(raw))
	if err != nil {
		return host.DispatchResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.token)

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return host.DispatchResult{}, fmt.Errorf("cli: 投递到网关失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, dispatchErrorBytes))
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(msg, &e) == nil && e.Error.Code != "" {
			// 上游业务码原样上浮：调用方（如 cron）要按 thread_busy 记录运行历史。
			return host.DispatchResult{}, fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
		}
		return host.DispatchResult{}, fmt.Errorf("cli: 网关返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Receipt struct {
			Workspace string `json:"workspace"`
			ThreadID  string `json:"threadId"`
			RunID     string `json:"runId"`
			Mode      string `json:"mode"`
		} `json:"receipt"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, dispatchRespBytes))
	if err := dec.Decode(&out); err != nil {
		return host.DispatchResult{}, fmt.Errorf("cli: 投递回执不是合法 JSON: %w", err)
	}
	return host.DispatchResult{
		Workspace: out.Receipt.Workspace, ThreadID: out.Receipt.ThreadID,
		RunID: out.Receipt.RunID, Mode: out.Receipt.Mode,
	}, nil
}

// wireDispatch 把 dispatch 桥接进宿主上下文（能力未授予或未注入控制面时不安装）。
func (s *Session) wireDispatch(hc *host.Context) {
	if hc == nil {
		return
	}
	client := newGatewayDispatch(s.cfg.Workspace)
	if client == nil {
		return
	}
	s.dispatchClient = client
	hc.Dispatch = func(ctx context.Context, req host.DispatchRequest) (host.DispatchResult, error) {
		// 身份按当前调用上下文盖章：CapabilitySet 已经决定了谁能拿到 ctx.dispatch，
		// 这里只用插件 id 记录归属，脚本改不了。
		return client.send(ctx, hc.PluginID, req)
	}
}
