package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// decodeToolResult 把脚本返回值结算成引擎侧 Result（05 §6.3 ToolReturn）。
func decodeToolResult(pluginID string, raw json.RawMessage, started time.Time) (domain.Result, error) {
	var body struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Mime string `json:"mime"`
			Data string `json:"data"`
		} `json:"content"`
		Details   map[string]any `json:"details"`
		Terminate bool           `json:"terminate"`
		// Status 允许系统插件包装内层工具时保留 denied/timeout 等回执状态；
		// 缺省仍是 ok。
		Status string `json:"status,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return domain.Result{}, fmt.Errorf("host: 插件 %s 的工具结果不是 ToolReturn 形状: %w", pluginID, err)
		}
	}
	out := domain.Result{Details: body.Details}
	for _, part := range body.Content {
		switch part.Type {
		case "text":
			out.Content = append(out.Content, domain.Part{Type: domain.PartText, Text: part.Text})
		case "image":
			if part.Mime == "" || part.Data == "" {
				return domain.Result{}, fmt.Errorf("host: 插件 %s 的工具结果里 image 部件缺 mime/data", pluginID)
			}
			out.Content = append(out.Content, domain.Part{Type: domain.PartImage, Mime: part.Mime, Data: part.Data})
		case "":
			// 忽略空部件，不把形状错误当成内容错误。
		default:
			return domain.Result{}, fmt.Errorf("host: 插件 %s 的工具结果里有未知部件类型 %q（只能 text/image）", pluginID, part.Type)
		}
	}
	now := time.Now().UTC()
	out.Receipt = domain.Receipt{
		Status:     domain.ReceiptOK,
		StartedAt:  started.UTC(),
		FinishedAt: now,
		DurationMS: now.Sub(started).Milliseconds(),
	}
	if body.Status != "" {
		switch domain.ReceiptStatus(body.Status) {
		case domain.ReceiptOK, domain.ReceiptError, domain.ReceiptDenied, domain.ReceiptTimeout, domain.ReceiptInterrupted:
			out.Receipt.Status = domain.ReceiptStatus(body.Status)
			out.Receipt.Reason = body.Reason
		default:
			return domain.Result{}, fmt.Errorf("host: 插件 %s 返回了未知工具状态 %q", pluginID, body.Status)
		}
	}
	return out, nil
}

// decodeHookOutcome 把脚本返回值结算成钩子动作（03 §5.6 的 { action, reason }）。
func decodeHookOutcome(pluginID string, raw json.RawMessage) (ports.HookOutcome, error) {
	out := ports.HookOutcome{PluginID: pluginID, Action: ports.HookObserve}
	if len(raw) == 0 {
		return out, nil
	}
	var body struct {
		Action      string          `json:"action"`
		Reason      string          `json:"reason"`
		Note        string          `json:"note"`
		Replacement json.RawMessage `json:"replacement"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ports.HookOutcome{}, fmt.Errorf("host: 插件 %s 的钩子结果不是 {action, reason} 形状: %w", pluginID, err)
	}
	switch ports.HookAction(strings.TrimSpace(body.Action)) {
	case "", ports.HookObserve:
		out.Action = ports.HookObserve
	case ports.HookNote:
		out.Action = ports.HookNote
	case ports.HookReplace:
		out.Action = ports.HookReplace
	case ports.HookAbort:
		out.Action = ports.HookAbort
	default:
		return ports.HookOutcome{}, fmt.Errorf("host: 插件 %s 返回了未知钩子动作 %q（只有 observe/note/replace/abort）", pluginID, body.Action)
	}
	out.Reason = body.Reason
	out.Note = body.Note
	if len(body.Replacement) > 0 && string(body.Replacement) != "null" {
		out.Replacement = body.Replacement
	}
	return out, nil
}

// helpers ------------------------------------------------------------------

// decodeJSValue 用 JSON 边界把脚本对象读成 Go 结构（05 §6.3 的 JSON 边界）。
func decodeJSValue(rt *mojsRuntime, v moejs.Value, out any) error {
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// jsFunc 在对象上按候选名找一个可调用成员。
func jsFunc(rt *mojsRuntime, obj moejs.Value, names ...string) (moejs.Value, string, error) {
	for _, name := range names {
		v, err := rt.Get(obj, name)
		if err != nil {
			continue
		}
		if engine.IsCallable(v) {
			return v, name, nil
		}
	}
	return moejs.Value{}, "", fmt.Errorf("缺少可调用成员（期望 %s）", strings.Join(names, " / "))
}

// jsInt 把脚本数字读成 int64（不是数字时 ok=false）。
func jsInt(v moejs.Value) (int64, bool) {
	if !v.IsNumber() {
		return 0, false
	}
	return int64(v.AsNumber()), true
}

// goFields 把脚本的 fields 对象摊成 map[string]any（日志与审计字段）。
func (p *Instance) goFields(rt *mojsRuntime, v moejs.Value) map[string]any {
	if !v.IsObject() {
		return nil
	}
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) > 32 {
		// 日志字段不做无界放大：超出的键名按字典序截断。
		keys := make([]string, 0, len(out))
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		trimmed := make(map[string]any, 32)
		for _, k := range keys[:32] {
			trimmed[k] = out[k]
		}
		return trimmed
	}
	return out
}

// stringList 读一个字符串数组（select 的 options）。
func (p *Instance) stringList(rt *mojsRuntime, v moejs.Value) []string {
	if !v.IsObject() {
		return nil
	}
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// errAgentAPIUnavailable 是 agent:host 在装配期之外被调用时的错误（诊断用）。
var errAgentAPIUnavailable = errors.New("host: agent API 未就绪")
