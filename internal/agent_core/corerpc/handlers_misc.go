package corerpc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/common/domain"
)

// ---- skill（6.7：装配期注入 name/description/path，正文由模型用 read_file 读取）----

// skillSummaryJSON 把一条技能元数据编成 spec 的 SkillSummary（含绝对路径，不含正文）。
func skillSummaryJSON(m domain.SkillMeta) map[string]any {
	out := map[string]any{
		"name":        m.Name,
		"description": m.Description,
		"origin":      m.Origin,
	}
	if m.Path != "" {
		out["path"] = m.Path
	}
	if m.PluginID != "" {
		out["pluginId"] = m.PluginID
	}
	return out
}

// handleSkillList 列出当前装配的技能（插件贡献 + 工作区，同名工作区优先）；
// conflicts 是同名冲突的技能名（装配期已记警告，这里再暴露给界面）。
func (s *Server) handleSkillList(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct{}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("skill.list: " + err.Error())
	}
	metas, conflicts := s.sess.SkillSummaries()
	skills := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		skills = append(skills, skillSummaryJSON(m))
	}
	out := map[string]any{"skills": skills}
	if len(conflicts) > 0 {
		out["conflicts"] = conflicts
	}
	return out, nil
}

// ---- change（M5：检查点账目与回滚，08 §8.2）----

// handleChangeList 实现 change.list：列检查点账目（线程可选、limit 1..200、
// 偏移游标）。真源是数据目录 changes/ 下的持久化 manifest，进程重启后仍在。
func (s *Server) handleChangeList(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Limit    int    `json:"limit"`
		Cursor   string `json:"cursor"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("change.list: " + err.Error())
	}
	if p.Limit < 0 || p.Limit > 200 {
		return nil, invalid("change.list: limit 必须在 0..200（0 取默认 50）")
	}
	page, err := s.sess.Changes(strings.TrimSpace(p.ThreadID), p.Limit, p.Cursor)
	if err != nil {
		return nil, invalid(err.Error())
	}
	return map[string]any{
		"changeSets": page.ChangeSets,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	}, nil
}

// handleChangeRevert 实现 change.revert：把检查点备份写回工作区，返回
// 恢复/删除/跳过/失效四份清单（不谎报成功）。审计由 Session.Revert 写
// storage.restore。
//
// 边界：回滚只恢复文件，不改会话历史，也不终止当前线程正在运行的轮次——
// runInfo 不被触碰，事件照常广播；回滚之后同一文件的再次写入会形成新的
// 检查点，可以再次回滚。
func (s *Server) handleChangeRevert(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID     string `json:"threadId"`
		Scope        string `json:"scope"`
		CheckpointID string `json:"checkpointId"`
		Path         string `json:"path"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("change.revert: " + err.Error())
	}
	if strings.TrimSpace(p.ThreadID) == "" {
		return nil, invalid("change.revert: 需要 threadId")
	}
	switch p.Scope {
	case "item", "file", "all":
	case "":
		return nil, invalid("change.revert: 需要 scope（item/file/all）")
	default:
		return nil, invalid("change.revert: scope 只能是 item/file/all")
	}
	if p.Scope == "item" && strings.TrimSpace(p.CheckpointID) == "" {
		return nil, invalid("change.revert: scope=item 需要 checkpointId")
	}
	if p.Scope == "file" && strings.TrimSpace(p.Path) == "" {
		return nil, invalid("change.revert: scope=file 需要 path")
	}
	res, err := s.sess.Revert(ctx, corecontracts.RevertRequest{
		ThreadID: p.ThreadID, Scope: p.Scope, CheckpointID: p.CheckpointID, Path: p.Path,
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{
		"restored":    res.Restored,
		"deleted":     res.Deleted,
		"skipped":     res.Skipped,
		"invalidated": res.Invalidated,
	}, nil
}
