package engine

// 上下文分项统计：会话页的 token 环要能说清「这些 token 花在哪」。
//
// 估算口径必须和引擎的水位判断（estimateTokens）以及提示词装配（buildSystem /
// toolSchemas）同源，否则环上显示一个数、压缩按另一个数触发，用户会被两个数字
// 同时误导。这里只导出薄包装，不复制算法。

import (
	"context"
	"encoding/json"

	"github.com/adag/ada-go/internal/common/ports"
)

// EstimateTokens 是保守 token 估算的导出口径（CJK 每字 1 token，其余 4 字节 1 token）。
// Core RPC 的上下文分项与引擎水位共用它。
func EstimateTokens(s string) int { return estimateTokens(s) }

// SystemPromptTokens 估算一次轮次的系统提示占用（与 run.systemPrompt 同源：
// 安全前言 + 插件片段 + 技能元数据 + 本轮上下文说明）。
func SystemPromptTokens(ctx context.Context, spec TurnSpec, prompts PromptSource, skills SkillSource) int {
	return estimateTokens(buildSystem(ctx, spec, prompts, skills))
}

// ToolSchemaTokens 估算工具声明占用（与 streamOnce 发给模型的 toolSchemas 同源）。
func ToolSchemaTokens(ctx context.Context, catalog ports.ToolCatalog) int {
	if catalog == nil {
		return 0
	}
	raw, err := json.Marshal(toolSchemas(catalog, ctx))
	if err != nil {
		return 0
	}
	return estimateTokens(string(raw))
}
