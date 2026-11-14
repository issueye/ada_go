package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHTTPHandler 把同一套工具挂成 Streamable HTTP 端点（供 serve 模式的
// <BaseURL>/mcp 使用，Agent 不必再 spawn stdio 子进程）。
//
// 无状态：每个请求开一个临时会话，工具状态全在 Deps（Store/TaskQueue）里，
// 所以不需要 Mcp-Session-Id，也避免常驻会话的回收问题。响应统一用
// application/json（不切 text/event-stream），客户端两种格式都认。
func NewHTTPHandler(d Deps) http.Handler {
	server := NewServer(d)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			// 本地回环 Host 校验保持 SDK 缺省（防 DNS rebinding），与画布
			// 服务自身的 guard 同向。
		},
	)
}
