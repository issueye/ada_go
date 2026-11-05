package mcp

import (
	"context"
	"encoding/json"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"browseruse/internal/config"
	"browseruse/internal/control"
	"browseruse/internal/version"
)

const instructions = "Local native browser for agents. Use snapshot before click, script for multi-step flows, and network to inspect fetch/XHR JSON APIs."

// newServer builds the MCP server and registers every control tool with its
// existing JSON Schema (raw schema, no reflection).
func newServer(svc *Service) *server.MCPServer {
	// 与应用同源的配置，只读一次：MCP 是独立进程，读同一份 config.json，
	// Script.MaxTimeoutMS 等预算因此与应用端一致。
	cfg := config.Load()
	s := server.NewMCPServer("browser-use", version.Version,
		server.WithInstructions(instructions),
		server.WithRecovery(),
	)
	for _, t := range control.Tools {
		tool := t
		s.AddTool(mcpgo.NewToolWithRawSchema(tool.Name, tool.Description, tool.Schema),
			func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return handleToolCall(ctx, svc, cfg, tool.Name, req)
			})
	}
	return s
}

func handleToolCall(ctx context.Context, svc *Service, cfg config.Config, name string, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	args, err := json.Marshal(req.Params.Arguments)
	if err != nil || len(args) == 0 || string(args) == "null" {
		args = []byte(`{}`)
	}
	// 外层期限与应用端 callTimeoutFor 同源（同一份 CallTimeout 预算）再加 5s 余量：
	// 旧实现硬编码 5 分钟，恰好等于脚本 Script.MaxTimeoutMS，脚本最长时两个
	// deadline 同时到期，谁先超时无法归因。客户端取消仍会取消父 ctx。
	budget := control.CallTimeout(name, args, 0, cfg) + 5*time.Second
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	res, err := svc.Call(ctx, name, args)
	if err != nil {
		// 工具失败走 isError 内容，而不是 JSON-RPC error，模型才能看到并自纠。
		return errorResult(err), nil
	}
	return res, nil
}
