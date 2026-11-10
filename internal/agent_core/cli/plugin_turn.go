package cli

// 通用轮次触发桥（capability: turn.invoke，仅 builtin）的 Core 侧实现。
//
// 插件只提交提示词；这里同步建线程/生成 runId，然后把轮次放到后台执行，
// 立即返回受理回执。轮次使用独立的后台 context，不随插件调用取消。

import (
	"context"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/agent_core/plugins/host"
)

const pluginTurnTimeout = 30 * time.Minute

func (s *Session) invokeTurnFromPlugin(ctx context.Context, req host.TurnRequest) (host.TurnResult, error) {
	prompt := strings.TrimSpace(req.Prompt)
	threadID := strings.TrimSpace(req.ThreadID)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = firstRunes(prompt, 40)
	}
	if threadID == "" {
		th, err := s.CreateThread(ctx, title)
		if err != nil {
			return host.TurnResult{}, err
		}
		threadID = th.ID
	} else if _, err := s.GetThread(ctx, threadID); err != nil {
		return host.TurnResult{}, err
	}
	runID, err := s.NewRunID(ctx)
	if err != nil {
		return host.TurnResult{}, err
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), pluginTurnTimeout)
		defer cancel()
		term, err := s.RunInThread(runCtx, threadID, runID, prompt, nil)
		if s.cfg.Log != nil {
			fields := map[string]any{"threadId": threadID, "runId": runID, "terminal": string(term)}
			if err != nil {
				fields["error"] = err.Error()
			}
			level := "info"
			if err != nil {
				level = "warn"
			}
			s.cfg.Log(level, "cli: 插件触发的轮次结束", fields)
		}
	}()
	return host.TurnResult{ThreadID: threadID, RunID: runID}, nil
}

func firstRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}
