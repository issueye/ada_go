// 检查点账目与回滚（M5，02 §5.4「检查点与回滚」、08 §8.2 change.list/change.revert）。
//
// 职责分界：
//   - internal/toolkit.ChangeQueue：内存账本——检查点登记、写串行、回执里的 chk_ id；
//     进程重启即空，不参与 change.list。
//   - 本文件的 changeStore：持久化账目——数据目录 changes/<checkpointId>/ 下的
//     文件备份（内容 + mode）与 manifest.json（{checkpointId, threadId, runId, tool,
//     paths, createdAt}，原子写）。change.list 与 change.revert 都以它为真源。
//
// 快照时机：宿主 fs 写入之前（cap_fs 的 Checkpoint 回调）把「被覆盖前的内容」
// 拷进备份；同一次工具调用里对同一路径的多次写入只保留第一次（=调用前状态）。
// 回滚语义：existed=true → 写回备份（restored）；备份丢失 → invalidated（不谎报）；
// existed=false（工具新建的文件）→ 回滚即删除（deleted）。回滚前把将被覆盖的
// 当前内容再存一份 revert-<ts>/，二次回滚也不会丢数据。
//
// 边界：回滚只恢复文件，不改会话历史，也不终止运行中的轮次。

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/proto"
)

// hostMount 与 host.DefaultMount 一致：脚本视图里的工作区挂载点。
const hostMount = "/workspace"

// changeManifest 是一个检查点的持久化账目（changes/<checkpointId>/manifest.json）。
type changeManifest struct {
	CheckpointID string `json:"checkpointId"`
	ThreadID     string `json:"threadId"`
	RunID        string `json:"runId,omitempty"`
	CallID       string `json:"callId,omitempty"`
	Tool         string `json:"tool,omitempty"`
	CreatedAt    string `json:"createdAt"`
	// Paths 是工作区相对路径（slash 形式），与 spec ChangeSet.paths 同形。
	Paths []string     `json:"paths"`
	Files []changeFile `json:"files"`
	// RevertedAt 非空表示已回滚：change.list 不再列出，change.revert 拒绝重放。
	RevertedAt string `json:"revertedAt,omitempty"`
}

// changeFile 是一个被覆盖路径的备份记录。
type changeFile struct {
	Path string `json:"path"`
	// Existed=false 表示该文件是工具新建的：回滚 = 删除，不备份内容。
	Existed bool `json:"existed"`
	// Backup 是 files/ 下的备份文件名（路径哈希），仅 Existed=true 时有值。
	Backup string `json:"backup,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

// changeStore 管理数据目录 changes/ 下的持久化检查点账目。
type changeStore struct {
	root string
	mu   sync.Mutex
}

func newChangeStore(dataDir string) *changeStore {
	return &changeStore{root: filepath.Join(dataDir, "changes")}
}

// safeCheckpointID 只接受 chk_ + 安全字符（CallID 是 ULID 或测试短语），
// 防止按 id 拼路径时被注入目录穿越。
func safeCheckpointID(id string) bool {
	if !strings.HasPrefix(id, "chk_") || len(id) > 128 {
		return false
	}
	for _, r := range id[4:] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// backupName 把相对路径映射成单层文件名（内容寻址不必要，确定性哈希即可）。
func backupName(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:]) + ".bak"
}

// base64RawURLEncode/Decode 是偏移量游标的编解码（thread.entries 的 seq 游标同形）。
func base64RawURLEncode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func base64RawURLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// snapshot 把一个将被覆盖的文件并入检查点；同路径已有快照时保留第一次
// （=本次调用开始前的状态，回滚语义正确）。
func (st *changeStore) snapshot(chkID string, m *changeManifest, rel, real string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, p := range m.Paths {
		if p == rel {
			return nil
		}
	}
	cf := changeFile{Path: rel}
	if info, err := os.Stat(real); err == nil && !info.IsDir() {
		data, rerr := os.ReadFile(real)
		if rerr != nil {
			return fmt.Errorf("cli: 读取待覆盖文件失败: %w", rerr)
		}
		if err := os.MkdirAll(filepath.Join(st.root, chkID, "files"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(st.root, chkID, "files", backupName(rel)), data, 0o600); err != nil {
			return err
		}
		cf = changeFile{Path: rel, Existed: true, Backup: backupName(rel), Mode: uint32(info.Mode().Perm())}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cli: 检查待覆盖文件失败: %w", err)
	}
	m.Paths = append(m.Paths, rel)
	m.Files = append(m.Files, cf)
	return st.save(chkID, *m)
}

// save 原子写 manifest.json：先写临时文件再改名，崩溃也不会留下半份账目。
func (st *changeStore) save(chkID string, m changeManifest) error {
	dir := filepath.Join(st.root, chkID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "manifest.json"))
}

// load 读 manifest；文件不存在返回零值清单 + false（不是错误：快照/列表按新建处理）。
func (st *changeStore) load(chkID string) (changeManifest, bool, error) {
	raw, err := os.ReadFile(filepath.Join(st.root, chkID, "manifest.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return changeManifest{}, false, nil
		}
		return changeManifest{}, false, err
	}
	var m changeManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return changeManifest{}, false, fmt.Errorf("cli: manifest.json 损坏（%s）: %w", chkID, err)
	}
	return m, true, nil
}

// list 列出（可选按线程过滤的）未回滚检查点，createdAt 倒序（最新在前）。
func (st *changeStore) list(threadID string) []changeManifest {
	st.mu.Lock()
	defer st.mu.Unlock()
	entries, err := os.ReadDir(st.root)
	if err != nil {
		return nil
	}
	var out []changeManifest
	for _, e := range entries {
		if !e.IsDir() || !safeCheckpointID(e.Name()) {
			continue
		}
		m, ok, err := st.load(e.Name())
		if err != nil || !ok || m.RevertedAt != "" {
			continue
		}
		if threadID != "" && m.ThreadID != threadID {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].CheckpointID > out[j].CheckpointID
	})
	return out
}

// revertOne 回滚一个检查点；onlyPath 非空时只回滚该路径（scope=file）。
// 返回四份清单与「当前内容备份目录」（防二次回滚丢数据），调用方写审计。
func (st *changeStore) revertOne(workspaceRoot, chkID, onlyPath string) (proto.RevertResult, string, error) {
	res := proto.RevertResult{Restored: []string{}, Deleted: []string{}, Skipped: []string{}, Invalidated: []string{}}
	if !safeCheckpointID(chkID) {
		return res, "", fmt.Errorf("cli: 检查点 id 非法: %q", chkID)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	m, ok, err := st.load(chkID)
	if err != nil {
		return res, "", err
	}
	if !ok {
		return res, "", fmt.Errorf("cli: 检查点 %s 不存在或已过期（chk_ 前缀 id 见 change.list）", chkID)
	}
	if m.RevertedAt != "" {
		return res, "", fmt.Errorf("cli: 检查点 %s 已于 %s 回滚，不能重复回滚", chkID, m.RevertedAt)
	}

	// 备份目录按毫秒时间戳命名：同秒内重复回滚合并进同一目录，不丢内容。
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	revertDir := filepath.Join(st.root, chkID, "revert-"+ts)
	changed := false
	kept := m.Files[:0]
	for _, cf := range m.Files {
		if onlyPath != "" && cf.Path != onlyPath {
			kept = append(kept, cf)
			continue
		}
		real := filepath.Join(workspaceRoot, filepath.FromSlash(cf.Path))
		switch {
		case !cf.Existed:
			// 工具新建的文件：回滚 = 删除；目标已经不在则跳过（不谎报删除）。
			if _, err := os.Stat(real); err == nil {
				if rmErr := os.Remove(real); rmErr == nil {
					res.Deleted = append(res.Deleted, cf.Path)
					changed = true
				} else {
					res.Skipped = append(res.Skipped, cf.Path)
				}
			} else {
				res.Skipped = append(res.Skipped, cf.Path)
			}
		default:
			data, berr := os.ReadFile(filepath.Join(st.root, chkID, "files", cf.Backup))
			if berr != nil {
				// 备份缺失：如实标记失效，不把「没恢复」报成成功。
				res.Invalidated = append(res.Invalidated, cf.Path)
				kept = append(kept, cf)
				continue
			}
			// 防二次回滚丢数据：当前内容（若存在）先存一份 revert-<ts> 备份。
			if cur, rerr := os.ReadFile(real); rerr == nil {
				_ = os.MkdirAll(revertDir, 0o700)
				_ = os.WriteFile(filepath.Join(revertDir, backupName(cf.Path)), cur, 0o600)
			}
			mode := os.FileMode(cf.Mode)
			if mode == 0 {
				mode = 0o644
			}
			if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
				res.Skipped = append(res.Skipped, cf.Path)
				kept = append(kept, cf)
				continue
			}
			if err := os.WriteFile(real, data, mode); err != nil {
				res.Skipped = append(res.Skipped, cf.Path)
				kept = append(kept, cf)
				continue
			}
			res.Restored = append(res.Restored, cf.Path)
			changed = true
		}
	}
	m.Files = kept
	m.Paths = pathsOf(kept)
	if onlyPath == "" || len(m.Files) == 0 {
		m.RevertedAt = time.Now().UTC().Format(time.RFC3339Nano)
		changed = true
	}
	if changed {
		if err := st.save(chkID, m); err != nil {
			return res, revertDir, err
		}
	}
	return res, revertDir, nil
}

func pathsOf(files []changeFile) []string {
	out := make([]string, 0, len(files))
	for _, cf := range files {
		out = append(out, cf.Path)
	}
	return out
}

// ---------------------------------------------------------------------------
// Session 侧：回调接线与 change.list / change.revert 的领域入口
// ---------------------------------------------------------------------------

// checkpointWrite 是宿主 fs 写入前回调（host.Context.Checkpoint）：
// 把被覆盖文件备份进持久化检查点，并同步内存账本的路径列表。
func (s *Session) checkpointWrite(meta host.CheckpointMeta, virtualPaths ...string) {
	if s == nil || s.changeStore == nil || meta.CallID == "" {
		return
	}
	chkID := "chk_" + meta.CallID
	cp, haveCP := s.changes.Lookup(chkID)
	// Executor 建档时的归属最准（tool 名来自描述符）；拿不到就用回调带来的。
	info := changeManifest{
		CheckpointID: chkID, ThreadID: meta.ThreadID, RunID: meta.RunID,
		CallID: meta.CallID, Tool: meta.Tool,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if haveCP {
		info.ThreadID, info.RunID, info.Tool = cp.ThreadID, cp.RunID, cp.Tool
		info.CreatedAt = cp.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	m, ok, err := s.changeStore.load(chkID)
	if err != nil {
		s.logWarn("检查点账目读取失败，跳过快照: " + err.Error())
		return
	}
	if ok {
		if m.RevertedAt != "" {
			return
		}
		info.Paths, info.Files = m.Paths, m.Files
	}
	for _, virtual := range virtualPaths {
		rel, err := workspaceRel(virtual)
		if err != nil {
			// 越界路径由宿主 fs 边界负责拒绝；这里不记账也不吞掉其他路径。
			s.logWarn("检查点路径无法归一化: " + err.Error())
			continue
		}
		real := filepath.Join(s.cfg.Workspace, filepath.FromSlash(rel))
		if err := s.changeStore.snapshot(chkID, &info, rel, real); err != nil {
			s.logWarn("检查点快照失败: " + err.Error())
			continue
		}
		_ = s.changes.AddPaths(chkID, virtual)
	}
}

// workspaceRel 把脚本视图路径（/workspace/... 或相对路径）归一化成
// 工作区相对 slash 路径；越出挂载点一律拒绝（与 fsGuard.realPath 同口径）。
func workspaceRel(virtual string) (string, error) {
	v := strings.ReplaceAll(strings.TrimSpace(virtual), "\\", "/")
	if v == "" {
		return "", errors.New("cli: 空路径")
	}
	if !strings.HasPrefix(v, "/") {
		// 相对路径按挂载点解析（与上游 Cwd=Mount 一致）。
		v = path.Join(hostMount, v)
	}
	clean := path.Clean(v)
	if clean != hostMount && !strings.HasPrefix(clean, hostMount+"/") {
		return "", fmt.Errorf("cli: 路径 %q 越出挂载点 %s", virtual, hostMount)
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(clean, hostMount), "/")
	if rel == "" {
		return "", errors.New("cli: 路径是工作区根，没有可检查点的文件")
	}
	return rel, nil
}

// ChangePage 是 change.list 的分页结果。
type ChangePage struct {
	ChangeSets []proto.ChangeSet
	NextCursor string
	HasMore    bool
}

// Changes 列检查点账目（change.list 数据源）。threadID 为空列本工作区全部；
// cursor 是 base64 的偏移量；limit ≤ 0 取 50，上限 200（与协议一致）。
func (s *Session) Changes(threadID string, limit int, cursor string) (ChangePage, error) {
	page := ChangePage{ChangeSets: []proto.ChangeSet{}}
	if s.changeStore == nil {
		return page, nil
	}
	offset, err := decodeOffsetCursor(cursor)
	if err != nil {
		return page, fmt.Errorf("cli: change.list 游标非法: %w", err)
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	all := s.changeStore.list(strings.TrimSpace(threadID))
	for i := offset; i < len(all) && len(page.ChangeSets) < limit; i++ {
		page.ChangeSets = append(page.ChangeSets, changeSetOf(all[i]))
	}
	if next := offset + limit; next < len(all) {
		page.HasMore = true
		page.NextCursor = encodeOffsetCursor(next)
	}
	return page, nil
}

func changeSetOf(m changeManifest) proto.ChangeSet {
	cs := proto.ChangeSet{
		ID:        m.CheckpointID,
		ThreadID:  m.ThreadID,
		RunID:     m.RunID,
		Tool:      m.Tool,
		Paths:     append([]string(nil), m.Paths...),
		CreatedAt: m.CreatedAt,
	}
	if cs.Paths == nil {
		cs.Paths = []string{}
	}
	if m.CallID != "" {
		cs.ItemKey = "tool:" + m.CallID
	}
	return cs
}

func encodeOffsetCursor(n int) string {
	if n <= 0 {
		return ""
	}
	return base64RawURLEncode([]byte(strconv.Itoa(n)))
}

func decodeOffsetCursor(cur string) (int, error) {
	if cur == "" {
		return 0, nil
	}
	raw, err := base64RawURLDecode(cur)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0, errors.New("游标形状非法")
	}
	return n, nil
}

// RevertRequest 是 change.revert 的领域入参（scope: item | file | all）。
type RevertRequest struct {
	ThreadID string
	Scope    string
	// CheckpointID 是 scope=item 时的检查点 id（chk_...）；scope=all/file 时忽略。
	CheckpointID string
	// Path 是 scope=file 时的单文件路径（脚本视图或工作区相对均可）。
	Path string
}

// Revert 回滚检查点（change.revert 数据源）。
//
// 语义：只恢复文件，不改会话历史，也不终止运行中的轮次（正在跑的这一轮
// 不受影响；它后续的写入会形成新的检查点，可再次回滚）。每次回滚写一条
// storage.restore 审计；回滚前把将被覆盖的当前内容存 revert-<ts> 备份。
func (s *Session) Revert(ctx context.Context, req RevertRequest) (proto.RevertResult, error) {
	res := proto.RevertResult{Restored: []string{}, Deleted: []string{}, Skipped: []string{}, Invalidated: []string{}}
	if s.changeStore == nil {
		return res, domainErrorCoreUnavailable("检查点账目未启用")
	}
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		return res, errors.New("cli: change.revert 需要 threadId")
	}
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "item"
	}
	switch scope {
	case "item", "file", "all":
	default:
		return res, fmt.Errorf("cli: change.revert scope 只能是 item/file/all，收到 %q", req.Scope)
	}

	targets := []changeManifest{}
	switch scope {
	case "item":
		chkID := strings.TrimSpace(req.CheckpointID)
		if chkID == "" {
			return res, errors.New("cli: scope=item 需要 checkpointId")
		}
		m, ok, err := s.changeStore.load(chkID)
		if err != nil {
			return res, err
		}
		if !ok || m.RevertedAt != "" || m.ThreadID != threadID {
			return res, fmt.Errorf("cli: 检查点 %s 不在线程 %s 的未回滚账目里", chkID, threadID)
		}
		targets = append(targets, m)
	case "all":
		targets = s.changeStore.list(threadID)
		if len(targets) == 0 {
			return res, fmt.Errorf("cli: 线程 %s 没有可回滚的检查点", threadID)
		}
	case "file":
		rel, err := workspaceRel(strings.TrimSpace(req.Path))
		if err != nil {
			return res, fmt.Errorf("cli: scope=file 需要 workspace 内的 path: %w", err)
		}
		for _, m := range s.changeStore.list(threadID) {
			for _, p := range m.Paths {
				if p == rel {
					targets = append(targets, m)
					break
				}
			}
			if len(targets) == 1 {
				break // 只回滚最近一份包含该路径的检查点
			}
		}
		if len(targets) == 0 {
			return res, fmt.Errorf("cli: 线程 %s 的检查点里没有路径 %s", threadID, req.Path)
		}
	}

	for _, m := range targets {
		onlyPath := ""
		if scope == "file" {
			onlyPath, _ = workspaceRel(strings.TrimSpace(req.Path))
		}
		one, revertDir, err := s.changeStore.revertOne(s.cfg.Workspace, m.CheckpointID, onlyPath)
		if err != nil {
			return res, err
		}
		res.Restored = append(res.Restored, one.Restored...)
		res.Deleted = append(res.Deleted, one.Deleted...)
		res.Skipped = append(res.Skipped, one.Skipped...)
		res.Invalidated = append(res.Invalidated, one.Invalidated...)
		// 内存账本同步失效（回执里的 chk_ id 不再可回滚）。
		s.changes.Drop(m.CheckpointID)
		s.audit(ctx, "storage.restore", threadID, "", map[string]any{
			"checkpointId": m.CheckpointID, "scope": scope, "tool": m.Tool,
			"restored": one.Restored, "deleted": one.Deleted,
			"skipped": one.Skipped, "invalidated": one.Invalidated,
			"preRevertBackupDir": revertDir,
		})
	}
	return res, nil
}

// Audit 写一条审计（09 §10 审计范围：交互、工具、插件加载/重载/禁用、鉴权失败、
// 配置变更、回滚）。corerpc 的 turn.cancel 等协议层动作也走这里。
func (s *Session) Audit(ctx context.Context, kind, threadID, pluginID string, detail any) {
	if s == nil || s.store == nil || strings.TrimSpace(kind) == "" {
		return
	}
	var raw json.RawMessage
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			raw = b
		}
	}
	rec := sqlite.AuditRecord{Kind: kind, ThreadID: threadID, PluginID: pluginID, Detail: raw, At: s.clock.Now()}
	if err := s.store.RecordAudit(ctx, rec); err != nil && s.cfg.Log != nil {
		s.cfg.Log("warn", "审计写入失败: "+err.Error(), map[string]any{"kind": kind})
	}
}

// audit 是包内便捷形式。
func (s *Session) audit(ctx context.Context, kind, threadID, pluginID string, detail any) {
	s.Audit(ctx, kind, threadID, pluginID, detail)
}

// domainErrorCoreUnavailable 统一「账目不可用」错误形状（core_unavailable）。
func domainErrorCoreUnavailable(msg string) error {
	return domain.NewError(domain.CodeCoreUnavailable, msg)
}
