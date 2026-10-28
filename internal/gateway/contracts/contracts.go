// Package contracts 是 M2 三块并行开发的共享边界：
// Core 生命周期（supervisor）、RPC 客户端/relay（relay）、SSR（web）。
//
// 这里只放接口与最小 DTO，不放实现：三块可以独立编译、独立测试，
// 最后由组合根（cmd/adag）把真实实现接起来。
package contracts

import (
	"context"
	"encoding/json"
)

// Event 是一条来自 Core 的 JSON-RPC 通知（evt.*）。
type Event struct {
	Method string
	Params json.RawMessage
}

// CoreClient 是 Gateway 侧对一个 Core 的 RPC 句柄。
//
// 语义：
//   - Call 按 JSON-RPC 方法调用，params 由调用方编码；返回 result 原文；
//   - Subscribe 返回事件通道与取消函数；通道关闭表示连接结束；
//   - Close 幂等，关闭后 Call 返回错误。
type CoreClient interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Subscribe(buffer int) (<-chan Event, func())
	Close() error
}

// DialFunc 建立一个到 Core 的客户端（测试注入假实现）。
type DialFunc func(ctx context.Context, endpoint, token string) (CoreClient, error)

// WorkspaceStatus 是 supervisor 对一个工作区的可观测状态。
type WorkspaceStatus struct {
	Workspace string `json:"workspace"`
	Product   string `json:"product"`
	Endpoint  string `json:"endpoint"`
	PID       int    `json:"pid"`
	Ready     bool   `json:"ready"`
	LastError string `json:"lastError,omitempty"`
}

// Supervisor 是工作区 → Core 客户端的管理面。
//
// 实现负责 spawn/握手/重启/回收；调用方只按工作区取客户端。
// Release 表示一次请求结束（实现可据此做空闲回收），不关闭在途轮次。
type Supervisor interface {
	Acquire(ctx context.Context, workspace string) (CoreClient, error)
	Release(workspace string)
	Shutdown(ctx context.Context) error
	List() []WorkspaceStatus
}
