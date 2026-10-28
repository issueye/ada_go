package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/adag/ada-go/internal/domain"
)

// driverName 是 modernc.org/sqlite 在 database/sql 里注册的驱动名（11 §13.2：无 cgo 的纯 Go 驱动）。
const driverName = "sqlite"

// buildURI 把本地路径转成 SQLite URI（file: 前缀 + 百分号转义 + 正斜杠）。
//
// 必须用 file: 形式才能带上 mode=ro（04 §5.7：只读连接「能只读打开时带 mode=ro」）：
// 驱动文档明确说明普通文件名形式的 mode 参数会被丢掉，变成读写打开。
// 路径里的 %、空格、# 等都要转义，否则 URI 解析会把它们当分隔符——这是 Windows 与
// 带空格用户名目录的真实坑。
func buildURI(path string, params url.Values) string {
	slashed := filepath.ToSlash(filepath.Clean(path))
	var b strings.Builder
	b.WriteString("file:")
	if !strings.HasPrefix(slashed, "/") {
		b.WriteString("/")
	}
	// 逐段转义：/ 保留，其余按 RFC3986 百分号编码。
	for i, seg := range strings.Split(slashed, "/") {
		if i > 0 {
			b.WriteString("/")
		}
		b.WriteString(url.PathEscape(seg))
	}
	if len(params) > 0 {
		b.WriteString("?")
		b.WriteString(params.Encode())
	}
	return b.String()
}

// commonConnParams 是每个连接都要带的 PRAGMA（04 §5.7「每个连接 busy_timeout=5000、
// foreign_keys=ON」；journal_size_limit 与 wal_autocheckpoint 在 SQLite 里也是连接级设置，
// 所以读连接同样带上，避免只读进程用默认值抢先做激进 checkpoint）。
func commonConnParams() url.Values {
	v := url.Values{}
	v.Set("_busy_timeout", fmt.Sprintf("%d", BusyTimeoutMS))
	v.Set("_foreign_keys", "1")
	v.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", JournalSizeLimitBytes))
	v.Add("_pragma", fmt.Sprintf("wal_autocheckpoint(%d)", WalAutoCheckpoint))
	return v
}

// writerDSN 是 Core 的唯一写连接：
//   - _journal_mode=WAL（库级持久设置，由第一个写连接落定）
//   - _synchronous=NORMAL|FULL（04 §5.7）
//   - _txlock=immediate：database/sql 的 BeginTx 直接发 BEGIN IMMEDIATE
//     （04 §5.7「写事务一律 BEGIN IMMEDIATE」），不需要手写 BEGIN 绕开连接池。
func writerDSN(cfg Config) string {
	v := commonConnParams()
	v.Set("_journal_mode", "WAL")
	v.Set("_synchronous", cfg.Synchronous)
	v.Set("_txlock", "immediate")
	return buildURI(StateDBPath(cfg.DataDir), v)
}

// readerDSN 是只读连接（CLI export/doctor、Core 的读池）：
// mode=ro 让 SQLite 拒绝任何写；query_only=ON 是第二道闸（PRAGMA 层面也拒绝写）。
// 04 §5.7 明确：只读打开要带 mode=ro，避免 WAL 只读打开对 -shm 的额外依赖问题。
func readerDSN(dataDir string) string {
	v := commonConnParams()
	v.Set("mode", "ro")
	v.Set("_query_only", "1")
	return buildURI(StateDBPath(dataDir), v)
}

// singleDSN 给「一条专用连接」用：备份（VACUUM INTO）与 checkpoint 必须独占连接，
// 不能和并发语句共享池（04 §5.7：备份用专用连接，只写目标文件、不改源库）。
func singleDSN(dataDir string) string {
	v := commonConnParams()
	v.Set("_journal_mode", "WAL")
	v.Set("_synchronous", SynchronousNormal)
	return buildURI(StateDBPath(dataDir), v)
}

// readOnlyDSNForFile 给「任意一个 db 文件」构造只读 DSN（校验备份产物、恢复前检查用）。
func readOnlyDSNForFile(path string) string {
	v := commonConnParams()
	v.Set("mode", "ro")
	v.Set("_query_only", "1")
	return buildURI(path, v)
}

// openPool 打开一个连接池并做一次 Ping 验证（失败方向：Ping 不过就直接拒绝，
// 不把「打不开库」推迟到第一条业务 SQL）。
func openPool(ctx context.Context, dsn string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开连接失败: %w", err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: 连接握手失败: %w", err)
	}
	return db, nil
}

// openSingle 打开一条独占连接（备份、checkpoint、损坏校验）。maxConns=1 保证
// 该连接不会被别的语句插队。
func openSingle(ctx context.Context, dsn string) (*sql.DB, error) {
	return openPool(ctx, dsn, 1)
}

// openReadOnlyDB 打开 CLI/Core 共用的只读连接池。
//
// 注意：只读打开要求 state.db 已存在（mode=ro 不会创建文件），不存在时返回
// workspace_missing 语义的业务错误（04 §5.7：读的人不负责建库）。
func openReadOnlyDB(ctx context.Context, dataDir string) (*sql.DB, error) {
	dbPath := StateDBPath(dataDir)
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fail(domain.CodeWorkspaceMissing, fmt.Sprintf(
			"数据目录里没有 state.db（%s）：该工作区还没有被 Core 启动过，先启动一次再运行只读命令", dbPath), err)
	}
	return openPool(ctx, readerDSN(dataDir), MaxReadConns)
}

// PragmaState 是库级/连接级 PRAGMA 的回显（供 doctor、测试与文档对齐最终取值）。
type PragmaState struct {
	JournalMode       string
	Synchronous       string
	SynchronousRaw    int
	WalAutocheckpoint int
	JournalSizeLimit  int64
	BusyTimeout       int
	ForeignKeys       bool
	QueryOnly         bool
	UserVersion       int
}

// synchronousName 把 PRAGMA synchronous 的整数回显翻译成文档里的名字。
func synchronousName(raw int) string {
	switch raw {
	case 0:
		return "OFF"
	case 1:
		return "NORMAL"
	case 2:
		return "FULL"
	case 3:
		return "EXTRA"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", raw)
	}
}

// ReadPragmas 在给定连接上读回当前 PRAGMA 取值（只读操作，不修改任何东西）。
func ReadPragmas(ctx context.Context, db *sql.DB) (PragmaState, error) {
	var p PragmaState
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&p.JournalMode); err != nil {
		return p, fmt.Errorf("sqlite: 读 journal_mode 失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&p.SynchronousRaw); err != nil {
		return p, fmt.Errorf("sqlite: 读 synchronous 失败: %w", err)
	}
	p.Synchronous = synchronousName(p.SynchronousRaw)
	if err := db.QueryRowContext(ctx, `PRAGMA wal_autocheckpoint`).Scan(&p.WalAutocheckpoint); err != nil {
		return p, fmt.Errorf("sqlite: 读 wal_autocheckpoint 失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA journal_size_limit`).Scan(&p.JournalSizeLimit); err != nil {
		return p, fmt.Errorf("sqlite: 读 journal_size_limit 失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&p.BusyTimeout); err != nil {
		return p, fmt.Errorf("sqlite: 读 busy_timeout 失败: %w", err)
	}
	var fk, qo int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		return p, fmt.Errorf("sqlite: 读 foreign_keys 失败: %w", err)
	}
	p.ForeignKeys = fk != 0
	if err := db.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&qo); err != nil {
		return p, fmt.Errorf("sqlite: 读 query_only 失败: %w", err)
	}
	p.QueryOnly = qo != 0
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&p.UserVersion); err != nil {
		return p, fmt.Errorf("sqlite: 读 user_version 失败: %w", err)
	}
	return p, nil
}

// checkIntegrity 跑 PRAGMA integrity_check（09 §10 doctor 检查项、04 §5.7 故障语义）。
// 返回第一条非 "ok" 的报告；损坏时调用方给 storage_corrupt。
func checkIntegrity(ctx context.Context, db *sql.DB) (report string, err error) {
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return "", fmt.Errorf("sqlite: integrity_check 执行失败: %w", err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", fmt.Errorf("sqlite: integrity_check 读取失败: %w", err)
		}
		parts = append(parts, s)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("sqlite: integrity_check 读取失败: %w", err)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("sqlite: integrity_check 没有返回结果")
	}
	if len(parts) == 1 && strings.EqualFold(parts[0], "ok") {
		return "ok", nil
	}
	return strings.Join(parts, "; "), fmt.Errorf("sqlite: 数据库完整性检查未通过")
}

// checkpoint 执行 wal_checkpoint(<mode>)；busy 不致命（PASSIVE 会因并发读者提前返回）。
// 返回 (是否被阻塞, 错误)。04 §5.7：导出结束立即 wal_checkpoint(PASSIVE)。
func checkpoint(ctx context.Context, db *sql.DB, mode string) (bool, error) {
	var busy, logSize, checkpointed int
	err := db.QueryRowContext(ctx, fmt.Sprintf(`PRAGMA wal_checkpoint(%s)`, mode)).Scan(&busy, &logSize, &checkpointed)
	if err != nil {
		if isBusyError(err) {
			return true, nil // PASSIVE 语义：被占用就放弃，不重试、不报错。
		}
		return false, fmt.Errorf("sqlite: wal_checkpoint(%s) 失败: %w", mode, err)
	}
	return busy != 0, nil
}
