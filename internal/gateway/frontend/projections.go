package frontend

// 本文件是前端插件的只读数据投影（7.5 的 props 扩展）：
//   - providers：Core provider.list + model.list（不含密钥）；
//   - usage：Core usage.get（工作区聚合 + 当前线程聚合）；
//   - interactions：当前线程的待答交互（thread.get）。
//
// 统一由 frontend.capabilities 里的 backend.read 约束（与只读动作预取同一个能力位）。
// 这里只读、不改 Core 状态；写操作仍走 base 路由或插件动作。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/adag/ada-go/internal/plugins"
)

type providersProjection struct {
	Providers []providerProjection `json:"providers"`
	Current   modelRefProjection   `json:"current"`
}

type providerProjection struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	BaseURL string            `json:"baseUrl,omitempty"`
	Active  bool              `json:"active"`
	Models  []modelProjection `json:"models"`
}

type modelProjection struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	ContextWindow    int    `json:"contextWindow"`
	MaxOutput        int    `json:"maxOutput,omitempty"`
	SupportsTools    bool   `json:"supportsTools"`
	SupportsThinking bool   `json:"supportsThinking,omitempty"`
}

type modelRefProjection struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

type usageProjection struct {
	Thread    usageTotals `json:"thread"`
	Workspace usageTotals `json:"workspace"`
}

type usageTotals struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
	CacheTokens  int    `json:"cacheTokens"`
	CostMicros   int64  `json:"costMicros"`
}

type interactionProjection struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	Options  []string `json:"options,omitempty"`
	Status   string   `json:"status"`
}

func hasCapability(caps []plugins.Capability, want plugins.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// callCore 在请求预算内取一次 Core 客户端并调用一个只读方法。
func (b *Bridge) callCore(r *http.Request, method string, params any) (json.RawMessage, error) {
	if b.sup == nil {
		return nil, fmt.Errorf("frontend: 装配没有注入 Supervisor")
	}
	ctx, cancel := context.WithTimeout(r.Context(), BackendActionTimeout)
	defer cancel()
	workspace := b.workspace(r)
	core, err := b.sup.Acquire(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer b.sup.Release(workspace)
	return core.Call(ctx, method, params)
}

func (b *Bridge) loadProviders(r *http.Request) (*providersProjection, error) {
	raw, err := b.callCore(r, "provider.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var listed struct {
		Providers []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			BaseURL     string `json:"baseUrl"`
			Active      bool   `json:"active"`
			Models      []struct {
				ID               string `json:"id"`
				DisplayName      string `json:"displayName"`
				ContextWindow    int    `json:"contextWindow"`
				MaxOutput        int    `json:"maxOutput"`
				SupportsTools    bool   `json:"supportsTools"`
				SupportsThinking bool   `json:"supportsThinking"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, err
	}
	out := &providersProjection{Providers: make([]providerProjection, 0, len(listed.Providers))}
	for _, p := range listed.Providers {
		row := providerProjection{ID: p.ID, Name: p.DisplayName, BaseURL: p.BaseURL, Active: p.Active}
		if strings.TrimSpace(row.Name) == "" {
			row.Name = p.ID
		}
		for _, m := range p.Models {
			name := m.DisplayName
			if strings.TrimSpace(name) == "" {
				name = m.ID
			}
			row.Models = append(row.Models, modelProjection{
				ID: m.ID, Name: name, ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
				SupportsTools: m.SupportsTools, SupportsThinking: m.SupportsThinking,
			})
		}
		out.Providers = append(out.Providers, row)
	}

	// 当前模型（工作区级）：model.list 的 current 字段。
	if rawModel, err := b.callCore(r, "model.list", map[string]any{}); err == nil {
		var current struct {
			Current modelRefProjection `json:"current"`
		}
		if json.Unmarshal(rawModel, &current) == nil {
			out.Current = current.Current
		}
	}
	return out, nil
}

func (b *Bridge) loadUsage(r *http.Request, threadID string) (*usageProjection, error) {
	params := map[string]any{}
	if threadID != "" {
		params["threadId"] = threadID
	}
	raw, err := b.callCore(r, "usage.get", params)
	if err != nil {
		return nil, err
	}
	var res struct {
		Thread    usageTotals `json:"thread"`
		Workspace usageTotals `json:"workspace"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &usageProjection{Thread: res.Thread, Workspace: res.Workspace}, nil
}

func (b *Bridge) loadInteractions(r *http.Request, threadID string) ([]interactionProjection, error) {
	raw, err := b.callCore(r, "thread.get", map[string]any{"threadId": threadID, "itemLimit": 1})
	if err != nil {
		return nil, err
	}
	var res struct {
		Interactions []interactionProjection `json:"interactions"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	out := make([]interactionProjection, 0, len(res.Interactions))
	for _, item := range res.Interactions {
		if item.Status != "" && item.Status != "pending" {
			continue
		}
		out = append(out, item)
		if len(out) >= 10 {
			break
		}
	}
	return out, nil
}
