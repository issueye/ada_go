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

// controlPlane 实现 supervisor.ControlIssuer。
type controlPlane struct {
	tokens *dispatch.Tokens

	mu  sync.RWMutex
	url string
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
	token, err := c.tokens.IssueFor(workspace, dispatch.Identity{
		Workspace: workspace, CoreID: workspace, Key: workspace,
	})
	if err != nil {
		return "", ""
	}
	return url, token
}

// revokeControls 清空全部控制令牌（Gateway 关闭时调用）。
func (c *controlPlane) revokeControls() {
	c.mu.Lock()
	c.url = ""
	c.mu.Unlock()
}
