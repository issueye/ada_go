package cli

import (
	"context"
	"encoding/json"

	"github.com/adag/ada-go/internal/agent_core/coreproto"
	"github.com/adag/ada-go/internal/agent_core/engine"
	"github.com/adag/ada-go/internal/common/adapters/sqlite"
	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// interactionStore 把 coreproto 的持久化回调接到 SQLite（04 interactions 表）。
type interactionStore struct{ st *sqlite.Store }

func (i interactionStore) SavePending(ctx context.Context, req ports.InteractionRequest) error {
	return i.st.CreateInteraction(ctx, req)
}

func (i interactionStore) Resolve(ctx context.Context, _ string, rec coreproto.InteractionRecord) error {
	_, err := i.st.ResolveInteraction(ctx, rec.ThreadID, rec.RequestID, rec.Status, rec.Value)
	return err
}

func (i interactionStore) ListPending(ctx context.Context, threadID string) ([]ports.InteractionRequest, error) {
	rows, err := i.st.ListPendingInteractions(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]ports.InteractionRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.InteractionRequest)
	}
	return out, nil
}

// runStore 把 engine.RunStore 接到 SQLite（04 runs 表）。
type runStore struct {
	st    *sqlite.Store
	model func(threadID string) ports.ModelRef
}

func (r runStore) StartRun(ctx context.Context, rec engine.RunRecord) error {
	_, err := r.st.StartRun(ctx, sqlite.Run{
		ID: rec.ID, ThreadID: rec.ThreadID, Status: sqlite.RunRunning, StartedAt: rec.StartedAt,
	})
	return err
}

func (r runStore) EndRun(ctx context.Context, rec engine.RunRecord) error {
	usage, err := json.Marshal(rec.Usage)
	if err != nil {
		return err
	}
	if err := r.st.FinishRun(ctx, rec.ID, rec.Status, rec.ErrorCode, usage); err != nil {
		return err
	}
	if rec.Usage == (domain.Usage{}) {
		return nil
	}
	// 把本轮用量另行记入 04 §5.7 的 usage 表，usage.get 的线程/工作区聚合
	// 依赖它（runs.usage_json 只是轮次快照）。provider/model 取当前生效模型。
	provider, model := "unknown", "unknown"
	if r.model != nil {
		ref := r.model(rec.ThreadID)
		if ref.ProviderID != "" {
			provider = ref.ProviderID
		}
		if ref.ModelID != "" {
			model = ref.ModelID
		}
	}
	return r.st.RecordUsage(ctx, sqlite.UsageRecord{
		ThreadID: rec.ThreadID, Provider: provider, Model: model,
		InputTokens:  rec.Usage.InputTokens,
		OutputTokens: rec.Usage.OutputTokens,
		CacheTokens:  rec.Usage.CacheTokens,
		CostMicros:   rec.Usage.CostMicros,
		CreatedAt:    rec.EndedAt,
	})
}
