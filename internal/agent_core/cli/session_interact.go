package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// onEvent 把增量写到 stdout，并把所有事件写入可选的事件文件。
func (s *Session) onEvent(evt domain.Event) {
	if s.eventsBuf != nil {
		raw, err := json.Marshal(evt)
		if err == nil {
			_, _ = s.eventsBuf.Write(raw)
			_ = s.eventsBuf.WriteByte('\n')
			_ = s.eventsBuf.Flush()
		}
	}
	if evt.Kind != domain.EvtMessageDelta {
		return
	}
	var body struct {
		PartKind string `json:"partKind"`
		Text     string `json:"text"`
	}
	if json.Unmarshal(evt.Payload, &body) != nil {
		return
	}
	if body.PartKind == "text" && body.Text != "" {
		fmt.Fprint(s.cfg.Out, body.Text)
	}
}

// uiAsk 是 host.Context.UI 的实现：把插件的 ctx.ui 请求转成交互桥上的一次交互，
// 由界面（终端或网页托盘）回答。没有界面时 broker 会立即解析成 unavailable，
// 调用方按失败关闭处理（INV-4）。
func (s *Session) uiAsk(ctx context.Context, req host.UIRequest) (host.UIResolution, error) {
	if s.broker == nil {
		return host.UIResolution{Status: domain.InteractionUnavailable}, nil
	}
	threadID := threadFrom(ctx)
	if threadID == "" {
		threadID = s.CurrentThread()
	}
	// form 的题目要能跟着 interactions 行一起持久化：表里没有 payload 列，
	// 题目就放在 body（view 侧按 kind=form 解析 body 的 JSON）。
	body := req.Message
	if req.Kind == domain.InteractionForm {
		body = string(req.Payload)
	}
	res, err := s.broker.Request(ctx, ports.InteractionRequest{
		ThreadID: threadID,
		RunID:    runFrom(ctx),
		PluginID: req.PluginID,
		Kind:     req.Kind,
		Title:    req.Title,
		Body:     body,
		Options:  req.Options,
		Payload:  req.Payload,
	})
	if err != nil {
		return host.UIResolution{}, err
	}
	return host.UIResolution{Status: res.Status, Value: res.Value}, nil
}

// onInteraction 是 CLI 的终端替身界面：确认 y/n、选择序号、输入文本。
func (s *Session) onInteraction(evt domain.Event) {
	if evt.Kind != domain.EvtInteractionRequested {
		return
	}
	var body struct {
		ThreadID      string   `json:"threadId"`
		InteractionID string   `json:"interactionId"`
		Kind          string   `json:"kind"`
		Title         string   `json:"title"`
		Body          string   `json:"body"`
		Options       []string `json:"options"`
	}
	if json.Unmarshal(evt.Payload, &body) != nil || body.InteractionID == "" {
		return
	}
	status, value := s.ask(body.Kind, body.Title, body.Body, body.Options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.broker.Respond(ctx, body.ThreadID, body.InteractionID, status, value); err != nil {
		fmt.Fprintf(s.cfg.Err, "交互响应失败: %v\n", err)
	}
}

func (s *Session) ask(kind, title, body string, options []string) (domain.InteractionStatus, string) {
	fmt.Fprintf(s.cfg.Err, "\n[交互] %s\n%s\n", title, body)
	switch kind {
	case string(domain.InteractionSelect):
		for i, opt := range options {
			fmt.Fprintf(s.cfg.Err, "  %d) %s\n", i+1, opt)
		}
		fmt.Fprint(s.cfg.Err, "选择序号（空=拒绝）: ")
		line, _ := s.in.ReadString('\n')
		line = strings.TrimSpace(line)
		var idx int
		if _, err := fmt.Sscanf(line, "%d", &idx); err == nil && idx >= 1 && idx <= len(options) {
			return domain.InteractionAccepted, options[idx-1]
		}
		return domain.InteractionDenied, ""
	case string(domain.InteractionInput):
		fmt.Fprint(s.cfg.Err, "输入（空=取消）: ")
		line, _ := s.in.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			return domain.InteractionDenied, ""
		}
		return domain.InteractionAccepted, line
	default:
		fmt.Fprint(s.cfg.Err, "允许？[y/N] ")
		line, _ := s.in.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "y" || line == "yes" {
			return domain.InteractionAccepted, "true"
		}
		return domain.InteractionDenied, "false"
	}
}
