package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
)

// frontendGate 提供「某工作区里某前端插件是否启用」的短期缓存。
// 渲染桥每个插槽贡献都会问一次；缓存避免一次页面渲染打十几次 plugin.list。
type frontendGate struct {
	sup          contracts.Supervisor
	workspaceFor func(*http.Request) string
	mu           sync.Mutex
	byWorkspace  map[string]frontendEnabledEntry
}

type frontendEnabledEntry struct {
	at       time.Time
	set      map[string]bool
	known    map[string]bool
	allowAll bool // Core 暂时不可用：按启用渲染，避免整页插件区消失
}

const frontendEnabledTTL = 2 * time.Second

func newFrontendGate(sup contracts.Supervisor, workspaceFor func(*http.Request) string) *frontendGate {
	return &frontendGate{sup: sup, workspaceFor: workspaceFor, byWorkspace: map[string]frontendEnabledEntry{}}
}

// Enabled 实现 frontend.BridgeOptions.Enabled。
func (g *frontendGate) Enabled(r *http.Request, pluginID string) bool {
	workspace := ""
	if g.workspaceFor != nil {
		workspace = g.workspaceFor(r)
	}
	entry, err := g.snapshot(workspace)
	if err != nil {
		return true
	}
	return entry.enabledIn(pluginID)
}

// enabledIn 是启用判定的纯逻辑（便于单测）：
//   - Core 明确列出的插件按启用状态；
//   - 未出现在 plugin.list 里的插件视为「Core 不加载的前端-only 插件」
//     （只贡献 Markdown 动态块的第三方包），安装即启用；要停用就移除目录；
//   - Core 不可用时按启用渲染，避免整页插件区消失。
func (e frontendEnabledEntry) enabledIn(pluginID string) bool {
	if e.allowAll {
		return true
	}
	if e.set[pluginID] {
		return true
	}
	if e.known[pluginID] {
		return false
	}
	return true
}

// Invalidate 丢弃一个工作区的缓存；workspace 为空时清空全部。
func (g *frontendGate) Invalidate(workspace string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if workspace == "" {
		g.byWorkspace = map[string]frontendEnabledEntry{}
		return
	}
	delete(g.byWorkspace, workspace)
}

func (g *frontendGate) snapshot(workspace string) (frontendEnabledEntry, error) {
	g.mu.Lock()
	if e, ok := g.byWorkspace[workspace]; ok && time.Since(e.at) < frontendEnabledTTL {
		g.mu.Unlock()
		return e, nil
	}
	g.mu.Unlock()

	// 每个工作区最多每 2s 查一次；失败也缓存 allowAll，避免 Core 故障时
	// 每个插槽都等满超时。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entry := frontendEnabledEntry{at: time.Now()}
	core, err := g.sup.Acquire(ctx, workspace)
	if err != nil {
		entry.allowAll = true
	} else {
		raw, callErr := core.Call(ctx, "plugin.list", map[string]any{})
		g.sup.Release(workspace)
		if callErr != nil {
			entry.allowAll = true
		} else {
			var res struct {
				Plugins []struct {
					ID      string `json:"id"`
					Enabled bool   `json:"enabled"`
					Status  string `json:"status"`
				} `json:"plugins"`
			}
			if jsonErr := json.Unmarshal(raw, &res); jsonErr != nil {
				entry.allowAll = true
			} else {
				entry.set = make(map[string]bool, len(res.Plugins))
				entry.known = make(map[string]bool, len(res.Plugins))
				for _, p := range res.Plugins {
					if p.ID == "" {
						continue
					}
					entry.known[p.ID] = true
					if !p.Enabled {
						continue
					}
					switch p.Status {
					case "", "ready", "degraded":
						entry.set[p.ID] = true
					}
				}
			}
		}
	}
	g.mu.Lock()
	g.byWorkspace[workspace] = entry
	g.mu.Unlock()
	return entry, err
}
