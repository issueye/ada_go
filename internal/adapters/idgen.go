package adapters

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/ports"
	"github.com/oklog/ulid/v2"
)

// ULIDGen 是 ports.IDGen 的生产实现。
//
// id 必须可排序：entries 的唯一约束是 (thread_id, seq)，但 entry/run/id 一律用 ULID，
// 便于跨线程引用与人工调试（04 §5.7）。前缀规则固定：线程 th_、调用 call_、检查点 chk_。
// 熵源用 crypto/rand，单调计数保证同一毫秒内也严格递增。
type ULIDGen struct {
	mu  sync.Mutex
	ent *ulid.MonotonicEntropy
}

var _ ports.IDGen = (*ULIDGen)(nil)

// NewULIDGen 创建单调递增的 ULID 生成器。
func NewULIDGen() *ULIDGen {
	return &ULIDGen{ent: ulid.Monotonic(rand.Reader, 0)}
}

// NewULID 生成裸 ULID（entry id 等）。
func (g *ULIDGen) NewULID(_ context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, err := ulid.New(ulid.Timestamp(time.Now()), g.ent)
	if err != nil {
		return "", fmt.Errorf("adapters: 生成 ULID 失败: %w", err)
	}
	return id.String(), nil
}

// NewThreadID 生成 th_<workspaceHash>_<ulid>：线程 id 自带工作区前缀，
// Gateway 只解析前缀就能路由到实例（01 §4.2）。
func (g *ULIDGen) NewThreadID(ctx context.Context, workspaceHash string) (string, error) {
	if strings.TrimSpace(workspaceHash) == "" {
		return "", fmt.Errorf("adapters: 线程 id 需要工作区哈希（4.2 的 sha256(realPath)[:12]）")
	}
	id, err := g.NewULID(ctx)
	if err != nil {
		return "", err
	}
	return "th_" + workspaceHash + "_" + id, nil
}

// NewCallID 生成工具调用 id（evt 与回执都按它配对，INV-6）。
func (g *ULIDGen) NewCallID(ctx context.Context) (string, error) {
	return g.prefixed(ctx, "call_")
}

// NewRunID 生成轮次 id（runs.id）。
func (g *ULIDGen) NewRunID(ctx context.Context) (string, error) {
	return g.NewULID(ctx)
}

// NewCheckpointID 生成检查点 id（仅 Descriptor.Rollback=true 时分配）。
func (g *ULIDGen) NewCheckpointID(ctx context.Context) (string, error) {
	return g.prefixed(ctx, "chk_")
}

// NewMessageID 生成投影消息 id（evt.message.delta 的 messageId）。
func (g *ULIDGen) NewMessageID(ctx context.Context) (string, error) {
	return g.NewULID(ctx)
}

// NewInteractionID 生成交互请求 id；Broker 用它 + threadID 做幂等键（03 §5.6）。
func (g *ULIDGen) NewInteractionID(ctx context.Context) (string, error) {
	return g.NewULID(ctx)
}

func (g *ULIDGen) prefixed(ctx context.Context, prefix string) (string, error) {
	id, err := g.NewULID(ctx)
	if err != nil {
		return "", err
	}
	return prefix + id, nil
}
