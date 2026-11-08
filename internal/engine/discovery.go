package engine

// 发现式工具的基座入口：find_tool / exec_tool。
//
// TODO(插件化)：本文件目前内联在引擎，后续整体搬迁到插件形态——
//   - find_tool → 目录只读投影插件（读 catalog 快照，按 query 过滤）；
//   - exec_tool → 工具执行插件（按名调内层工具，审批链照旧）。
// 前置条件是宿主新增两个受控桥并只授予 builtin：catalog.read（只读目录投影，
// 不开放注册面）与 tool.invoke（宿主代跑内层工具，beforeCall/policyDeny/Runner
// 链路在宿主侧保持单份，避免脚本绕过审批）。搬迁后 domain 的保留名与
// toolkit 的保留名校验保留，`discovery` 注册位语义不变。
// 暫留在引擎的原因：跨插件调用无通路（Invoker 按注册表路由到插件自身）、
// 审批链在引擎侧、给脚本开目录读/任意执行等于新增高权限能力位。
//
// 插件在 registerTool 时可以选 discovery: true（发现式）：工具声明不注入模型
// 清单，模型先 find_tool 拿到名称/说明/参数 Schema，再 exec_tool 按名执行。
// 适合低频或大 Schema 工具，省上下文；注入式（缺省）行为不变。
//
// 实现要点（与注入式同一套安全语义）：
//   - 这两个名字是基座保留名（domain.ToolFindName/ToolExecName），不进工具目录；
//     只有目录里存在发现式工具时才把它们的声明注入模型（否则模型根本不需要）。
//   - exec_tool 的内层调用走与注入式完全相同的链路：beforeCall 审批钩子
//     （策略插件照内层描述符询问/拒绝）→ policyDeny 失败关闭 → Runner.Execute
//     （参数校验、超时、截断、检查点都由 toolkit Executor 承担）。
//   - 内层回执状态原样上浮为 exec_tool 的回执状态（denied/timeout 等界面语义不丢）。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// okResult 构造基座入口自己的 ok 回执（exec_tool 的成功路径上浮内层回执，不走这里）。
func okResult(now time.Time, text string, details map[string]any) domain.Result {
	return domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: text}},
		Details: details,
		Receipt: domain.Receipt{Status: domain.ReceiptOK, StartedAt: now, FinishedAt: now},
	}
}

// invalidResult 构造 exec_tool 的参数/目标错误回执：与 runTools 的
// tool_not_found 同形（error 状态 + 原因 + 给模型的下一步提示）。
func invalidResult(reason, text string) domain.Result {
	return domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: text}},
		Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: reason},
	}
}

// deniedResult 构造审批链产生的 denied 回执（runTools.recordDenied 同形）。
func deniedResult(reason, detail string) domain.Result {
	return domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: "denied by policy: " + detail}},
		Receipt: domain.Receipt{Status: domain.ReceiptDenied, Reason: reason},
	}
}

// maxFindResults 是 find_tool 单次返回的工具数上限：发现式工具目录不该无限膨胀，
// 超出时按名称截断（模型可以用 query 收窄）。
const maxFindResults = 64

// baseDescriptor 构造基座工具的描述符。PluginID 留空：它们不进工具目录
// （toolkit 要求贡献者非空，正是为了挡住它们被注册），只在引擎内联消费。
func baseDescriptor(name, description string, parameters json.RawMessage) domain.Descriptor {
	return domain.Descriptor{
		Name:        name,
		Description: description,
		Parameters:  parameters,
		Access:      domain.AccessRead,
	}
}

const findToolParams = `{"type":"object","properties":{"query":{"type":"string","description":"可选关键词：按名称或说明过滤（大小写不敏感的包含匹配）。省略时列出全部。"}},"additionalProperties":false}`

const execToolParams = `{"type":"object","required":["tool"],"properties":{"tool":{"type":"string","description":"要执行的工具名（来自 find_tool 的结果）"},"args":{"type":"object","description":"该工具的参数，必须符合 find_tool 返回的 parameters Schema"}},"additionalProperties":false}`

// executeTool 分派一次已批准的工具调用：基座入口（find_tool/exec_tool）内联
// 处理，其余走 Runner（toolkit Executor：参数校验、超时、截断、检查点）。
// 返回值：结果、循环观察身份（描述符 + 参数，exec_tool 用内层身份）、
// 是否中止轮次（内层审批钩子 abort 时为 true，调用方不记结果）。
func (r *run) executeTool(call domain.ToolCall, desc domain.Descriptor, args json.RawMessage) (domain.Result, domain.Descriptor, json.RawMessage, bool) {
	switch call.Tool {
	case domain.ToolFindName:
		return r.findToolResult(args), desc, args, false
	case domain.ToolExecName:
		return r.execToolResult(call, args)
	default:
		res := r.deps.Runner.Execute(r.ctx, domain.ToolCall{CallID: call.CallID, Tool: call.Tool, Args: args})
		return res, desc, args, false
	}
}

// findToolDescriptor / execToolDescriptor 是注入给模型的基座声明（也是
// runTools 里审批、循环检测用的描述符；Access=read，策略插件不会拦截）。
func findToolDescriptor() domain.Descriptor {
	return baseDescriptor(domain.ToolFindName,
		"发现可用工具：列出本会话里未直接注入的工具（名称/说明/参数 Schema）。"+
			"拿到结果后用 exec_tool 执行；没有匹配项时返回空数组。",
		json.RawMessage(findToolParams))
}

func execToolDescriptor() domain.Descriptor {
	return baseDescriptor(domain.ToolExecName,
		"执行一个发现式工具：tool 填 find_tool 结果里的名称，args 按它的 parameters Schema 构造。"+
			"审批与超时沿用该工具自身的配置；只有 find_tool 列出的工具才能这样执行。",
		json.RawMessage(execToolParams))
}

// hasDiscoveryTools 报告目录里是否存在发现式工具：决定要不要把基座入口注入模型。
func hasDiscoveryTools(list []domain.Descriptor) bool {
	for _, d := range list {
		if d.Discovery {
			return true
		}
	}
	return false
}

// withBaseTools 在目录快照上追加基座入口描述符（仅当存在发现式工具）。
// 追加后模型可见集合 = 注入式工具 + find/exec，runTools 的 byName 与
// toolSchemas 用同一份快照，两边永远一致。
func withBaseTools(list []domain.Descriptor) []domain.Descriptor {
	if !hasDiscoveryTools(list) {
		return list
	}
	out := make([]domain.Descriptor, 0, len(list)+2)
	out = append(out, list...)
	out = append(out, findToolDescriptor(), execToolDescriptor())
	return out
}

// findToolResult 处理 find_tool：列出发现式工具（可选 query 过滤）。
func (r *run) findToolResult(args json.RawMessage) domain.Result {
	var req struct {
		Query string `json:"query"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &req)
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))

	list := r.deps.Tools.List(r.ctx)
	type toolListing struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Access      string          `json:"access"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	out := make([]toolListing, 0, 8)
	names := make([]string, 0, 8)
	for _, d := range list {
		if !d.Discovery {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(d.Name), query) &&
			!strings.Contains(strings.ToLower(d.Description), query) {
			continue
		}
		names = append(names, d.Name)
		out = append(out, toolListing{
			Name: d.Name, Description: d.Description,
			Access: string(d.Access), Parameters: d.Parameters,
		})
		if len(out) >= maxFindResults {
			break
		}
	}
	// 按名称稳定排序，模型看到的顺序可复现。
	sort.Strings(names)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	payload, err := json.Marshal(out)
	if err != nil {
		return invalidResult("encoding_error", "find_tool 结果无法编码: "+err.Error())
	}
	return okResult(r.deps.Clock.Now(), string(payload), map[string]any{"count": len(out), "query": query})
}

// execToolResult 处理 exec_tool：按名执行一个发现式工具。
//
// 返回 abort=true 表示内层审批钩子中止了轮次（与注入式被钩子中止同形：
// 调用方不发结果、按 toolsErrored 收敛）。
func (r *run) execToolResult(call domain.ToolCall, args json.RawMessage) (domain.Result, domain.Descriptor, json.RawMessage, bool) {
	var req struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return invalidResult("invalid_args", "exec_tool 参数必须是 {tool, args} 对象"), domain.Descriptor{}, nil, false
		}
	}
	name := strings.TrimSpace(req.Tool)
	if name == "" {
		return invalidResult("invalid_args", "exec_tool 缺少 tool：先用 find_tool 查看可用工具"), domain.Descriptor{}, nil, false
	}
	if name == domain.ToolFindName || name == domain.ToolExecName {
		return invalidResult("invalid_target", name+" 是基座入口本身，不需要（也不能）经 exec_tool 执行"), domain.Descriptor{}, nil, false
	}

	// 内层工具必须在目录里且是发现式：注入式工具模型本来就能直接调用，
	// 不经 exec_tool 是刻意的契约（发现式才有「按发现执行」的语义）。
	var inner domain.Descriptor
	found := false
	for _, d := range r.deps.Tools.List(r.ctx) {
		if d.Name == name {
			inner = d
			found = true
			break
		}
	}
	if !found {
		res := domain.Result{
			Content: []domain.Part{{Type: domain.PartText,
				Text: "tool_not_found: exec_tool 找不到工具 " + name + "；用 find_tool 查看当前可用的发现式工具"}},
			Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: "tool_not_found"},
		}
		return res, domain.Descriptor{}, nil, false
	}
	if !inner.Discovery {
		res := domain.Result{
			Content: []domain.Part{{Type: domain.PartText,
				Text: "invalid_target: " + name + " 是注入式工具，直接调用即可，不经 exec_tool"}},
			Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: "invalid_target"},
		}
		return res, domain.Descriptor{}, nil, false
	}

	innerCall := domain.ToolCall{CallID: call.CallID, Tool: inner.Name, Args: nonEmptyArgs(req.Args)}

	// 审批链与注入式逐位一致：beforeCall 钩子（快照用内层描述符，策略插件
	// 看到的是真正的工具名与参数）→ 失败关闭 → 超时/执行交给 Runner。
	verdict, err := r.beforeCall(innerCall, inner)
	if err != nil {
		// tool.beforeCall 是唯一失败关闭的安全点（06 §6.6）。
		return deniedResult("hook failed", err.Error()), domain.Descriptor{}, nil, false
	}
	if verdict.Aborted {
		r.emitErrorCode(domain.CodeToolInvalidArgs, "控制面钩子中止了工具调用: "+verdict.Reason)
		return domain.Result{}, domain.Descriptor{}, nil, true
	}
	if !verdict.Allowed {
		r.hook(HookToolAfterCall, mustJSON(map[string]any{
			"callId": innerCall.CallID, "tool": inner.Name, "status": "denied",
		}))
		return deniedResult(verdict.Reason, verdict.Reason), domain.Descriptor{}, nil, false
	}
	innerArgs := innerCall.Args
	if len(verdict.Replacements) > 0 {
		if replaced, ok := coerceArgs(verdict.Replacements); ok {
			innerArgs = replaced
		}
	}
	if reason, denied := r.policyDeny(inner); denied {
		return deniedResult(reason, reason+"（没有可用的策略插件）"), domain.Descriptor{}, nil, false
	}

	innerRes := r.deps.Runner.Execute(r.ctx, domain.ToolCall{CallID: innerCall.CallID, Tool: inner.Name, Args: innerArgs})
	r.hook(HookToolAfterCall, mustJSON(map[string]any{
		"callId": innerCall.CallID, "tool": inner.Name, "status": string(innerRes.Receipt.Status),
	}))

	// 内层结果原样上浮：内容给模型，回执状态给界面（denied/timeout 语义不丢）；
	// Details 补一个 tool 键，工具卡能看出真正执行的是谁。
	res := innerRes
	if res.Details == nil {
		res.Details = map[string]any{}
	}
	res.Details["tool"] = inner.Name
	if res.Receipt.Status != domain.ReceiptOK && len(res.Content) == 0 {
		res.Content = []domain.Part{{Type: domain.PartText,
			Text: fmt.Sprintf("%s 以 %s 结束，没有产出内容", inner.Name, res.Receipt.Status)}}
	}
	// 循环检测按内层身份观察：同一发现式工具的相同调用照样累计；repeatable 的
	// 内层工具（轮询类）保留豁免。
	return res, inner, innerArgs, false
}
