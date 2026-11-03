package cli

// 插件安装位置与启用状态的存储分层（生产统一）：
//
//   - 插件本体：全局 ~/.adago/plugins/<id>/（plugin.admin 安装桥的唯一落点；
//     工作区 .adago/plugins 仅作为旧目录继续被发现，新安装不再写入）。
//   - 启用状态：全局 ~/.adago/plugins.json 是用户级启停，跨工作区共享；
//     工作区 <workspace>/.adago/plugins.json 只在「该工作区有单独启停」时存在，
//     其条目覆盖全局。两者都没有条目时回落产品声明（声明=启用）。
//   - 旧版把启停写在 plugin_meta（SQLite，按工作区）：打开会话时若发现用户
//     显式选择（user_set=1）且工作区覆盖文件还不存在，就一次性落成
//     .adago/plugins.json，之后 SQLite 只保留版本/指纹/last_error 等运行元信息。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/corecontracts"
	"github.com/adag/ada-go/internal/plugins/discovery"
	"github.com/adag/ada-go/internal/plugins/host"
)

// pluginStateEntry 是一个插件的启用决策；出现在文件里即视为「用户显式选择」。
type pluginStateEntry struct {
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// pluginStateFile 是 plugins.json 的落盘形状（全局与工作区覆盖共用）。
type pluginStateFile struct {
	Version int                         `json:"version"`
	Plugins map[string]pluginStateEntry `json:"plugins"`
}

// globalPluginStatePath 返回用户级 plugins.json；ADAGO_HOME 优先，主目录
// 不可用时返回空串（调用方退回工作区文件，保证 Core 仍能工作）。
func globalPluginStatePath() string {
	if home := strings.TrimSpace(os.Getenv("ADAGO_HOME")); home != "" {
		return filepath.Join(home, "plugins.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".adago", "plugins.json")
}

// globalPluginStatePath 是会话侧的全局状态路径：Config.PluginStatePath 显式
// 指定时优先（测试/嵌入方隔离真实主目录），否则用默认位置。
func (s *Session) globalPluginStatePath() string {
	if p := strings.TrimSpace(s.cfg.PluginStatePath); p != "" {
		return p
	}
	return globalPluginStatePath()
}

func workspacePluginStatePath(workspace string) string {
	return filepath.Join(workspace, ".adago", "plugins.json")
}

// legacyWorkspacePluginStatePath 是旧工作区启停文件（只读兼容）。
func legacyWorkspacePluginStatePath(workspace string) string {
	return filepath.Join(workspace, ".adag", "plugins.json")
}

// workspacePluginState 读取工作区覆盖：优先 .adago/plugins.json，新文件不存在时
// 回落到旧 .adag/plugins.json（legacy=true，只读兼容）。
func workspacePluginState(workspace string) (entries map[string]pluginStateEntry, exists, legacy bool) {
	entries, exists, err := loadPluginStateFile(workspacePluginStatePath(workspace))
	if err != nil || exists {
		return entries, exists, false
	}
	entries, exists, err = loadPluginStateFile(legacyWorkspacePluginStatePath(workspace))
	if err != nil {
		return nil, false, false
	}
	return entries, exists, exists
}

// materializeWorkspacePluginState 在首次写新工作区文件前，把旧 .adag/plugins.json
// 的条目带到 .adago/plugins.json，避免切换一个插件时丢掉其它覆盖。
func materializeWorkspacePluginState(workspace string) {
	if strings.TrimSpace(workspace) == "" {
		return
	}
	wsPath := workspacePluginStatePath(workspace)
	if _, err := os.Stat(wsPath); err == nil {
		return
	}
	entries, exists, err := loadPluginStateFile(legacyWorkspacePluginStatePath(workspace))
	if err != nil || !exists || len(entries) == 0 {
		return
	}
	_ = savePluginStateFile(wsPath, entries)
}

// loadPluginStateFile 读取一份启停状态；文件不存在返回 (nil, false, nil)，
// 损坏按不存在处理（JSON 状态坏掉不应该让 Core 起不来）。
func loadPluginStateFile(path string) (map[string]pluginStateEntry, bool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, false, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var data pluginStateFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, false, nil
	}
	if data.Plugins == nil {
		data.Plugins = map[string]pluginStateEntry{}
	}
	return data.Plugins, true, nil
}

// savePluginStateFile 原子写入启停状态（0600，同目录临时文件 + rename）。
func savePluginStateFile(path string, entries map[string]pluginStateEntry) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("cli: 插件启停状态路径为空")
	}
	raw, err := json.MarshalIndent(pluginStateFile{
		Version: 1,
		Plugins: entries,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("cli: 插件启停状态无法序列化: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cli: 创建插件状态目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".plugins-*.json")
	if err != nil {
		return fmt.Errorf("cli: 创建插件状态临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cli: 设置插件状态权限失败: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cli: 写入插件启停状态失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cli: 关闭插件状态文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("cli: 替换插件启停状态失败: %w", err)
	}
	return nil
}

// overlayPluginState 把 JSON 启停状态（工作区覆盖 > 全局）套到 plugin_meta
// 记录上：出现在文件里即视为用户显式选择。同时负责一次性迁移：工作区覆盖
// 文件还不存在、而 plugin_meta 里有用户显式选择时，把它们落成 .adago/plugins.json。
func (s *Session) overlayPluginState(ctx context.Context, out map[string]sqlite.PluginMetaRecord) {
	wsPath := workspacePluginStatePath(s.cfg.Workspace)
	wsEntries, wsExists, _ := workspacePluginState(s.cfg.Workspace)
	if !wsExists {
		s.migrateWorkspacePluginOverrides(ctx, wsPath, out)
		wsEntries, _, _ = workspacePluginState(s.cfg.Workspace)
	}
	globalEntries, _, err := loadPluginStateFile(s.globalPluginStatePath())
	if err != nil {
		s.logWarn("全局插件启停状态读取失败，忽略: " + err.Error())
	}
	for id, entry := range wsEntries {
		overlayPluginStateEntry(out, id, entry)
	}
	for id, entry := range globalEntries {
		if _, ok := wsEntries[id]; ok {
			continue // 工作区覆盖优先
		}
		overlayPluginStateEntry(out, id, entry)
	}
}

func overlayPluginStateEntry(out map[string]sqlite.PluginMetaRecord, id string, entry pluginStateEntry) {
	rec, ok := out[id]
	if !ok {
		rec = sqlite.PluginMetaRecord{ID: id}
	}
	rec.Enabled = entry.Enabled
	rec.UserSet = true
	out[id] = rec
}

// migrateWorkspacePluginOverrides 把旧版 per-workspace 的用户启停（plugin_meta
// 里 user_set=1 的行）一次性物化成 <workspace>/.adago/plugins.json。
func (s *Session) migrateWorkspacePluginOverrides(
	ctx context.Context, wsPath string, out map[string]sqlite.PluginMetaRecord,
) {
	entries := map[string]pluginStateEntry{}
	for id, rec := range out {
		if !rec.UserSet {
			continue
		}
		entries[id] = pluginStateEntry{Enabled: rec.Enabled, UpdatedAt: nowRFC3339()}
	}
	if len(entries) == 0 {
		return
	}
	if err := savePluginStateFile(wsPath, entries); err != nil {
		s.logWarn("迁移工作区插件启停状态失败: " + err.Error())
		return
	}
	s.audit(ctx, "plugin.state.migrated", "", "", map[string]any{"entries": len(entries)})
}

// setPluginStateEntry 把一条启停决策写进目标文件；返回写入前的状态供回滚。
func setPluginStateEntry(
	path, pluginID string, enabled bool,
) (prev pluginStateEntry, hadPrev bool, err error) {
	entries, _, err := loadPluginStateFile(path)
	if err != nil {
		return prev, false, err
	}
	if entries == nil {
		entries = map[string]pluginStateEntry{}
	}
	prev, hadPrev = entries[pluginID]
	entries[pluginID] = pluginStateEntry{Enabled: enabled, UpdatedAt: nowRFC3339()}
	if err := savePluginStateFile(path, entries); err != nil {
		return prev, hadPrev, err
	}
	return prev, hadPrev, nil
}

// restorePluginStateEntry 在重载失败时把目标文件回滚到写入前的状态。
func restorePluginStateEntry(path, pluginID string, prev pluginStateEntry, hadPrev bool) {
	entries, _, err := loadPluginStateFile(path)
	if err != nil {
		return
	}
	if entries == nil {
		entries = map[string]pluginStateEntry{}
	}
	if hadPrev {
		entries[pluginID] = prev
	} else {
		delete(entries, pluginID)
	}
	_ = savePluginStateFile(path, entries)
}

// installPluginGlobally 实现 host.Context.InstallPlugin（capability: plugin.admin）。
// 骨架统一写入全局插件根 ~/.adago/plugins/<id>/；不覆盖已有目录，也不接受
// 与随包插件冲突的 id。路径/大小边界在这里收口，脚本侧拿不到任意文件写权限。
func (s *Session) installPluginGlobally(
	ctx context.Context, spec host.PluginInstallSpec,
) (host.PluginInstallResult, error) {
	id := strings.ToLower(strings.TrimSpace(spec.ID))
	if len(id) < 3 || len(id) > 64 || !pluginIDRE.MatchString(id) {
		return host.PluginInstallResult{}, errors.New("插件 ID 需要 3-64 位小写字母、数字或连字符，且以字母开头")
	}
	for _, sc := range s.baseScripts {
		if sc.Manifest != nil && sc.Manifest.ID == id {
			return host.PluginInstallResult{}, fmt.Errorf("插件 id %s 与随包插件冲突，不能安装", id)
		}
	}
	if len(spec.Files) == 0 {
		return host.PluginInstallResult{}, errors.New("插件骨架至少需要一个文件")
	}
	if len(spec.Files) > maxPluginInstallFiles {
		return host.PluginInstallResult{}, fmt.Errorf("插件骨架最多 %d 个文件", maxPluginInstallFiles)
	}
	cleaned := make([]host.PluginFileSpec, 0, len(spec.Files))
	seen := map[string]bool{}
	total := 0
	for _, f := range spec.Files {
		rel, err := normalizePluginFileRel(f.Path)
		if err != nil {
			return host.PluginInstallResult{}, err
		}
		if seen[rel] {
			return host.PluginInstallResult{}, fmt.Errorf("插件骨架里有重复文件 %q", rel)
		}
		seen[rel] = true
		if len(f.Content) > maxPluginInstallFileBytes {
			return host.PluginInstallResult{}, fmt.Errorf("文件 %s 超过单文件上限 %d 字节", rel, maxPluginInstallFileBytes)
		}
		total += len(f.Content)
		if total > maxPluginInstallTotalBytes {
			return host.PluginInstallResult{}, fmt.Errorf("插件骨架总大小超过上限 %d 字节", maxPluginInstallTotalBytes)
		}
		cleaned = append(cleaned, host.PluginFileSpec{Path: rel, Content: f.Content})
	}
	root, err := discovery.GlobalRoot()
	if err != nil {
		return host.PluginInstallResult{}, fmt.Errorf("定位全局插件目录失败: %w", err)
	}
	dir := filepath.Join(root, id)
	if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
		return host.PluginInstallResult{}, fmt.Errorf("插件 %s 已存在（%s）", id, dir)
	} else if statErr == nil {
		return host.PluginInstallResult{}, fmt.Errorf("全局插件目录里已有同名文件 %s", id)
	}
	for _, f := range cleaned {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return host.PluginInstallResult{}, fmt.Errorf("创建插件目录失败: %w", err)
		}
		if err := os.WriteFile(target, []byte(f.Content), 0o644); err != nil {
			return host.PluginInstallResult{}, fmt.Errorf("写入 %s 失败: %w", f.Path, err)
		}
	}
	// 预加载校验：骨架必须能被完整装配（入口工厂执行、registerTool/registerAction
	// 形状合法）。失败即删除刚写入的目录——「校验失败不落盘」对语义错误同样成立，
	// 而不是等用户点重载时才发现装不上。
	if err := s.preflightPluginAssembly(ctx, dir); err != nil {
		_ = os.RemoveAll(dir)
		return host.PluginInstallResult{}, fmt.Errorf("插件 %s 预加载失败，已回滚安装: %w", id, err)
	}
	names := make([]string, 0, len(cleaned))
	for _, f := range cleaned {
		names = append(names, f.Path)
	}
	s.audit(ctx, "plugin.installed", "", id, map[string]any{"dir": dir, "files": len(names)})
	return host.PluginInstallResult{ID: id, Dir: dir, Files: names}, nil
}

// preflightPluginAssembly 用单插件产品做一次完整装配：入口工厂执行、工具/动作
// 注册形状、能力信任矩阵都在这里收口；错误文本与真实重载一致。
func (s *Session) preflightPluginAssembly(ctx context.Context, dir string) error {
	// LoadDirDetailed 以「父目录里的插件子目录」为单位扫描；dir 本身就是插件
	// 目录，所以加载它的父目录再挑出目标项。
	loaded, warnings, err := discovery.LoadDirDetailed(filepath.Dir(dir), "")
	if err != nil {
		return err
	}
	var script host.Script
	found := false
	for _, p := range loaded {
		if p.Dir == dir {
			script = p.Script
			found = true
			break
		}
	}
	if !found {
		for _, w := range warnings {
			if w.Dir == dir {
				return fmt.Errorf("%s", w.Message)
			}
		}
		return errors.New("目录里没有可加载的插件")
	}
	productRaw, err := json.Marshal(map[string]any{
		"id": "plugin-install-preview", "name": "插件安装预检",
		"plugins":      []string{script.Manifest.ID},
		"capabilities": map[string]any{"skills": false, "images": false, "subagents": false},
		"identity":     map[string]any{"name": "ada-go", "locale": "zh-CN"},
		"policies": map[string]any{
			"toolTimeoutMs": 120000, "turnTimeoutMs": 0, "maxTurnTokens": 0,
			"loopRepeatLimit": 3, "loopAbortLimit": 5, "parallelTools": 1,
		},
	})
	if err != nil {
		return err
	}
	p, err := coreapp.ParseProduct(productRaw)
	if err != nil {
		return err
	}
	asm, err := s.assembler.Assemble(ctx, p, []host.Script{script}, corecontracts.Deps{
		Context: host.Context{
			Workspace: dir,
			Log:       func(string, string, map[string]any) {},
			HTTP:      s.httpBridge,
		},
	})
	if err != nil {
		return err
	}
	return asm.Close(ctx)
}

// normalizePluginFileRel 校验安装骨架里的相对路径：拒绝绝对路径、反斜杠、
// 「..」与隐藏段，输出干净的斜杠路径。
func normalizePluginFileRel(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("插件骨架文件路径为空")
	}
	if strings.Contains(p, "\\") || strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return "", fmt.Errorf("插件骨架文件路径 %q 必须是插件目录内的相对路径", raw)
	}
	slash := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if slash == "." || slash == "/" || strings.HasPrefix(slash, "../") ||
		strings.HasPrefix(slash, "/") || strings.Contains(slash, "/.adago/") {
		return "", fmt.Errorf("插件骨架文件路径 %q 越出插件目录", raw)
	}
	for _, seg := range strings.Split(slash, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("插件骨架文件路径 %q 含隐藏/非法段", raw)
		}
	}
	if len(slash) > 200 {
		return "", fmt.Errorf("插件骨架文件路径 %q 过长", raw)
	}
	return slash, nil
}

// plugin.admin 的安装边界（09 §9.5 口径）。
const (
	maxPluginInstallFiles      = 32
	maxPluginInstallFileBytes  = 256 << 10
	maxPluginInstallTotalBytes = 1 << 20
)

// pluginIDRE 与 discovery/manifest 的目录名约定一致。
var pluginIDRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// removePlugin 实现 host.Context.RemovePlugin（capability: plugin.admin）。
// 删除发现的最高优先级副本（工作区旧目录优先于全局根）；随包插件不可移除。
// 移除后同步清掉两份启停状态里的条目（残留无害，但留着会误导 check_plugin）。
// 生效需重载：已加载的代仍然在内存里服务，磁盘删除不影响在途调用。
func (s *Session) removePlugin(ctx context.Context, pluginID string) (host.PluginRemoveResult, error) {
	id := strings.ToLower(strings.TrimSpace(pluginID))
	if len(id) < 3 || len(id) > 64 || !pluginIDRE.MatchString(id) {
		return host.PluginRemoveResult{}, errors.New("插件 ID 需要 3-64 位小写字母、数字或连字符，且以字母开头")
	}
	for _, sc := range s.baseScripts {
		if sc.Manifest != nil && sc.Manifest.ID == id {
			return host.PluginRemoveResult{}, fmt.Errorf("插件 %s 是随包插件，不能移除（只能启用/禁用）", id)
		}
	}
	candidates := []struct {
		source string
		dir    string
	}{
		{"workspace", filepath.Join(s.cfg.Workspace, ".adago", "plugins", id)},
		{"global", filepath.Join(s.globalPluginsRoot(), id)},
	}
	for _, c := range candidates {
		if info, err := os.Stat(c.dir); err != nil || !info.IsDir() {
			continue
		}
		if err := os.RemoveAll(c.dir); err != nil {
			return host.PluginRemoveResult{}, fmt.Errorf("删除插件目录失败: %w", err)
		}
		s.dropPluginStateEntry(id)
		s.audit(ctx, "plugin.removed", "", id, map[string]any{"dir": c.dir, "source": c.source})
		return host.PluginRemoveResult{ID: id, Dir: c.dir, Source: c.source}, nil
	}
	return host.PluginRemoveResult{}, fmt.Errorf("没有找到可移除的插件目录（%s）", id)
}

// globalPluginsRoot 返回全局插件根；主目录不可用时退回当前数据目录下的
// plugins/（与 globalPluginStatePath 的退化策略一致）。
func (s *Session) globalPluginsRoot() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".adago", "plugins")
	}
	return filepath.Join(s.cfg.DataDir, "plugins")
}

// dropPluginStateEntry 把一个 id 从全局与工作区启停状态里同时清掉（尽力而为）。
func (s *Session) dropPluginStateEntry(pluginID string) {
	for _, path := range []string{
		workspacePluginStatePath(s.cfg.Workspace),
		s.globalPluginStatePath(),
	} {
		entries, exists, err := loadPluginStateFile(path)
		if err != nil || !exists {
			continue
		}
		if _, ok := entries[pluginID]; !ok {
			continue
		}
		delete(entries, pluginID)
		if err := savePluginStateFile(path, entries); err != nil {
			s.logWarn("清理插件启停条目失败: " + err.Error())
		}
	}
}

// checkPlugin 实现 host.Context.CheckPlugin（capability: plugin.admin）。
// pluginID 为空时返回全部插件的摘要；非空时给出单个插件的诊断报告，
// 包含发现位置、启停决策来源、装配状态与入口预加载结果。
func (s *Session) checkPlugin(ctx context.Context, pluginID string) (host.PluginCheckResult, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		views := s.PluginViews(ctx)
		out := host.PluginCheckResult{Plugins: make([]host.PluginCheckSummary, 0, len(views))}
		for _, v := range views {
			out.Plugins = append(out.Plugins, host.PluginCheckSummary{
				ID: v.ID, Source: v.Source, Status: v.Status, Enabled: v.Enabled, LastError: v.LastError,
			})
		}
		return out, nil
	}
	view, ok := s.PluginViewDetail(ctx, pluginID)
	if !ok {
		return host.PluginCheckResult{Report: &host.PluginCheckReport{
			ID:     pluginID,
			Found:  false,
			Status: "missing",
			Checks: []host.PluginCheckItem{{
				Name: "发现", OK: false,
				Detail: "没有在随包/全局/工作区三个来源里发现这个插件；检查 id 拼写，或先用 create_plugin 创建",
			}},
		}}, nil
	}
	report := host.PluginCheckReport{
		ID: view.ID, Found: true,
		Name: view.Name, Version: view.Version, Trust: view.Trust,
		Source: view.Source, Dir: view.Path,
		Enabled: view.Enabled, Status: view.Status, LastError: view.LastError,
	}
	report.Tools = append(report.Tools, view.Tools...)
	for _, f := range view.Files {
		report.Files = append(report.Files, f.Path)
	}
	if view.ManifestCapabilities != nil {
		report.Capabilities = append([]string(nil), view.ManifestCapabilities...)
	}
	addCheck := func(name string, ok bool, detail string) {
		report.Checks = append(report.Checks, host.PluginCheckItem{Name: name, OK: ok, Detail: detail})
	}
	addCheck("发现", true, "来源 "+sourceLabel(view.Source)+locationDetail(view.Path))
	addCheck("清单", view.ID != "" && view.Version != "",
		fmt.Sprintf("id=%s version=%s trust=%s entry=%s capabilities=[%s]",
			view.ID, view.Version, view.Trust, view.Entry, strings.Join(report.Capabilities, ", ")))
	addCheck("启停状态", true, enabledDetail(s, view))
	asmDetail := "状态 " + view.Status
	if view.LastError != "" {
		asmDetail += "：" + view.LastError
	}
	addCheck("装配状态", view.Status == "ready", asmDetail)
	switch {
	case view.Source == "bundled" || view.Source == "":
		addCheck("入口加载", true, "随包插件随应用装配，无需单独预检")
	case !view.Enabled:
		addCheck("入口加载", true, "插件当前停用，跳过预检；启用并重载后再查")
	case strings.TrimSpace(view.Path) != "":
		if err := s.preflightPluginAssembly(ctx, view.Path); err != nil {
			addCheck("入口加载", false, err.Error())
		} else {
			addCheck("入口加载", true, "预检装配通过（入口工厂执行 + 注册形状合法）")
		}
	default:
		addCheck("入口加载", true, "没有落盘目录，跳过预检")
	}
	return host.PluginCheckResult{Report: &report}, nil
}

func sourceLabel(source string) string {
	switch source {
	case "bundled", "":
		return "随包（应用自带）"
	case discovery.SourceGlobal:
		return "全局 ~/.adago/plugins"
	case discovery.SourceWorkspace:
		return "工作区 .adago/plugins（旧目录）"
	default:
		return source
	}
}

func locationDetail(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return "：" + path
}

// enabledDetail 说明启停决策来自哪一层：工作区覆盖 / 全局 / 产品声明。
func enabledDetail(s *Session, view PluginView) string {
	decision := "产品声明"
	if entries, exists, _ := workspacePluginState(s.cfg.Workspace); exists {
		if _, ok := entries[view.ID]; ok {
			decision = "工作区覆盖 .adago/plugins.json"
		}
	}
	if decision == "产品声明" {
		if entries, exists, _ := loadPluginStateFile(s.globalPluginStatePath()); exists {
			if _, ok := entries[view.ID]; ok {
				decision = "全局 plugins.json"
			}
		}
	}
	state := "启用"
	if !view.Enabled {
		state = "停用"
	}
	return state + "（决策来源：" + decision + "）"
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
