package corerpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/proto"
)

// ---- interaction ----

func (s *Server) handleInteractionList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = decode(raw, &p)
	return map[string]any{"interactions": interactionsJSON(s.pending(ctx, p.ThreadID))}, nil
}

func (s *Server) handleInteractionRespond(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID  string         `json:"threadId"`
		RequestID string         `json:"requestId"`
		Decision  string         `json:"decision"`
		Value     map[string]any `json:"value"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" || p.RequestID == "" {
		return nil, invalid("interaction.respond: 需要 threadId 与 requestId")
	}
	status, value, err := decisionOf(p.Decision, p.Value)
	if err != nil {
		return nil, err
	}
	res, err := s.sess.RespondInteraction(ctx, p.ThreadID, p.RequestID, status, value)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{
		"interactionId": res.RequestID,
		"threadId":      p.ThreadID,
		"status":        string(res.Status),
		"response":      map[string]any{"value": res.Value},
		"resolvedAt":    res.ResolvedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// ---- action（M4：前端动作 → 插件后端动作）----

// handleActionInvoke 实现 action.invoke：ActionEnvelope 进，{ok, result|error} 出。
// 超时/崩溃/拒绝等插件侧失败按协议收敛成 ok=false + 人话错误（不是 RPC 异常），
// 让界面可以就地显示；参数缺失才是 invalid_params。
func (s *Server) handleActionInvoke(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.ActionEnvelope
	if err := decode(raw, &p); err != nil {
		return nil, invalid("action.invoke: " + err.Error())
	}
	if p.PluginID == "" || p.Action == "" {
		return nil, invalid("action.invoke: 需要 pluginId 与 action")
	}
	input := json.RawMessage("{}")
	if len(p.Input) > 0 {
		enc, err := json.Marshal(p.Input)
		if err != nil {
			return nil, invalid("action.invoke: input 无法编码: " + err.Error())
		}
		input = enc
	}
	out, err := s.sess.CallAction(ctx, p.PluginID, p.Action, input, p.ThreadID)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return map[string]any{"ok": false, "error": de.Detail}, nil
		}
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	var result any
	if len(out) > 0 {
		if err := json.Unmarshal(out, &result); err != nil {
			return map[string]any{"ok": false, "error": "动作结果不是合法 JSON: " + err.Error()}, nil
		}
	}
	return map[string]any{"ok": true, "result": result}, nil
}

func decisionOf(decision string, value map[string]any) (domain.InteractionStatus, string, error) {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "accept":
		return domain.InteractionAccepted, valueString(value), nil
	case "deny":
		return domain.InteractionDenied, valueString(value), nil
	case "cancel":
		return domain.InteractionCancelled, "", nil
	default:
		return "", "", invalid("interaction.respond: decision 只能是 accept/deny/cancel")
	}
}

func valueString(value map[string]any) string {
	for _, key := range []string{"option", "text", "value"} {
		if v, ok := value[key]; ok {
			return fmt.Sprint(v)
		}
	}
	if len(value) == 0 {
		return "true"
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}
