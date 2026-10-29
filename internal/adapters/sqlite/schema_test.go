package sqlite

import (
	"context"
	"database/sql"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/domain"
)

// TestMigrateEmptyToLatest 验证空库→最新版本：04 §5.7 的 DDL 全部对象存在，
// user_version 落在 SchemaVersion()。
func TestMigrateEmptyToLatest(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	st := openStore(t, testConfig(t, ws, dataDir))
	ctx := context.Background()

	v, err := st.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != SchemaVersion() {
		t.Fatalf("user_version=%d，期望 %d", v, SchemaVersion())
	}
	if err := verifySchemaShape(ctx, st.write); err != nil {
		t.Fatalf("schema 形状校验失败: %v", err)
	}
	report, err := st.CheckIntegrity(ctx)
	if err != nil {
		t.Fatalf("新库 integrity_check 失败: %v", err)
	}
	if report != "ok" {
		t.Fatalf("integrity_check 返回 %q", report)
	}
}

// TestMigrateIsIdempotent 验证同一版本重复打开不再执行迁移（幂等，09 §10 升级）。
func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	cfg := testConfig(t, ws, dataDir)
	st := openStore(t, cfg)
	th := mustThread(t, st, "keep-me")
	if err := st.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 再开一次：数据必须还在，迁移不重复执行（不报错、不改结构）。
	st2 := openStore(t, cfg)
	got, err := st2.GetThread(context.Background(), th.ID)
	if err != nil {
		t.Fatalf("重启后读线程失败: %v", err)
	}
	if got.Title != "keep-me" {
		t.Fatalf("线程标题漂移: %q", got.Title)
	}
}

// TestMigrateFromOldVersion 验证「旧版本库→升级」的真实代码路径：
// 先在版本 1 上建库并写数据，再用「追加了版本 2」的迁移表重新打开，
// 断言 user_version 前进、老数据完好、新版本对象出现。
//
// 版本 2 是测试专用的临时迁移（生产 migrations 只有 04 §5.7 冻结的版本 1），
// 这样既走了多版本升级路径，又不给生产发明表结构。
//
// 不并行原因：它临时替换包级 migrations 变量，与其它用例共享进程状态。
func TestMigrateFromOldVersion(t *testing.T) {
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()

	st := openStore(t, testConfig(t, ws, dataDir))
	th := mustThread(t, st, "升级前的线程")
	e := appendMessage(t, st, th.ID, "", "升级前的 entry")
	if err := st.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	orig := migrations
	t.Cleanup(func() { migrations = orig })
	migrations = append(append([]migration{}, orig...), migration{
		Version:   orig[0].Version + 1,
		Statement: `CREATE TABLE m1_probe(id TEXT PRIMARY KEY, note TEXT NOT NULL);`,
		Note:      "测试专用的版本 2（验证升级路径，不进生产）",
	})

	st2 := openStore(t, testConfig(t, ws, dataDir))
	v, err := st2.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if want := orig[0].Version + 1; v != want || v != SchemaVersion() {
		t.Fatalf("升级后 user_version=%d，期望 %d", v, want)
	}
	got, err := st2.GetThread(ctx, th.ID)
	if err != nil {
		t.Fatalf("升级后读线程失败: %v", err)
	}
	if got.Title != "升级前的线程" || got.HeadEntryID != e.ID {
		t.Fatalf("升级后线程状态漂移：%+v", got)
	}
	if !tableExists(t, ctx, StateDBPath(dataDir), "m1_probe") {
		t.Fatalf("版本 2 的表没建出来：迁移循环没有执行待办版本")
	}
}

// TestMigrateRefusesNewerVersion 验证「库比二进制新」拒绝打开（不降级、不改写别人的库）。
//
// 不并行原因：临时替换包级 migrations。
func TestMigrateRefusesNewerVersion(t *testing.T) {
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// 直接把库推到版本 99：模拟「未来二进制的库」。
	db, err := openPool(ctx, writerDSN(Config{Synchronous: SynchronousNormal, DataDir: dataDir}), 1)
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 99`); err != nil {
		t.Fatalf("推版本号失败: %v", err)
	}
	_ = db.Close()

	if _, err := OpenCore(ctx, testConfig(t, ws, dataDir)); err == nil {
		t.Fatalf("库版本 99 高于二进制，竟然启动了")
	} else {
		de, ok := domain.AsError(err)
		if !ok || de.Code != domain.CodeStorageCorrupt {
			t.Fatalf("错误码不对：%v", err)
		}
	}
}

// TestMigrateFailureRollsBack 验证迁移失败：失败的那一步整体回滚、不写它的 user_version、
// 并且拒绝启动（04 §5.7「失败回滚并拒绝启动」）。
//
// 语义要点：每一步是独立事务，所以「上一步已提交的版本」不会被下一步的失败带走 ——
// 库停在最后一个自洽版本上，下次启动仍能正常打开。
//
// 不并行原因：临时替换包级 migrations。
func TestMigrateFailureRollsBack(t *testing.T) {
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()

	orig := migrations
	t.Cleanup(func() { migrations = orig })
	migrations = append(append([]migration{}, orig...), migration{
		Version: orig[0].Version + 1,
		Statement: `CREATE TABLE half_built(id TEXT PRIMARY KEY);
CREATE TABLE half_built(id TEXT PRIMARY KEY);`, // 第二条必然失败
		Note: "测试用的失败迁移",
	})

	_, err := OpenCore(ctx, testConfig(t, ws, dataDir))
	if err == nil {
		t.Fatalf("迁移失败却启动成功了")
	}
	de, ok := domain.AsError(err)
	if !ok {
		t.Fatalf("迁移失败的错误不是业务错误: %T %v", err, err)
	}
	if de.Code != domain.CodeStorageCorrupt {
		t.Fatalf("迁移失败码=%d，期望 %d storage_corrupt（拒绝启动）", de.Code, domain.CodeStorageCorrupt)
	}
	if de.Diag == "" {
		t.Fatalf("迁移失败必须带诊断码")
	}

	v, err := readVersionOfFile(ctx, StateDBPath(dataDir))
	if err != nil {
		t.Fatalf("读库版本失败: %v", err)
	}
	if v != orig[0].Version {
		t.Fatalf("user_version=%d，期望停在已成功的版本 %d：失败的那一步不能写新版本号", v, orig[0].Version)
	}
	if tableExists(t, ctx, StateDBPath(dataDir), "half_built") {
		t.Fatalf("失败的迁移残留了 half_built 表：事务回滚没生效")
	}

	// 库仍然可用：用原始迁移表重新打开，落在版本 1。
	migrations = orig
	st := openStore(t, testConfig(t, ws, dataDir))
	if got, err := st.Version(ctx); err != nil || got != orig[0].Version {
		t.Fatalf("回滚后重新打开：user_version=%d err=%v，期望 %d", got, err, orig[0].Version)
	}
}

// TestMigrateFirstVersionFailureLeavesNoSchema 验证最坏情况：第一步就失败时，
// 库里不能出现「版本号已写、对象缺失」的组合（否则下次启动会误判为已迁移）。
//
// 不并行原因：临时替换包级 migrations。
func TestMigrateFirstVersionFailureLeavesNoSchema(t *testing.T) {
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()

	orig := migrations
	t.Cleanup(func() { migrations = orig })
	migrations = []migration{{
		Version: orig[0].Version,
		Statement: `CREATE TABLE threads(id TEXT PRIMARY KEY);
CREATE TABLE threads(id TEXT PRIMARY KEY);`,
		Note: "第一步就失败的迁移",
	}}

	if _, err := OpenCore(ctx, testConfig(t, ws, dataDir)); err == nil {
		t.Fatalf("第一步迁移失败却启动成功了")
	}
	if fileExists(StateDBPath(dataDir)) {
		v, err := readVersionOfFile(ctx, StateDBPath(dataDir))
		if err == nil && v != 0 {
			t.Fatalf("迁移第一步失败但 user_version=%d：版本号被写进去了", v)
		}
		if err == nil && tableExists(t, ctx, StateDBPath(dataDir), "threads") {
			t.Fatalf("迁移第一步失败但残留 threads 表：回滚没生效")
		}
	}
}

func readVersionOfFile(ctx context.Context, path string) (int, error) {
	db, err := openSingle(ctx, readOnlyDSNForFile(path))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return CurrentVersion(db)
}

func tableExists(t *testing.T, ctx context.Context, path, table string) bool {
	t.Helper()
	db, err := openSingle(ctx, readOnlyDSNForFile(path))
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
		t.Fatalf("查表失败: %v", err)
	}
	return n > 0
}

// TestCurrentVersionDoesNotMigrate 验证 doctor 语义：CurrentVersion 只报告版本、不迁移
// （04 §5.7「doctor 只报告版本，不迁移」），并且 OpenReadOnly 遇到版本不符是报错而不是补迁移。
func TestCurrentVersionDoesNotMigrate(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	dbPath := StateDBPath(dataDir)

	// 手工造一个「有文件、没结构」的库（模拟 Core 从没跑过迁移）。
	wdb, err := openPool(ctx, writerDSN(Config{Synchronous: SynchronousNormal, DataDir: dataDir}), 1)
	if err != nil {
		t.Fatalf("建裸库失败: %v", err)
	}
	if _, err := wdb.ExecContext(ctx, `CREATE TABLE only_for_probe(x INTEGER)`); err != nil {
		t.Fatalf("建探针表失败: %v", err)
	}
	v, err := CurrentVersion(wdb)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if v != 0 {
		t.Fatalf("CurrentVersion 报告 %d，期望 0（它不改任何东西）", v)
	}
	if err := wdb.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 只读句柄必须拒绝这种库，而不是替 Core 迁移。
	if _, err := OpenReadOnly(ctx, Config{Workspace: ws, DataDir: dataDir}); err == nil {
		t.Fatalf("OpenReadOnly 对 user_version=0 的库竟然成功了：它不该做迁移")
	}
	// 探针表还在、版本号还是 0：证明只读路径真的没动结构。
	rdb, err := openPool(ctx, readOnlyDSNForFile(dbPath), 1)
	if err != nil {
		t.Fatalf("重开裸库失败: %v", err)
	}
	defer rdb.Close()
	if v, err := CurrentVersion(rdb); err != nil || v != 0 {
		t.Fatalf("只读路径把 user_version 改成了 %d（err=%v）", v, err)
	}
	if n := 0; countTable(t, ctx, rdb, "only_for_probe") == n {
		t.Fatalf("只读路径把探针表删了：迁移/结构变更不该发生")
	}
}

func countTable(t *testing.T, ctx context.Context, db *sql.DB, name string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("查表 %s 失败: %v", name, err)
	}
	return n
}

// TestReadOnlyDoesNotCreateOrUpdate 验证只读句柄既不会建库也不会顺手迁移。
func TestReadOnlyDoesNotCreateOrUpdate(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()

	if _, err := OpenReadOnly(ctx, Config{Workspace: ws, DataDir: dataDir}); err == nil {
		t.Fatalf("state.db 不存在时只读打开竟然成功了")
	} else if fileExists(StateDBPath(dataDir)) {
		t.Fatalf("只读打开失败却把 state.db 建出来了：%s", StateDBPath(dataDir))
	}

	// Core 跑过一次之后，只读句柄能打开且报出版本，但不改结构。
	st := openStore(t, testConfig(t, ws, dataDir))
	wantVersion, err := st.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, err := OpenReadOnly(ctx, Config{Workspace: ws, DataDir: dataDir})
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer r.Close()
	got, err := r.Version(ctx)
	if err != nil {
		t.Fatalf("只读 Version: %v", err)
	}
	if got != wantVersion {
		t.Fatalf("只读句柄把版本改了：%d → %d", wantVersion, got)
	}
	// 只读连接上的写必须失败（query_only + mode=ro 双保险）。
	if _, err := r.read.ExecContext(ctx, `INSERT INTO threads(id, workspace, title, created_at, updated_at)
VALUES('th_bad','w','bad',1,1)`); err == nil {
		t.Fatalf("只读连接竟然写成功了")
	} else if !isReadOnlyError(err) && !isBusyError(err) {
		t.Logf("只读写失败的错误形态：%v", err)
	}
}

// TestPragmasMatchDesign 断言 04 §5.7 的连接级/库级 PRAGMA 最终取值。
func TestPragmasMatchDesign(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	st := openStore(t, testConfig(t, ws, dataDir))
	ctx := context.Background()

	p, err := st.WritePragmas(ctx)
	if err != nil {
		t.Fatalf("WritePragmas: %v", err)
	}
	if p.JournalMode != "wal" {
		t.Errorf("journal_mode=%q，期望 wal", p.JournalMode)
	}
	if p.Synchronous != SynchronousNormal {
		t.Errorf("synchronous=%s，期望 %s", p.Synchronous, SynchronousNormal)
	}
	if p.BusyTimeout != BusyTimeoutMS {
		t.Errorf("busy_timeout=%d，期望 %d", p.BusyTimeout, BusyTimeoutMS)
	}
	if !p.ForeignKeys {
		t.Errorf("foreign_keys 必须是 ON")
	}
	if p.WalAutocheckpoint != WalAutoCheckpoint {
		t.Errorf("wal_autocheckpoint=%d，期望 %d", p.WalAutocheckpoint, WalAutoCheckpoint)
	}
	if p.JournalSizeLimit != JournalSizeLimitBytes {
		t.Errorf("journal_size_limit=%d，期望 %d", p.JournalSizeLimit, JournalSizeLimitBytes)
	}

	// 只读连接：query_only 必须 ON，mode=ro 让它连写都开不了。
	rp, err := st.Reader.Pragmas(ctx)
	if err != nil {
		t.Fatalf("只读 Pragmas: %v", err)
	}
	if !rp.QueryOnly {
		t.Errorf("只读连接的 query_only 必须是 ON（04 §5.7 CLI 只读）")
	}
	if rp.BusyTimeout != BusyTimeoutMS {
		t.Errorf("只读连接 busy_timeout=%d，期望 %d", rp.BusyTimeout, BusyTimeoutMS)
	}

	// -wal/-shm 应该已经出现（WAL 生效的直接证据）。
	if !fileExists(st.WALPath()) && !fileExists(st.SHMPath()) {
		t.Logf("当前没有 -wal/-shm 文件（可能已经 checkpoint 干净）：%s", dataDir)
	}
}

// TestSynchronousFullIsHonoured 验证 FULL 档可配（04 §5.7 掉电安全选项）。
func TestSynchronousFullIsHonoured(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	cfg := testConfig(t, ws, dataDir)
	cfg.Synchronous = SynchronousFull
	st := openStore(t, cfg)
	p, err := st.WritePragmas(context.Background())
	if err != nil {
		t.Fatalf("WritePragmas: %v", err)
	}
	if p.Synchronous != SynchronousFull {
		t.Fatalf("synchronous=%s，期望 %s", p.Synchronous, SynchronousFull)
	}
}

// TestRejectUnknownSynchronous 验证非法同步档直接拒绝打开（不静默降级成 NORMAL）。
func TestRejectUnknownSynchronous(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	cfg := testConfig(t, ws, dataDir)
	cfg.Synchronous = "OFF"
	if _, err := OpenCore(context.Background(), cfg); err == nil {
		t.Fatalf("synchronous=OFF 竟然被接受了（掉电会丢最后提交窗口，必须拒绝）")
	}
}

// TestForeignKeysEnforced 验证 foreign_keys=ON 真在挡脏数据（entry 必须挂在存在的线程上）。
func TestForeignKeysEnforced(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	st := openStore(t, testConfig(t, ws, dataDir))
	ctx := context.Background()

	_, err := st.AppendEntry(ctx, portsEntryFor("th_not_exist", "", `{"text":"hi"}`))
	if err == nil {
		t.Fatalf("往不存在的线程追加 entry 竟然成功了")
	}
	if _, ok := domain.AsError(err); !ok {
		t.Fatalf("错误必须是可判定的业务错误，得到 %T", err)
	}
}

// TestDependencyDirection 用 go/parser 精确检查本包非测试文件的 import：
// 只允许标准库 + domain/ports/diag + 已锁定的第三方库（设计 13 的依赖方向）。
// 故意不做字符串扫描——注释里写「不 import internal/engine」是文档，不是依赖。
func TestDependencyDirection(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	// workspacepath 是只依赖 domain 的叶子工具（路径归一化），
	// 不构成对上层/适配器层的反向依赖。
	allowedInternal := []string{
		"github.com/adag/ada-go/internal/domain",
		"github.com/adag/ada-go/internal/ports",
		"github.com/adag/ada-go/internal/diag",
		"github.com/adag/ada-go/internal/workspacepath",
	}
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		pf, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", f, err)
		}
		for _, imp := range pf.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			seen++
			if !strings.HasPrefix(path, "github.com/adag/ada-go/internal/") {
				continue // 标准库与第三方
			}
			ok := false
			for _, a := range allowedInternal {
				if path == a {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s import 了越界的内部包 %q", f, path)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("一个 import 都没抓到：测试跑在了错误的目录")
	}
}

// TestAppendOnlyNoUpdateOnEntries 守住 04 §5.7「entry 一旦写入不可修改」：
// 生产代码里不允许出现 UPDATE entries。
func TestAppendOnlyNoUpdateOnEntries(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s: %v", f, err)
		}
		text := string(raw)
		for _, pat := range []string{"UPDATE entries", "DELETE FROM entries"} {
			if strings.Contains(text, pat) {
				t.Errorf("%s 出现 %q：entries 是 append-only（04 §5.7）", f, pat)
			}
		}
		// -wal/-shm 只能在「停 Core 的恢复路径」里删（backup.go 的 Restore）。
		if f != "backup.go" && strings.Contains(text, `removeIfExists(`) {
			t.Errorf("%s 调用了 removeIfExists：删除 -wal/-shm 只允许发生在停 Core 后的恢复路径（04 §5.7）", f)
		}
	}
}
