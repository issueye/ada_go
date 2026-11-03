// Package mcpserver 将记忆引擎装配为 MCP 服务器工具集。
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mem_go/internal/memory"
)

// ServerVersion 是 MCP 服务器版本号。
const ServerVersion = "0.1.0"

// serverName 是 MCP 服务器标识。
const serverName = "mem-go"

// NewServer 创建配置好全部记忆工具的 MCP 服务器。
func NewServer(engine *memory.Engine) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: ServerVersion},
		&mcp.ServerOptions{
			Instructions: "这是 AI Agent 的长期记忆库。当用户陈述个人偏好、重要事实、" +
				"或明确要求记住/遗忘某事时，调用 memory_store / memory_forget；" +
				"回答问题前若可能已有相关记忆，先用 memory_search 召回；" +
				"scope 用于隔离不同用户或项目的记忆，默认 'default'。",
		})
	registerTools(server, engine)
	return server
}

// registerTools 注册全部记忆工具。
func registerTools(server *mcp.Server, engine *memory.Engine) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_store",
		Description: "记住一条长期记忆。用户陈述事实/偏好/约定，或要求记住某事时调用。同命名空间下语义重复的内容会自动合并更新。",
	}, storeTool(engine))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_search",
		Description: "按语义检索长期记忆，按相似度、重要度与时间衰减综合排序。回答与用户历史相关的问题前调用。",
	}, searchTool(engine))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_list",
		Description: "按最近更新时间列出记忆（不做语义检索），可按命名空间/类型/标签过滤。",
	}, listTool(engine))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_get",
		Description: "按 ID 获取单条记忆的完整内容。",
	}, getTool(engine))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_delete",
		Description: "按 ID 精确删除一条或多条记忆。",
	}, deleteTool(engine))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_forget",
		Description: "语义遗忘：删除与描述内容相似度超过阈值的记忆。用户要求忘记某事时调用。",
	}, forgetTool(engine))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_stats",
		Description: "查看记忆库统计信息（总数、类型/命名空间分布、嵌入模型等）。",
	}, statsTool(engine))
}

// ---------------- memory_store ----------------

type storeIn struct {
	Content    string   `json:"content" jsonschema:"要记住的内容（必填）"`
	Kind       string   `json:"kind,omitempty" jsonschema:"记忆类型: fact|preference|event|knowledge|procedure，默认 fact"`
	Tags       []string `json:"tags,omitempty" jsonschema:"自由标签，便于后续过滤，如 [\"go\",\"环境\"]"`
	Importance float64  `json:"importance,omitempty" jsonschema:"重要度 0.0-1.0，默认 0.5；越高在检索中权重越大"`
	Scope      string   `json:"scope,omitempty" jsonschema:"命名空间，用于隔离不同用户/项目，默认 'default'"`
}

type storeOut struct {
	Status string        `json:"status"` // created | updated
	Action string        `json:"action"` // created=新增, updated=合并更新既有记忆
	Memory memory.Memory `json:"memory"`
}

func storeTool(engine *memory.Engine) mcp.ToolHandlerFor[storeIn, storeOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in storeIn) (*mcp.CallToolResult, storeOut, error) {
		mem, action, err := engine.Store(ctx, memory.StoreInput{
			Content:    in.Content,
			Kind:       in.Kind,
			Tags:       in.Tags,
			Importance: in.Importance,
			Scope:      in.Scope,
		})
		if err != nil {
			return nil, storeOut{}, err
		}
		var head string
		if action == memory.ActionUpdated {
			head = "已合并更新既有记忆（内容相似，视为同一记忆的强化）"
		} else {
			head = "已保存新记忆"
		}
		return textResult(fmt.Sprintf("%s：%s\nID: %s | 类型: %s | 重要度: %.2f | 命名空间: %s",
				head, mem.Content, mem.ID, mem.Kind, mem.Importance, mem.Scope)),
			storeOut{Status: "ok", Action: string(action), Memory: mem}, nil
	}
}

// ---------------- memory_search ----------------

type searchIn struct {
	Query string `json:"query" jsonschema:"检索语句（必填），用自然语言描述想找的记忆"`
	Limit int    `json:"limit,omitempty" jsonschema:"返回条数，默认 5"`
	Kind  string `json:"kind,omitempty" jsonschema:"按类型过滤: fact|preference|event|knowledge|procedure"`
	Tag   string `json:"tag,omitempty" jsonschema:"按标签过滤"`
	Scope string `json:"scope,omitempty" jsonschema:"按命名空间过滤"`
}

type searchOut struct {
	Count   int             `json:"count"`
	Results []memory.Memory `json:"results"`
}

func searchTool(engine *memory.Engine) mcp.ToolHandlerFor[searchIn, searchOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		results, err := engine.Search(ctx, memory.SearchInput{
			Query: in.Query, Limit: in.Limit, Kind: in.Kind, Tag: in.Tag, Scope: in.Scope,
		})
		if err != nil {
			return nil, searchOut{}, err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("检索到 %d 条记忆：\n", len(results)))
		for i, m := range results {
			sb.WriteString(fmt.Sprintf("%d. [得分 %.3f|相似 %.3f] (%s) %s  [标签: %s | 更新: %s | ID: %s]\n",
				i+1, m.Score, m.Similarity, m.Kind, m.Content,
				renderTags(m.Tags), m.UpdatedAt.Format("2006-01-02"), m.ID))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), searchOut{Count: len(results), Results: results}, nil
	}
}

// ---------------- memory_list ----------------

type listIn struct {
	Scope string `json:"scope,omitempty" jsonschema:"按命名空间过滤"`
	Kind  string `json:"kind,omitempty" jsonschema:"按类型过滤: fact|preference|event|knowledge|procedure"`
	Tag   string `json:"tag,omitempty" jsonschema:"按标签过滤"`
	Limit int    `json:"limit,omitempty" jsonschema:"返回条数，默认 20"`
}

type listOut struct {
	Count   int             `json:"count"`
	Results []memory.Memory `json:"results"`
}

func listTool(engine *memory.Engine) mcp.ToolHandlerFor[listIn, listOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		results, err := engine.List(ctx, memory.ListInput{Scope: in.Scope, Kind: in.Kind, Tag: in.Tag, Limit: in.Limit})
		if err != nil {
			return nil, listOut{}, err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("共 %d 条记忆（按更新时间倒序）：\n", len(results)))
		for i, m := range results {
			sb.WriteString(fmt.Sprintf("%d. (%s) %s  [标签: %s | 更新: %s | ID: %s]\n",
				i+1, m.Kind, m.Content, renderTags(m.Tags),
				m.UpdatedAt.Format("2006-01-02 15:04"), m.ID))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), listOut{Count: len(results), Results: results}, nil
	}
}

// ---------------- memory_get ----------------

type getIn struct {
	ID string `json:"id" jsonschema:"记忆 ID（必填）"`
}

func getTool(engine *memory.Engine) mcp.ToolHandlerFor[getIn, memory.Memory] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, memory.Memory, error) {
		mem, err := engine.Get(ctx, in.ID)
		if err != nil {
			return nil, memory.Memory{}, err
		}
		data, _ := json.MarshalIndent(mem, "", "  ")
		return textResult("记忆详情：\n" + string(data)), mem, nil
	}
}

// ---------------- memory_delete ----------------

type deleteIn struct {
	IDs []string `json:"ids" jsonschema:"要删除的记忆 ID 列表（必填）"`
}

type deleteOut struct {
	Deleted int `json:"deleted"`
}

func deleteTool(engine *memory.Engine) mcp.ToolHandlerFor[deleteIn, deleteOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		n, err := engine.Delete(ctx, in.IDs)
		if err != nil {
			return nil, deleteOut{}, err
		}
		return textResult(fmt.Sprintf("已删除 %d 条记忆。", n)), deleteOut{Deleted: n}, nil
	}
}

// ---------------- memory_forget ----------------

type forgetIn struct {
	Query     string  `json:"query" jsonschema:"要遗忘的内容描述（必填）"`
	Threshold float64 `json:"threshold,omitempty" jsonschema:"相似度阈值 0-1，默认 0.8；调高更保守"`
	Limit     int     `json:"limit,omitempty" jsonschema:"最多删除条数，默认 5"`
	Kind      string  `json:"kind,omitempty" jsonschema:"按类型过滤"`
	Tag       string  `json:"tag,omitempty" jsonschema:"按标签过滤"`
	Scope     string  `json:"scope,omitempty" jsonschema:"按命名空间过滤"`
}

type forgetOut struct {
	Deleted int             `json:"deleted"`
	Removed []memory.Memory `json:"removed"`
}

func forgetTool(engine *memory.Engine) mcp.ToolHandlerFor[forgetIn, forgetOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in forgetIn) (*mcp.CallToolResult, forgetOut, error) {
		removed, err := engine.Forget(ctx, memory.ForgetInput{
			Query: in.Query, Threshold: in.Threshold, Limit: in.Limit,
			Kind: in.Kind, Tag: in.Tag, Scope: in.Scope,
		})
		if err != nil {
			return nil, forgetOut{}, err
		}
		if len(removed) == 0 {
			return textResult("没有找到与描述足够相似的记忆，未删除任何内容。"), forgetOut{Deleted: 0, Removed: []memory.Memory{}}, nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("已遗忘 %d 条记忆：\n", len(removed)))
		for i, m := range removed {
			sb.WriteString(fmt.Sprintf("%d. %s (ID: %s)\n", i+1, m.Content, m.ID))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), forgetOut{Deleted: len(removed), Removed: removed}, nil
	}
}

// ---------------- memory_stats ----------------

func statsTool(engine *memory.Engine) mcp.ToolHandlerFor[struct{}, memory.Stats] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, memory.Stats, error) {
		st, err := engine.Stats(ctx)
		if err != nil {
			return nil, memory.Stats{}, err
		}
		data, _ := json.MarshalIndent(st, "", "  ")
		return textResult("记忆库统计：\n" + string(data)), st, nil
	}
}

// ---------------- 输出工具函数 ----------------

// textResult 构造纯文本结果（structured content 由 SDK 根据 Out 类型填充）。
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// renderTags 渲染标签列表用于文本输出。
func renderTags(tags []string) string {
	if len(tags) == 0 {
		return "-"
	}
	return strings.Join(tags, ",")
}
