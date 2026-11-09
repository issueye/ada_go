package gateway

// Gateway 侧的控制面：给每个 Core 签发一次性内部发送凭据。
//
// 地址在监听之后才知道（cmd 先 New 后 Listen），所以这里用可设置的原子值：
// Core 是懒加载的，第一次 Acquire 一定发生在 Serve 之后，地址那时已经就位。
// 若地址为空（例如测试注入 Supervisor），则不签发——Core 侧 dispatch.send
// 就不会安装，插件拿不到 ctx.dispatch。

import (
	"sync"

	"github.com/adag/ada-go/internal/gateway/dispatch"
	"github.com/adag/ada-go/internal/gateway/supervisor"
)

// controlPlane 实现 supervisor.ControlIssuer 与 processmgr.ControlIssuer。
type controlPlane struct {
	tokens *dispatch.Tokens

	mu  sync.RWMutex
	url string
	// processTokens 是「被管进程的签发键 → 令牌」，停止进程时按它回收。
	processTokens map[string]string
	// onIssue 在给同一签发键重签令牌时回调（键 = 工作区）。
	//
	// 重签意味着旧 Core 已退出/被替换：它注册过的被管进程必须先回收，
	// 否则新 Core 会拿到一份「引用计数已被旧代占住」的注册表。
	onIssue func(key string)
}

var _ supervisor.ControlIssuer = (*controlPlane)(nil)

func newControlPlane(tokens *dispatch.Tokens) *controlPlane {
	return &controlPlane{tokens: tokens}
}

// SetURL 设置控制面根地址（形如 http://127.0.0.1:52353）。
func (c *controlPlane) SetURL(url string) {
	c.mu.Lock()
	c.url = url
	c.mu.Unlock()
}

// URL 返回控制面根地址（未设置时为空）。
func (c *controlPlane) URL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.url
}

// ControlAccess 给一个工作区的 Core 签发凭据。每次拉起重新签发，旧令牌作废。
func (c *controlPlane) ControlAccess(workspace string) (string, string) {
	url := c.URL()
	if url == "" {
		return "", ""
	}
	// 重签前清理旧 Core 的进程注册：旧 Core 已退出/被替换，它拉起的进程
	// 不该继续挂在引用计数上（否则新 Core 的注册只会加引用、永远不回收）。
	if c.onIssue != nil {
		c.onIssue(workspace)
	}
	token, err := c.tokens.IssueFor(workspace, dispatch.Identity{
		Workspace: workspace, CoreID: workspace, Key: workspace,
		// Core 的宿主桥既要发消息（ctx.dispatch）也要管进程（ctx.processes）。
		Scopes: []string{dispatch.ScopeDispatchSend, dispatch.ScopeProcessControl},
	})
	if err != nil {
		return "", ""
	}
	return url, token
}

// IssueForProcess 给网关注册的被管进程签发发送凭据（processmgr.ControlIssuer）。
//
// 令牌只带发送范围：scheduler 能投递消息，但不能反向注册/注销进程。
// 身份里写死 PluginID，sender 因此由令牌盖章，进程改不了。
func (c *controlPlane) IssueForProcess(owner, name, workspace string) (string, string) {
	url := c.URL()
	if url == "" || owner == "" || name == "" {
		return "", ""
	}
	key := processControlKey(owner, name, workspace)
	token, err := c.tokens.IssueFor(key, dispatch.Identity{
		Workspace: workspace,
		CoreID:    key,
		Key:       key,
		Scopes:    []string{dispatch.ScopeDispatchSend},
		PluginID:  owner,
	})
	if err != nil {
		return "", ""
	}
	c.mu.Lock()
	if c.processTokens == nil {
		c.processTokens = map[string]string{}
	}
	c.processTokens[key] = token
	c.mu.Unlock()
	return url, token
}

// RevokeProcess 回收一个被管进程的令牌（进程停止时调用）。
func (c *controlPlane) RevokeProcess(owner, name, workspace string) {
	if c == nil {
		return
	}
	key := processControlKey(owner, name, workspace)
	c.mu.Lock()
	token := c.processTokens[key]
	delete(c.processTokens, key)
	c.mu.Unlock()
	if token != "" {
		c.tokens.Revoke(token)
	}
}

// processControlKey 是进程令牌的签发键：同一 owner/name/workspace 重签即作废旧令牌。
func processControlKey(owner, name, workspace string) string {
	return "proc:" + owner + "/" + name + "/" + workspace
}

// revokeControls 清空全部控制令牌（Gateway 关闭时调用）。
func (c *controlPlane) revokeControls() {
	c.mu.Lock()
	c.url = ""
	c.processTokens = nil
	c.mu.Unlock()
}
