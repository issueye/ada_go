package sqlite

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// 存储层资源限制的最终取值（04 §5.7、09 §9.5）。集中在这里，方便 doctor/测试与文档对齐。
const (
	// BusyTimeoutMS 是每个连接的 PRAGMA busy_timeout（04 §5.7：5000）。
	BusyTimeoutMS = 5000
	// SynchronousNormal 是默认同步级别（04 §5.7：NORMAL 保证崩溃一致性）。
	// 需要更强掉电持久性时把 Config.Synchronous 设为 "FULL"。
	SynchronousNormal = "NORMAL"
	// SynchronousFull 是掉电安全档（04 §5.7 明示可选）。
	SynchronousFull = "FULL"
	// WalAutoCheckpoint 是库级 wal_autocheckpoint（页数，04 §5.7：1000）。
	WalAutoCheckpoint = 1000
	// JournalSizeLimitBytes 是 journal_size_limit（04 §5.7：64MB）。
	JournalSizeLimitBytes = 64 << 20

	// MaxBusyRetries 是 SQLITE_BUSY/SQLITE_LOCKED 的指数退避重试上限（04 §5.7：最多 3 次）。
	MaxBusyRetries = 3
	// BusyBackoffBase 是退避基数：第 n 次重试等待 BusyBackoffBase<<(n-1)，即 50/100/200ms。
	BusyBackoffBase = 50 * time.Millisecond

	// DefaultWriterQueueSize 是单写者队列容量（04 §5.7：有界队列）。
	// 队列满时 entry/interaction/audit 反压等待；usage 走窗口合并，不占队列。
	DefaultWriterQueueSize = 512
	// DefaultUsageFlushWindow 是 usage 合并窗口（04 §5.7：usage 可按窗口合并）。
	// 窗口只影响 usage 行的聚合粒度，不影响 entry/交互的持久性。
	DefaultUsageFlushWindow = 250 * time.Millisecond
	// DefaultCloseTimeout 是 Close 排空队列与等在途写的默认预算。
	DefaultCloseTimeout = 5 * time.Second
	// MaxReadConns 是读连接池上限（WAL 下读写不互斥，04 §5.7）。
	MaxReadConns = 4
	// MaxPathDepth 是回溯 parent_id 链的行数上限：防御式，避免畸形数据把查询拖死。
	MaxPathDepth = 100_000
	// MaxStateValueBytes 是 plugin_state 单值上限（ports/state.go：编码后 1MB，同 05 §6.3）。
	MaxStateValueBytes = 1 << 20
	// MaxPayloadBytes 是 entry payload 上限（与 08 §8.1 的 RPC 单条 2MB 同源）。
	MaxPayloadBytes = 2 << 20
	// DefaultThreadListLimit 是 thread.list 的默认分页尺寸（08 §8.2）。
	DefaultThreadListLimit = 50
	// MaxThreadListLimit 是 thread.list 的分页上限。
	MaxThreadListLimit = 200
)

// Config 是打开一个工作区数据库所需的全部参数。
//
// 只有 Workspace 必填；其余留空取默认值。DataDir 留空时用 DefaultDataDir(Workspace)
// （04 §5.7 的 ~/.adago/workspaces/<hash>）；测试一律显式传 t.TempDir() 下的目录，
// 绝不依赖 ~/.adago（M1-B 硬约束）。
type Config struct {
	// Workspace 是工作区根路径，用于身份键与 workspace 列（01 §4.2）。
	Workspace string
	// DataDir 是本工作区数据目录（state.db 与 locks/ 的父目录）。
	DataDir string
	// Synchronous 为 "NORMAL"（默认）或 "FULL"（04 §5.7）。其它值拒绝打开。
	Synchronous string
	// WriterQueueSize <= 0 时取 DefaultWriterQueueSize。
	WriterQueueSize int
	// UsageFlushWindow <= 0 时取 DefaultUsageFlushWindow。
	UsageFlushWindow time.Duration
	// CloseTimeout <= 0 时取 DefaultCloseTimeout。
	CloseTimeout time.Duration
	// VerifyOnRecovery 控制「打开时存在 -wal（上次非正常退出）」时是否跑
	// PRAGMA integrity_check；nil 按 true，失败即 storage_corrupt 拒绝启动（04 §5.7）。
	VerifyOnRecovery *bool
	// Now 是时间源（可测试）。nil 时取 time.Now().UTC()；一切 created_at 用 UnixMilli。
	Now func() time.Time
	// NewID 是 entry/run/interaction id 源（ULID，04 §5.7）。nil 时用 crypto/rand ULID。
	// 线程 id 不在这里生成：ports.SessionStore 约定由 IDGen 生成 th_<hash>_<ulid>（01 §4.2）。
	NewID func(ctx context.Context) (string, error)
}

// normalized 返回补齐默认值后的副本，并校验取值。
func (c Config) normalized() (Config, error) {
	if c.Workspace == "" {
		return c, fmt.Errorf("sqlite: Config.Workspace 不能为空")
	}
	if c.Synchronous == "" {
		c.Synchronous = SynchronousNormal
	}
	switch c.Synchronous {
	case SynchronousNormal, SynchronousFull, "EXTRA":
	default:
		return c, fmt.Errorf("sqlite: 非法 synchronous %q（只允许 NORMAL/FULL/EXTRA，04 §5.7）", c.Synchronous)
	}
	if c.DataDir == "" {
		dir, err := DefaultDataDir(c.Workspace)
		if err != nil {
			return c, err
		}
		c.DataDir = dir
	}
	if c.WriterQueueSize <= 0 {
		c.WriterQueueSize = DefaultWriterQueueSize
	}
	if c.UsageFlushWindow <= 0 {
		c.UsageFlushWindow = DefaultUsageFlushWindow
	}
	if c.CloseTimeout <= 0 {
		c.CloseTimeout = DefaultCloseTimeout
	}
	if c.VerifyOnRecovery == nil {
		yes := true
		c.VerifyOnRecovery = &yes
	}
	if c.Now == nil {
		c.Now = func() time.Time { return time.Now().UTC() }
	}
	if c.NewID == nil {
		c.NewID = newULID
	}
	return c, nil
}

// newULID 是默认 id 源：crypto/rand + ULID（可排序，04 §5.7）。
func newULID(_ context.Context) (string, error) {
	id, err := ulid.New(ulid.Timestamp(time.Now()), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("sqlite: 生成 ULID 失败: %w", err)
	}
	return id.String(), nil
}
