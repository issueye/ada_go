package cli

// 插件只读事件查询（capability: events.read）的 Core 侧实现。
//
// 数据源是 coreproto.Emitter 的可回放缓冲：只保留最近一段事件，溢出时 Replay
// 返回 ok=false，调用方（插件）应降级为轮询 thread.get/usage。查询被限制在
// 当前线程上下文内；工作区级事件不通过该通道暴露。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/adag/ada-go/internal/agent_core/plugins/host"
)

// queryPluginEvents 实现 host.Context.Events。
func (s *Session) queryPluginEvents(ctx context.Context, q host.EventQuery) ([]host.EventView, error) {
	if s.emitter == nil {
		return nil, errors.New("事件源未就绪")
	}
	threadID := strings.TrimSpace(q.ThreadID)
	if threadID == "" {
		return nil, errors.New("events.query 需要线程上下文")
	}
	events, ok := s.emitter.Replay(threadID, q.FromSeq)
	if !ok {
		return nil, fmt.Errorf("线程 %s 的事件缓冲已溢出，请改用 thread.get/usage 轮询", threadID)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	types := map[string]bool{}
	for _, t := range q.Types {
		if t = strings.TrimSpace(t); t != "" {
			types[t] = true
		}
	}
	capHint := len(events)
	if capHint > limit {
		capHint = limit
	}
	out := make([]host.EventView, 0, capHint)
	for _, evt := range events {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		if len(types) > 0 && !types[string(evt.Kind)] {
			continue
		}
		out = append(out, host.EventView{
			Seq:      evt.Seq,
			Type:     string(evt.Kind),
			ThreadID: evt.ThreadID,
			At:       evt.At,
			Data:     append([]byte(nil), evt.Payload...),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// queryPluginUsage 实现 host.Context.Usage：只返回工作区内的原始用量行，
// 不做展示口径聚合。threadID 为空表示整个工作区。
func (s *Session) queryPluginUsage(ctx context.Context, q host.UsageQuery) ([]host.UsageRow, error) {
	if s.store == nil {
		return nil, errors.New("用量存储未就绪")
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.store.UsageRows(ctx, strings.TrimSpace(q.ThreadID), limit)
	if err != nil {
		return nil, err
	}
	out := make([]host.UsageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, host.UsageRow{
			ThreadID: row.ThreadID, Provider: row.Provider, Model: row.Model,
			InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
			CacheTokens: row.CacheTokens, CostMicros: row.CostMicros, At: row.CreatedAt,
		})
	}
	return out, nil
}
