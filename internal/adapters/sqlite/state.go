package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// 本文件实现 ports.StateStore（04 §5.7 plugin_state 表）。
//
// 命名空间约定照 ports/state.go：调用方把 `t:<threadID>/<key>` 之类的组合放进 Key，
// 存储层主键是 (plugin_id, key)，本包不猜业务前缀、也不做任何前缀改写。

// Get 读取键值；found=false 表示不存在（05 §6.3 ctx.state.get 返回 null）。
// 读走 WAL 只读连接（04 §5.7）。
func (r *Reader) Get(ctx context.Context, key ports.StateKey) (json.RawMessage, bool, error) {
	if key.PluginID == "" {
		return nil, false, invalidInput("state 需要 pluginId")
	}
	var raw []byte
	if err := r.read.QueryRowContext(ctx,
		`SELECT value FROM plugin_state WHERE plugin_id = ? AND key = ?`, key.PluginID, key.Key).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("sqlite: 读插件状态失败: %w", err)
	}
	return json.RawMessage(raw), true, nil
}

// Set 写入键值（upsert）；value 必须已 JSON 编码且不超过 1MB（ports/state.go、05 §6.3）。
// 写进入唯一写队列（04 §5.7）。
func (st *Store) Set(ctx context.Context, key ports.StateKey, value json.RawMessage) error {
	if key.PluginID == "" {
		return invalidInput("state 需要 pluginId")
	}
	if len(value) > MaxStateValueBytes {
		return fail(domain.CodeToolInvalidArgs, fmt.Sprintf(
			"插件状态值 %d 字节超过上限 %d（05 §6.3：编码后 1MB），已拒绝写入", len(value), MaxStateValueBytes), nil)
	}
	if len(value) == 0 || !json.Valid(value) {
		return invalidInput("插件状态值必须是合法 JSON（可以先存 null）")
	}
	return st.w.submit(ctx, "state.set", func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO plugin_state(plugin_id, key, value, updated_at) VALUES(?, ?, ?, ?)
ON CONFLICT(plugin_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key.PluginID, key.Key, []byte(value), ms(st.cfg.Now()))
		if err != nil {
			return fmt.Errorf("sqlite: 写插件状态失败: %w", err)
		}
		return nil
	})
}

// Delete 删除键；幂等（不存在不报错）。
func (st *Store) Delete(ctx context.Context, key ports.StateKey) error {
	if key.PluginID == "" {
		return invalidInput("state 需要 pluginId")
	}
	return st.w.submit(ctx, "state.delete", func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM plugin_state WHERE plugin_id = ? AND key = ?`, key.PluginID, key.Key); err != nil {
			return fmt.Errorf("sqlite: 删插件状态失败: %w", err)
		}
		return nil
	})
}

// ListKeys 列举指定前缀的键（按 key 稳定排序，04 §5.7 plugin_state 主键顺序）。
func (r *Reader) ListKeys(ctx context.Context, pluginID, prefix string) ([]string, error) {
	if pluginID == "" {
		return nil, invalidInput("state 需要 pluginId")
	}
	rows, err := r.read.QueryContext(ctx, `
SELECT key FROM plugin_state WHERE plugin_id = ? AND key >= ? AND key < ? ORDER BY key ASC`,
		pluginID, prefix, prefixUpperBound(prefix))
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列插件状态键失败: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("sqlite: 读取插件状态键失败: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// prefixUpperBound 给出「大于所有 prefix 前缀键」的最小串（配合 BLOB TEXT 的字典序范围扫描，
// 避免 LIKE 的全表扫描）。空 prefix 时用不可能出现的上界。
func prefixUpperBound(prefix string) string {
	if prefix == "" {
		return "\xff\xff"
	}
	if strings.HasSuffix(prefix, "\xff") {
		return prefix + "\xff"
	}
	next := []byte(prefix)
	for i := len(next) - 1; i >= 0; i-- {
		if next[i] < 0xff {
			next[i]++
			return string(next[:i+1])
		}
	}
	return prefix + "\xff"
}
