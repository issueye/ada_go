package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SchemaVersion 是本二进制期望的 schema 版本（PRAGMA user_version）。
//
// 规则（04 §5.7 迁移）：新增 migration 只追加、已发布版本号与内容不可改；
// 版本号单调递增，高于本二进制的库一律拒绝打开（不降级、不改写别人的库）。
func SchemaVersion() int { return len(migrations) }

// schema1DDL 是 04 §5.7 的 DDL 原文（threads/entries/两个索引 + runs/interactions/
// plugin_state/plugin_meta/usage/audit）。逐字照文档，不增删列、不加触发器：
// append-only 与 seq 唯一性由代码 + entries_seq 唯一索引共同保证。
const schema1DDL = `
CREATE TABLE threads (
  id            TEXT PRIMARY KEY,
  workspace     TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  head_entry_id TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  archived      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE entries (
  id         TEXT PRIMARY KEY,
  thread_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  parent_id  TEXT,
  seq        INTEGER NOT NULL,
  kind       TEXT NOT NULL, -- message|compaction|branch_summary|model_change|plugin_state|custom
  role       TEXT,          -- user|assistant|tool|system
  payload    BLOB NOT NULL, -- JSON
  tokens     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX entries_seq ON entries(thread_id, seq);
CREATE INDEX entries_parent ON entries(thread_id, parent_id);

CREATE TABLE runs (
  id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, status TEXT NOT NULL,
  started_at INTEGER NOT NULL, ended_at INTEGER,
  error_code TEXT, usage_json TEXT
);

CREATE TABLE interactions (
  id TEXT PRIMARY KEY, thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  run_id TEXT NOT NULL, call_id TEXT, plugin_id TEXT NOT NULL,
  kind TEXT NOT NULL, -- confirm|select|input
  title TEXT NOT NULL, body TEXT NOT NULL, options_json TEXT,
  status TEXT NOT NULL, -- pending|accepted|denied|cancelled|timeout|unavailable
  response_json TEXT, created_at INTEGER NOT NULL, resolved_at INTEGER, expires_at INTEGER
);
CREATE INDEX interactions_pending ON interactions(thread_id, status);

CREATE TABLE plugin_state (
  plugin_id TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL,
  updated_at INTEGER NOT NULL, PRIMARY KEY(plugin_id, key)
);

CREATE TABLE plugin_meta (
  id TEXT PRIMARY KEY, version TEXT NOT NULL, enabled INTEGER NOT NULL,
  fingerprint TEXT NOT NULL, installed_at INTEGER NOT NULL, last_error TEXT
);

CREATE TABLE usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT, thread_id TEXT NOT NULL,
  provider TEXT NOT NULL, model TEXT NOT NULL,
  input_tokens INTEGER, output_tokens INTEGER, cache_tokens INTEGER,
  cost_micros INTEGER, created_at INTEGER NOT NULL
);

CREATE TABLE audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL,
  kind TEXT NOT NULL, thread_id TEXT, plugin_id TEXT, detail_json TEXT
);
`

// migration 是一次版本升级：Version 是执行完后的 user_version。
type migration struct {
	// Version 是目标版本号（1 起）。
	Version int
	// Statement 是该版本需要的 DDL（多语句，按分号切分依次执行）。
	Statement string
	// Note 说明这一步的来源（设计文档章节），便于 doctor 输出与评审。
	Note string
}

// migrations 是唯一真源：索引 0 = 版本 1。
//
// M1-B 只有版本 1（04 §5.7 冻结的那一份 DDL）。后续里程碑追加条目时：
// 只能 append，不能修改已有条目；每一步都必须能在事务里回滚。
var migrations = []migration{
	{Version: 1, Statement: schema1DDL, Note: "04 §5.7 冻结的初始 schema（threads/entries/runs/interactions/plugin_state/plugin_meta/usage/audit）"},
}

// CurrentVersion 读取 PRAGMA user_version，不改任何东西（doctor 用：只报告版本、不迁移，
// 04 §5.7）。对空库返回 0。
func CurrentVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("sqlite: 读取 user_version 失败: %w", err)
	}
	return v, nil
}

// migrate 将库升级到 SchemaVersion()。
//
// 只有持锁的 Core 在启动时调用（04 §5.7）。执行顺序：
//  1. 读 user_version；高于本二进制支持的版本 → 拒绝（storage_corrupt）。
//  2. 逐个待执行版本：BEGIN IMMEDIATE → 该版本全部 DDL → PRAGMA user_version=N → COMMIT。
//     任一步失败即 ROLLBACK，user_version 保持原值（迁移失败不会留下半个 schema），
//     并向上返回错误让调用方拒绝启动。
//
// DDL 必须在事务内：SQLite 支持事务内 CREATE TABLE/INDEX，所以「失败回滚」是真实的，
// 不是靠删文件模拟。
func migrate(ctx context.Context, db *sql.DB) (applied []int, err error) {
	cur, err := CurrentVersion(db)
	if err != nil {
		return nil, storageCorrupt(err.Error(), err)
	}
	if cur > SchemaVersion() {
		return nil, storageCorrupt(fmt.Sprintf(
			"数据库 schema 版本 %d 高于当前二进制支持的 %d：不要用旧二进制打开新库（04 §5.7 升级只向前）",
			cur, SchemaVersion()), nil)
	}
	if cur == SchemaVersion() {
		return nil, nil
	}

	for _, m := range migrations {
		if m.Version <= cur {
			continue
		}
		tx, err := db.BeginTx(ctx, nil) // 写连接池 DSN 带 _txlock=immediate → BEGIN IMMEDIATE
		if err != nil {
			return applied, storageCorrupt(fmt.Sprintf("迁移到版本 %d 失败（开启事务）: %v", m.Version, err), err)
		}
		// 失败即回滚：事务内的 CREATE 与 PRAGMA user_version 一起消失，
		// 库里不会留下「半个 schema」或「新的版本号 + 旧的表」。
		for _, stmt := range splitStatements(m.Statement) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return applied, storageCorrupt(fmt.Sprintf(
					"迁移到版本 %d 失败，已回滚（user_version 保持 %d，不写新版本）: %v", m.Version, cur, err), err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
			_ = tx.Rollback()
			return applied, storageCorrupt(fmt.Sprintf("迁移到版本 %d 失败（写 user_version），已回滚: %v", m.Version, err), err)
		}
		if err := tx.Commit(); err != nil {
			return applied, storageCorrupt(fmt.Sprintf("迁移到版本 %d 提交失败（已回滚标记）: %v", m.Version, err), err)
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

// verifySchemaShape 检查关键表是否存在（版本号对了但表被人为删过也判损坏，04 §5.7 故障语义）。
func verifySchemaShape(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table','index')`)
	if err != nil {
		return storageCorrupt("读取 sqlite_master 失败: "+err.Error(), err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return storageCorrupt("读取 sqlite_master 失败: "+err.Error(), err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return storageCorrupt("读取 sqlite_master 失败: "+err.Error(), err)
	}
	want := []string{
		"threads", "entries", "entries_seq", "entries_parent", "runs",
		"interactions", "interactions_pending", "plugin_state", "plugin_meta", "usage", "audit",
	}
	var missing []string
	for _, w := range want {
		if !found[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		v, _ := CurrentVersion(db)
		return storageCorrupt(fmt.Sprintf(
			"user_version=%d 但缺少对象 %s：schema 与版本声明不一致（04 §5.7 迁移失败方向：拒绝启动）",
			v, strings.Join(missing, ", ")), nil)
	}
	return nil
}

// splitStatements 按分号切分 DDL：跳过整行注释、剥掉行尾 -- 注释、忽略字符串里的分号。
// 本包的 DDL 没有触发器/自定义函数，这套规则足够（也不为此引入 SQL 解析器依赖）。
func splitStatements(script string) []string {
	var out []string
	var cur strings.Builder
	inStr := false
	for _, line := range strings.Split(script, "\n") {
		// 行尾注释处理：不在字符串里时遇到 -- 就截断该行剩余部分。
		if !inStr {
			if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
		}
		for i := 0; i < len(line); i++ {
			c := line[i]
			if c == '\'' {
				inStr = !inStr
			}
			if c == ';' && !inStr {
				if s := strings.TrimSpace(cur.String()); s != "" {
					out = append(out, s)
				}
				cur.Reset()
				continue
			}
			cur.WriteByte(c)
		}
		cur.WriteByte('\n')
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}
