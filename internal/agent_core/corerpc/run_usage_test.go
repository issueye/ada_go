package corerpc_test

// run.usage 是输入框下方 tok/s 与缓存命中率的数据源。
//
// 引擎的 evt.usage 载荷是**本轮累计快照**（usageAcc.snapshot 每次发总量），
// corerpc 必须在广播时把它收敛进 runInfo，并让 thread.get / turn.get 的
// run.usage 带出同一份累计值。这里钉三件事：
//  1. 运行中就能读到 usage（不必等终态）；
//  2. 多次 evt.usage 是覆盖而不是累加（否则 tok/s 会被放大）；
//  3. 终态后 run.usage 保留最后一帧（页面据此冻结整轮平均速度）。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// usageModel 发两帧用量后结束。引擎的 evt.usage 载荷是**本轮累计**（每次请求
// 的用量累进 run 级累加器，发的是累加后的总量）：第一帧输出 200，第二帧是
// 200+400=600。这里用「400 的第二帧」把覆盖语义和累加语义区分开：
// 覆盖 → 600；若 corerpc 把两帧再相加 → 800。
type usageModel struct{ entered chan struct{} }

func (m usageModel) Stream(ctx context.Context, _ ports.Request) (ports.Stream, error) {
	return &usageStream{ctx: ctx, entered: m.entered}, nil
}

type usageStream struct {
	ctx     context.Context
	entered chan struct{}
	step    int
	done    bool
}

func (s *usageStream) Recv() (ports.Delta, error) {
	s.step++
	switch s.step {
	case 1:
		return ports.Delta{Kind: ports.DeltaTextStart}, nil
	case 2:
		return ports.Delta{Kind: ports.DeltaUsage, Usage: &domain.Usage{InputTokens: 100, OutputTokens: 200, CacheTokens: 60}}, nil
	case 3:
		// 第二帧：另一次模型请求，输出 400。引擎自身把它累进 run 级累加器
		// （200+400=600）后发 600。若 corerpc 再把两帧相加，终值会变成 800。
		return ports.Delta{Kind: ports.DeltaUsage, Usage: &domain.Usage{InputTokens: 100, OutputTokens: 400, CacheTokens: 60}}, nil
	case 4:
		return ports.Delta{Kind: ports.DeltaFinish, FinishReason: "stop"}, nil
	default:
		return ports.Delta{}, context.Canceled
	}
}

func (s *usageStream) Close() error { return nil }

func TestRunUsageExposedAsCumulativeSnapshot(t *testing.T) {
	sess := newSession(t, usageModel{})
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")
	threadID := createThread(t, c, "用量暴露")

	c.call(t, "turn.start", map[string]any{
		"threadId":       threadID,
		"idempotencyKey": "run-usage-001",
		"text":           "继续",
	})

	type usageDTO struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
		CacheTokens  int64 `json:"cacheTokens"`
		CostMicros   int64 `json:"costMicros"`
	}
	var state struct {
		Run struct {
			Status string    `json:"status"`
			Usage  *usageDTO `json:"usage"`
		} `json:"run"`
	}

	// 等到 usage 出现（哪怕还没到终态）：页面在运行中就要显示 tok/s。
	deadline := time.Now().Add(3 * time.Second)
	var lastRaw json.RawMessage
	for {
		raw := c.call(t, "thread.get", map[string]any{"threadId": threadID, "itemLimit": 10})
		lastRaw = raw
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("thread.get 解析失败: %v (%s)", err, raw)
		}
		if state.Run.Usage != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("thread.get 始终没有带出 run.usage: %s", raw)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 等轮次收敛到终态，再读一次：usage 必须保留最后一帧。
	deadline = time.Now().Add(3 * time.Second)
	for {
		raw := c.call(t, "thread.get", map[string]any{"threadId": threadID, "itemLimit": 10})
		lastRaw = raw
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("thread.get 解析失败: %v (%s)", err, raw)
		}
		if state.Run.Status == "terminal" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("轮次未收敛到 terminal: %s", raw)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if state.Run.Usage == nil {
		t.Fatalf("终态后 run.usage 丢失（页面会算不出整轮平均速度）: %s", lastRaw)
	}
	// 关键断言：累计快照是覆盖语义。两帧给的是 200 → 600，
	// 再累加会得到 800。
	if state.Run.Usage.OutputTokens != 600 {
		t.Fatalf("outputTokens = %d，期望 600（累计快照覆盖，不是累加）；响应 %s",
			state.Run.Usage.OutputTokens, lastRaw)
	}
	if state.Run.Usage.InputTokens != 200 || state.Run.Usage.CacheTokens != 120 {
		t.Fatalf("usage 其它字段被改坏: %+v（响应 %s）", state.Run.Usage, lastRaw)
	}
}
