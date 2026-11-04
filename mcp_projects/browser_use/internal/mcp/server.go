package mcp

import (
	"context"
	"encoding/json"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"browseruse/internal/control"
	"browseruse/internal/version"
)

const instructions = "Local native browser for agents. Use snapshot before click, script for multi-step flows, and network to inspect fetch/XHR JSON APIs."

// newServer builds the MCP server and registers every control tool with its
// existing JSON Schema (raw schema, no reflection).
func newServer(svc *Service) *server.MCPServer {
	s := server.NewMCPServer("browser-use", version.Version,
		server.WithInstructions(instructions),
		server.WithRecovery(),
	)
	for _, t := range control.Tools {
		tool := t
		s.AddTool(mcpgo.NewToolWithRawSchema(tool.Name, tool.Description, tool.Schema),
			func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return handleToolCall(ctx, svc, tool.Name, req)
			})
	}
	return s
}

func handleToolCall(ctx context.Context, svc *Service, name string, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	// 与旧实现一致：单次工具调用最多 5 分钟；客户端取消会取消父 ctx。
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	args, err := json.Marshal(req.Params.Arguments)
	if err != nil || len(args) == 0 || string(args) == "null" {
		args = []byte(`{}`)
	}
	res, err := svc.Call(ctx, name, args)
	if err != nil {
		// 工具失败走 isError 内容，而不是 JSON-RPC error，模型才能看到并自纠。
		return errorResult(err), nil
	}
	return res, nil
}
