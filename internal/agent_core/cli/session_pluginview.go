package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/agent_core/plugins/discovery"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/adapters/sqlite"
	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/pluginapi"
)

// PluginView 是 plugin.list 的一行：运行时摘要 + 发现层信息。
type PluginView = corecontracts.PluginView

// PluginFile 是插件包内一个文件（相对路径，不含宿主绝对路径）。
type PluginFile = corecontracts.PluginFile

// PluginViews 列出随包 + 第三方插件的当前状态（plugin.list 的数据源）。
//
// 列表是「全部随包/已安装插件」而不是「当前产品装配的插件」：
// 产品未声明的随包插件用中性状态 unused 表示，不伪装成 failed。
// README/文件清单只在详情请求（detailID 非空）时计算，避免每次列表/门禁
// 轮询都去读盘或展开包内容。
func (s *Session) PluginViews(ctx context.Context) []PluginView {
	return s.pluginViews(ctx, "")
}

// PluginViewDetail 返回单个插件的完整视图（含 README 与文件清单）。
func (s *Session) PluginViewDetail(ctx context.Context, id string) (PluginView, bool) {
	for _, v := range s.pluginViews(ctx, id) {
		if v.ID == id {
			return v, true
		}
	}
	return PluginView{}, false
}

// PluginViewsDetailed 返回全部插件视图，但只为 detailID 计算 README/文件清单。
func (s *Session) PluginViewsDetailed(ctx context.Context, detailID string) []PluginView {
	return s.pluginViews(ctx, detailID)
}

func (s *Session) pluginViews(ctx context.Context, detailID string) []PluginView {
	s.mu.Lock()
	asm := s.asm
	base := s.baseScripts
	frontend := append([]host.Script(nil), s.frontendScripts...)
	discovered := append([]discovery.Plugin(nil), s.discovered...)
	failed := map[string]string{}
	for id, msg := range s.failedPlugins {
		failed[id] = msg
	}
	product := s.product
	s.mu.Unlock()

	productIDs := map[string]bool{}
	if product != nil {
		for _, id := range product.Plugins {
			productIDs[id] = true
		}
	}

	meta := s.listPluginMeta(ctx)
	frontByID := make(map[string]host.Script, len(frontend))
	for _, sc := range frontend {
		if sc.Manifest != nil {
			frontByID[sc.Manifest.ID] = sc
		}
	}
	out := make([]PluginView, 0, len(base)+len(frontend)+len(discovered)+len(failed))
	seen := map[string]bool{}
	for _, sc := range base {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		seen[id] = true
		rec, hasRec := meta[id]
		v := pluginViewOf(sc, "", nil, rec, hasRec, asm)
		v.Backend = true
		v.Frontend = sc.Manifest.Frontend != nil
		if detailID != "" && detailID == id {
			var front *host.Script
			if f, ok := frontByID[id]; ok {
				front = &f
			}
			baseScript := sc
			// 开发/源码或 Electron resources 运行时，官方插件有真实目录。
			if dir := officialPluginDir(s.assembler, id); dir != "" {
				v.Path = dir
				v.Files = walkPluginFiles(dir)
			} else {
				v.Files = pluginFiles(&baseScript, front, "")
			}
			v.Readme = pluginReadme("", &baseScript)
		}
		if !productIDs[id] {
			// 随包但当前产品未声明：只有用户显式启用过才进入装配；
			// 否则用中性状态 unused 表示，可查看、可启用，但不是故障。
			optedIn := hasRec && rec.UserSet && rec.Enabled
			if !optedIn {
				v.Status = "unused"
				v.Enabled = false
				v.LastError = ""
			}
		}
		// 装配期被隔离的随包插件（用户启用但坏掉）：状态与错误必须可见，
		// 不能因为它在 base 列表里就显示成 ready。
		if msg, bad := failed[id]; bad {
			v.Status = "failed"
			v.LastError = msg
			v.Enabled = true
		}
		out = append(out, v)
	}
	// frontend-only 插件也进列表：产品声明或用户显式启用决定它是否已挂载。
	for _, sc := range frontend {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		if seen[id] {
			continue
		}
		seen[id] = true
		rec, hasRec := meta[id]
		enabled := productIDs[id]
		if hasRec && rec.UserSet {
			enabled = rec.Enabled
		}
		status := "ready"
		if !enabled {
			if productIDs[id] {
				status = "disabled"
			} else {
				status = "unused"
			}
		}
		frontScript := sc
		v := PluginView{
			ID: id, Name: sc.Manifest.Name, Version: sc.Manifest.Version,
			APIVersion: sc.Manifest.APIVersion, Trust: string(sc.Manifest.Trust),
			Enabled: enabled, Frontend: true, Status: status,
		}
		if detailID != "" && detailID == id {
			if dir := officialPluginDir(s.assembler, id); dir != "" {
				v.Path = dir
				v.Files = walkPluginFiles(dir)
			} else {
				v.Files = pluginFiles(nil, &frontScript, "")
			}
			v.Readme = pluginReadme("", &frontScript)
		}
		out = append(out, v)
	}
	for _, p := range discovered {
		id := p.ID()
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		rec, hasRec := meta[id]
		v := pluginViewOf(p.Script, p.Source, &p, rec, hasRec, asm)
		v.Backend = true
		v.Frontend = p.Script.Manifest != nil && p.Script.Manifest.Frontend != nil
		if p.Dir != "" {
			if abs, absErr := filepath.Abs(p.Dir); absErr == nil {
				v.Path = abs
			} else {
				v.Path = p.Dir
			}
		}
		if detailID != "" && detailID == id {
			v.Files = pluginFiles(nil, nil, p.Dir)
			v.Readme = pluginReadme(p.Dir, &p.Script)
		}
		out = append(out, v)
	}
	for id, msg := range failed {
		if seen[id] {
			continue
		}
		seen[id] = true
		v := PluginView{ID: id, Status: "failed", LastError: msg, Enabled: true, Backend: true,
			Trust: string(pluginapi.TrustThirdParty)}
		if rec, ok := meta[id]; ok {
			v.Enabled = rec.Enabled
			v.Version = rec.Version
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

const (
	maxPluginFiles  = 300
	maxReadmeBytes  = 256 << 10
	pluginReadmeDoc = "README.md"
)

// officialPluginDir 在开发/源码或 Electron resources 布局里定位官方插件目录
// （绝对路径）；找不到时返回空。
func officialPluginDir(asm corecontracts.Assembler, id string) string {
	// 与加载器共用同一份解析（ADAG_OFFICIAL_PLUGINS_DIR / resources / 源码检出）。
	if root, err := asm.OfficialRoot(); err == nil && root != "" {
		dir := filepath.Join(root, id)
		if st, statErr := os.Stat(filepath.Join(dir, "plugin.json")); statErr == nil && !st.IsDir() {
			if abs, absErr := filepath.Abs(dir); absErr == nil {
				return abs
			}
			return filepath.Clean(dir)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exeDir := filepath.Dir(exe)
	candidates := []string{
		// desktop/bin/adag.exe 与 bin/release/adag-* 都向上两级到仓库根。
		filepath.Join(exeDir, "..", "..", "plugins", "official", id),
		filepath.Join(exeDir, "..", "plugins", "official", id),
		filepath.Join(exeDir, "plugins", "official", id),
	}
	for _, dir := range candidates {
		manifest := filepath.Join(dir, "plugin.json")
		if st, statErr := os.Stat(manifest); statErr == nil && !st.IsDir() {
			if abs, absErr := filepath.Abs(dir); absErr == nil {
				return abs
			}
			return filepath.Clean(dir)
		}
	}
	return ""
}

// pluginFiles 生成插件包内的文件清单（详情页「插件目录」）。
//
// 第三方插件有真实目录，直接走文件系统；官方插件在源码/Electron resources
// 布局里也有真实目录，找不到目录时才用加载单元还原（测试/异常路径）。
func pluginFiles(base, front *host.Script, dir string) []PluginFile {
	if dir != "" {
		return walkPluginFiles(dir)
	}
	sizes := map[string]int64{}
	var manifest *pluginapi.Manifest
	if base != nil && base.Manifest != nil {
		manifest = base.Manifest
	} else if front != nil {
		manifest = front.Manifest
	}
	if manifest != nil {
		if raw, err := json.MarshalIndent(manifest, "", "  "); err == nil {
			sizes["plugin.json"] = int64(len(raw))
		}
	}
	if base != nil {
		if base.Source != "" {
			// 官方后端插件的 Source 就是构建产物 dist/index.js。
			sizes["dist/index.js"] = int64(len(base.Source))
		}
		for name, data := range base.StaticAssets {
			sizes[strings.TrimPrefix(name, "./")] = int64(len(data))
		}
		if base.Readme != "" {
			sizes["README.md"] = int64(len(base.Readme))
		}
	}
	if front != nil {
		for name, src := range front.Bundle {
			sizes[strings.TrimPrefix(name, "./")] = int64(len(src))
		}
		for name, data := range front.StaticAssets {
			path := strings.TrimPrefix(name, "./")
			// 官方前端包的 StaticAssets 键相对 static/ 目录（todo.css）；
			// 目录视图还原成包内真实路径 static/todo.css。
			if !strings.HasPrefix(path, "static/") && !strings.HasPrefix(path, "skills/") {
				path = "static/" + path
			}
			sizes[path] = int64(len(data))
		}
		if front.Readme != "" {
			sizes["README.md"] = int64(len(front.Readme))
		}
	}
	return sortedPluginFiles(sizes)
}

func walkPluginFiles(dir string) []PluginFile {
	sizes := map[string]int64{}
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".cache":
				return fs.SkipDir
			}
			if strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		sizes[rel] = info.Size()
		count++
		if count >= maxPluginFiles {
			return fs.SkipAll
		}
		return nil
	})
	return sortedPluginFiles(sizes)
}

func sortedPluginFiles(sizes map[string]int64) []PluginFile {
	out := make([]PluginFile, 0, len(sizes))
	for path, size := range sizes {
		out = append(out, PluginFile{Path: path, Size: size, Depth: strings.Count(path, "/")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// pluginReadme 读插件 README；没有 README 时回退为清单 description。
func pluginReadme(dir string, sc *host.Script) string {
	if sc != nil && strings.TrimSpace(sc.Readme) != "" {
		return clipBytes(sc.Readme, maxReadmeBytes)
	}
	if dir != "" {
		for _, name := range []string{pluginReadmeDoc, "readme.md", "README.MD"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				return clipBytes(string(data), maxReadmeBytes)
			}
		}
	}
	if sc != nil && sc.Manifest != nil {
		return sc.Manifest.Description
	}
	return ""
}

func clipBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n\n…（README 已截断）"
}

// pluginViewOf 把一份脚本 + 元信息 + 运行时状态合成一行视图。
// recOK 为 false 表示 plugin_meta 还没有这一行（缺省 enabled）。
func pluginViewOf(sc host.Script, source string, p *discovery.Plugin, rec sqlite.PluginMetaRecord, recOK bool, asm corecontracts.Assembly) PluginView {
	v := PluginView{Source: source}
	m := sc.Manifest
	if m == nil {
		return v
	}
	v.ID, v.Name, v.Version = m.ID, m.Name, m.Version
	v.APIVersion = m.APIVersion
	v.Trust = string(m.Trust)
	v.Enabled = !recOK || rec.Enabled
	if m.Backend != nil {
		v.Entry = m.Backend.Entry
		for _, cap := range m.Backend.Capabilities {
			v.ManifestCapabilities = append(v.ManifestCapabilities, string(cap))
		}
	}
	if p != nil {
		v.Fingerprint = p.Fingerprint
	} else {
		v.Fingerprint = fingerprintScript(sc)
	}
	if recOK {
		v.LastError = rec.LastError
	}
	if !v.Enabled {
		v.Status = "disabled"
		return v
	}
	if asm != nil {
		if inst, ok := asm.Plugin(m.ID); ok {
			sum := inst.Summary()
			v.Status = sum.Status
			if sum.LastError != "" {
				v.LastError = sum.LastError
			}
			v.Tools = sum.Tools
			v.Hooks = sum.Hooks
			v.Skills = sum.Skills
			return v
		}
	}
	// 已启用但没有运行时结果：装配失败被隔离（last_error 已记录）或尚未装配。
	v.Status = "failed"
	if v.LastError == "" {
		v.LastError = "已启用但未出现在当前装配中"
	}
	return v
}

// viewFor 返回单个插件视图（setEnabled 的返回值）。
func (s *Session) viewFor(ctx context.Context, id string) PluginView {
	for _, v := range s.PluginViews(ctx) {
		if v.ID == id {
			return v
		}
	}
	return PluginView{ID: id, Status: "failed", LastError: "插件不在当前视图里"}
}

// lookupPlugin 找一个插件的脚本、来源与指纹（随包 base 优先，其次前端段，最后第三方）。
func (s *Session) lookupPlugin(id string) (host.Script, string, string, bool) {
	for _, sc := range s.baseScripts {
		if sc.Manifest != nil && sc.Manifest.ID == id {
			return sc, "", fingerprintScript(sc), true
		}
	}
	// 纯前端官方插件只在 frontendScripts 里（baseScripts 只收 backend 段）。
	for _, sc := range s.frontendScripts {
		if sc.Manifest != nil && sc.Manifest.ID == id {
			return sc, "", fingerprintScript(sc), true
		}
	}
	for _, p := range s.discoveredSnapshot() {
		if p.ID() == id {
			return p.Script, p.Source, p.Fingerprint, true
		}
	}
	return host.Script{}, "", "", false
}

// hasNonReadToolsExcluding 报告当前目录里是否仍有其它插件贡献的非只读工具。
func (s *Session) hasNonReadToolsExcluding(ctx context.Context, pluginID string) bool {
	asm := s.Assembly()
	if asm == nil {
		return false
	}
	for _, d := range asm.ToolCatalog().List(ctx) {
		if d.PluginID != pluginID && d.Access != domain.AccessRead {
			return true
		}
	}
	return false
}

// ReloadPlugins 重载插件（plugin.reload）：pluginId 为空重载全部第三方
// （整装配重建、双代切换）；指定第三方 id 时走同一条重建路径；指定随包插件时
// 原地重建其 Runtime（同代，登记表必须与已提交贡献一致）。返回重载后的视图。
func (s *Session) ReloadPlugins(ctx context.Context, pluginID string) ([]PluginView, error) {
	pluginID = strings.TrimSpace(pluginID)
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if pluginID != "" {
		for _, sc := range s.baseScripts {
			if sc.Manifest == nil || sc.Manifest.ID != pluginID {
				continue
			}
			asm := s.Assembly()
			if asm == nil {
				return nil, errors.New("cli: 会话没有装配结果")
			}
			inst, ok := asm.Plugin(pluginID)
			if !ok {
				return nil, fmt.Errorf("cli: 插件 %s 当前未装配（可能已被禁用）", pluginID)
			}
			if err := inst.Reload(ctx); err != nil {
				return nil, err
			}
			// 审计（09 §10：插件重载）：随包插件原地重建同一代 Runtime。
			s.audit(ctx, "plugin.reload", "", pluginID, map[string]any{"pluginId": pluginID, "scope": "plugin"})
			return s.PluginViews(ctx), nil
		}
		found := false
		for _, p := range s.discoveredSnapshot() {
			if p.ID() == pluginID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("cli: 没有发现插件 %q（先安装到 <workspace>/.adago/plugins/<id> 再重载）", pluginID)
		}
	}
	if err := s.reloadAssembly(ctx); err != nil {
		return nil, err
	}
	// 审计（09 §10：插件重载）：pluginId 为空表示整装配重建。
	s.audit(ctx, "plugin.reload", "", pluginID, map[string]any{"pluginId": pluginID, "scope": scopeOf(pluginID)})
	return s.PluginViews(ctx), nil
}

// scopeOf 描述重载范围（审计 detail 用）。
func scopeOf(pluginID string) string {
	if pluginID == "" {
		return "all"
	}
	return "plugin"
}

// SetPluginEnabled 启用/禁用插件（plugin.setEnabled）：先落 plugin_meta，再双代重载。
//
//   - 第三方禁用后从目录摘除（INV-11：禁用即能力消失），启用后恢复；
//   - 随包插件禁用后本次装配容忍缺席（审批策略已移除，不存在「禁用策略插件」门禁）；
//   - 重载失败时回滚 meta 并返回错误（运行中的旧代保持原样）。
func (s *Session) SetPluginEnabled(ctx context.Context, pluginID string, enabled bool) (PluginView, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return PluginView{}, errors.New("cli: 需要 pluginId")
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if _, _, _, ok := s.lookupPlugin(pluginID); !ok {
		return PluginView{}, fmt.Errorf("cli: 没有发现插件 %q", pluginID)
	}
	// 启停状态的真源是 JSON：默认写全局（~/.adago/plugins.json，跨工作区共享）；
	// 工作区已有覆盖文件（.adago/plugins.json）时继续写工作区，保持该工作区的
	// 单独启停语义。SQLite 不再存启用决策。
	wsPath := workspacePluginStatePath(s.cfg.Workspace)
	path := s.globalPluginStatePath()
	if path == "" {
		// 主目录不可用：退回工作区文件，保证启停仍可用。
		path = wsPath
	} else if _, exists, _ := workspacePluginState(s.cfg.Workspace); exists {
		path = wsPath
	}
	if path == wsPath {
		materializeWorkspacePluginState(s.cfg.Workspace)
	}
	prev, hadPrev, err := setPluginStateEntry(path, pluginID, enabled)
	if err != nil {
		return PluginView{}, err
	}
	if err := s.reloadAssembly(ctx); err != nil {
		// 回滚 JSON，与仍在运行的旧代保持一致。
		restorePluginStateEntry(path, pluginID, prev, hadPrev)
		return PluginView{}, err
	}
	// 审计（09 §10：插件禁用/启用）：成功才记，失败路径上面已回滚。
	s.audit(ctx, "plugin.changed", "", pluginID, map[string]any{"enabled": enabled})
	return s.viewFor(ctx, pluginID), nil
}

// manifestVersionOf 取清单版本（缺清单时用占位版本，plugin_meta 需要非空行）。
func manifestVersionOf(sc host.Script) string {
	if sc.Manifest == nil || sc.Manifest.Version == "" {
		return "0.0.0"
	}
	return sc.Manifest.Version
}

// ListPluginMetaOffline 用只读连接读取插件启用状态（adag plugin list 用）。
// 数据库还不存在时返回空表：插件目录可以先安装、后启动。
// 启用决策叠加 JSON 状态（工作区 .adago/plugins.json 覆盖全局 plugins.json）。
func ListPluginMetaOffline(ctx context.Context, workspace, dataDir string) (map[string]sqlite.PluginMetaRecord, error) {
	abs, dir, err := paths(workspace, dataDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(sqlite.StateDBPath(dir)); err != nil {
		return map[string]sqlite.PluginMetaRecord{}, nil
	}
	r, err := sqlite.OpenReadOnly(ctx, sqlite.Config{Workspace: abs, DataDir: dir})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	rows, err := r.ListPluginMeta(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]sqlite.PluginMetaRecord{}
	for _, rec := range rows {
		out[rec.ID] = rec
	}
	wsEntries, wsExists, _ := workspacePluginState(abs)
	globalEntries, _, _ := loadPluginStateFile(globalPluginStatePath())
	apply := func(entries map[string]pluginStateEntry) {
		for id, entry := range entries {
			rec, ok := out[id]
			if !ok {
				rec = sqlite.PluginMetaRecord{ID: id}
			}
			rec.Enabled = entry.Enabled
			rec.UserSet = true
			out[id] = rec
		}
	}
	apply(globalEntries)
	if wsExists {
		apply(wsEntries)
	}
	return out, nil
}

// ActionDef 返回某插件的界面动作定义（M4 action.invoke 数据面）。
func (s *Session) ActionDef(pluginID, action string) (host.ActionDef, bool) {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	return asm.ActionDef(pluginID, action)
}

// CallAction 执行一次前端动作（读/写都走这里；写动作的确认文案在清单里，
// CSRF/Origin 由 Gateway 中间件负责）。input 是 JSON 对象原文。
func (s *Session) CallAction(ctx context.Context, pluginID, action string, input json.RawMessage, threadID string) (json.RawMessage, error) {
	s.mu.Lock()
	asm := s.asm
	base := s.hostBase
	s.mu.Unlock()
	if asm == nil {
		return nil, domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	def, ok := asm.ActionDef(pluginID, action)
	if !ok {
		return nil, domain.NewError(domain.CodePluginDenied,
			"插件 "+pluginID+" 没有界面动作 "+action)
	}
	inst, ok := asm.Plugin(pluginID)
	if !ok {
		return nil, domain.NewError(domain.CodePluginDenied,
			"插件 "+pluginID+" 未加载")
	}
	hc := base
	hc.PluginID = pluginID
	hc.ThreadID = threadID
	hc.Signal = ctx
	hc.Now = time.Now().UTC()
	// 只读动作也按线程命名空间读 state（t:<threadID>/<key>）：计划/任务类插件
	// 的状态是线程级，不能因为前端预取而落到空线程。
	if threadID != "" {
		ctx = withThread(ctx, threadID)
		hc.Signal = ctx
	}
	return inst.CallAction(ctx, def.Handler, input, hc)
}

// HTTPAPIError 是插件 HTTP 路由的业务状态错误（404/405），供 Core RPC 映射。
type HTTPAPIError = corecontracts.HTTPAPIError

// CallHTTPAPI 调用插件注册的动态 HTTP 路由（route）。
// 路由表与插件实例都在当前装配里；404/405 转换成 HTTPAPIError。
func (s *Session) CallHTTPAPI(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string) (json.RawMessage, error) {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	if asm == nil {
		return nil, domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	out, err := asm.CallHTTPAPI(ctx, pluginID, method, path, req, threadID)
	if err != nil {
		return nil, httpAPIError(err)
	}
	return out, nil
}

// CallHTTPAPIStream 执行一次 SSE 路由；每帧通过 onEvent 交给调用方。
// 与 CallHTTPAPI 共用同一套路由表、线程上下文与 404/405 映射；类型不符返回 406。
func (s *Session) CallHTTPAPIStream(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string, onEvent func(host.RouteEvent) error) error {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	if asm == nil {
		return domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	if err := asm.StreamHTTPAPI(ctx, pluginID, method, path, req, threadID, onEvent); err != nil {
		return httpAPIError(err)
	}
	return nil
}

// CheckHTTPAPIStream 校验路由存在且是 SSE（不执行处理器）。
func (s *Session) CheckHTTPAPIStream(pluginID, method, path string) error {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	if asm == nil {
		return domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	if err := asm.CheckHTTPAPIStream(pluginID, method, path); err != nil {
		return httpAPIError(err)
	}
	return nil
}

// httpAPIError 把路由层的判定映射成可读的 HTTP 状态（由 Core RPC 透给 Gateway）。
func httpAPIError(err error) error {
	switch {
	case errors.Is(err, corecontracts.ErrRouteNotFound):
		return &HTTPAPIError{Status: 404, Message: "没有匹配的接口"}
	case errors.Is(err, corecontracts.ErrRouteMethodNotAllowed):
		return &HTTPAPIError{Status: 405, Message: "该路径不支持此方法"}
	case errors.Is(err, corecontracts.ErrRouteStreaming):
		return &HTTPAPIError{Status: 406, Message: "该接口是 SSE，请用 EventSource 订阅"}
	case errors.Is(err, corecontracts.ErrRouteNotStreaming):
		return &HTTPAPIError{Status: 406, Message: "该接口不是 SSE"}
	}
	return err
}
