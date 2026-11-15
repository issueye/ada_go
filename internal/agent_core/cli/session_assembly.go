package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/agent_core/devfake"
	"github.com/adag/ada-go/internal/agent_core/engine"
	"github.com/adag/ada-go/internal/agent_core/plugins/discovery"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/agent_core/plugins/provider"
	"github.com/adag/ada-go/internal/common/adapters/sqlite"
	"github.com/adag/ada-go/internal/common/ports"
	"github.com/adag/ada-go/internal/common/toolkit"
)

// ---------------------------------------------------------------------------
// M3 插件平台：发现、启用持久化与双代热重载（06 §6.5、06 §6.9）
// ---------------------------------------------------------------------------

// reloadDrainTimeout 是热重载等待在途轮次结束的上限；超过后仍会切换
// （在途调用可能拿到已关闭的旧代并失败，不再无限等待）。
const reloadDrainTimeout = 30 * time.Second

// closeOldAssemblyTimeout 是关闭旧装配（逐个插件实例等 owner goroutine 退出）的上限。
// 不设上限就是死锁入口：owner goroutine 可能卡在等用户回答的工具调用里。
const closeOldAssemblyTimeout = 5 * time.Second

// sessionBaseScripts 决定随包插件的来源：测试注入优先，否则经注入的 Assembler 取官方包。
// internal 不得 import official/——经组合根转手（Assembler.OfficialScripts）。
func sessionBaseScripts(cfg Config) ([]host.Script, error) {
	if len(cfg.Scripts) > 0 {
		return cfg.Scripts, nil
	}
	return cfg.Assembler.OfficialScripts()
}

// sessionFrontendScripts 返回前端插件脚本：测试注入优先，否则经组合根取官方包。
// Session 只用 id 集合（产品声明允许缺席的集合）；真实渲染在 Gateway 侧。
func sessionFrontendScripts(cfg Config) ([]host.Script, error) {
	if cfg.FrontendScripts != nil {
		return cfg.FrontendScripts, nil
	}
	return cfg.Assembler.FrontendScripts()
}

// installAssembly 把装配结果接进会话：工具执行器、模型选择与引擎依赖里
// 可重建的子集（目录/钩子/模型/Runner/Compactor/HasPolicy/Prompts/Skills）。
// Open 与热重载共用。
// 旧装配在新目录接入后关闭（双代：替换瞬间之后的新调用走新代，旧代在途调用
// 已由 reloadAssembly 的排空等待覆盖）。
func (s *Session) installAssembly(asm corecontracts.Assembly) error {
	exec, err := toolkit.NewExecutor(toolkit.ExecutorConfig{
		Catalog: asm.ToolCatalog(),
		Invoker: asm.ToolInvoker(),
		Clock:   s.clock,
		// Changes 跨热重载复用：检查点账本不随装配换代清零（M5）。
		Changes: s.changes,
		// 检查点归属：threadId/runId 由轮次 ctx 携带（withThread/withRun）。
		CtxMeta: func(ctx context.Context) (string, string) {
			return threadFrom(ctx), runFrom(ctx)
		},
	})
	if err != nil {
		return fmt.Errorf("cli: 建工具执行器失败: %w", err)
	}
	s.applyProviderConfigs(asm)
	model, modelErr := s.pickModel(asm)
	prompts := assemblyPromptSource{frags: promptFragmentsOf(asm)}
	var skillSrc engine.SkillSource
	if s.Product().Capabilities.Skills {
		skillSrc = sessionSkillSource{s: s}
	}
	for _, conflict := range asm.SkillConflicts() {
		s.logWarn("技能同名冲突，工作区优先: " + conflict)
	}
	s.mu.Lock()
	old := s.asm
	s.asm = asm
	s.executor = exec
	s.model, s.modelErr = model, modelErr
	s.deps.Tools = asm.ToolCatalog()
	s.deps.Hooks = asm.HookChain()
	s.deps.Model = model
	s.deps.Runner = exec
	s.deps.Compactor = &engine.ModelCompactor{Model: model, Clock: s.clock, IDs: s.ids}
	s.deps.Prompts = prompts
	s.deps.Skills = skillSrc
	s.mu.Unlock()
	if old != nil && old != asm {
		// 关旧代必须有界：旧插件实例的 owner goroutine 可能正卡在 ask_user 这类等
		// 人回答的调用里，而回答可能要靠同一条被重载堵住的 RPC 送进来。无期限地等
		// 会把重载、那条连接和整个界面一起挂死（06 §6.5 的双代语义不要求无限等）。
		closeCtx, cancel := context.WithTimeout(context.Background(), closeOldAssemblyTimeout)
		defer cancel()
		if err := old.Close(closeCtx); err != nil {
			s.logWarn("关闭旧插件装配超时，已放弃等待（在途调用会拿到已关闭错误）: " + err.Error())
		}
	}
	return nil
}

// pickModel 是装配后的模型选择：测试覆盖 > --fake > provider 插件（与 M1 相同优先级）。
// 第二个返回值非 nil 表示当前装配没有可用模型（没有 provider 且未用 --fake）。
func (s *Session) pickModel(asm corecontracts.Assembly) (ports.ModelClient, error) {
	switch {
	case s.cfg.ModelClient != nil:
		return s.cfg.ModelClient, nil
	case s.cfg.Fake:
		return devfake.New(), nil
	case asm.ProviderCount() > 0:
		return provider.NewRetry(asm.ProviderRegistry()), nil
	default:
		return nil, errors.New("没有可用的 provider：启用 provider 插件或使用 --fake")
	}
}

// assembleInitial 是 Open 的装配路径：随包 base + 启用的第三方。
//
// 这里没有旧代可回退：单个第三方插件装配失败时把它隔离（记 plugin_meta.last_error、
// 状态 failed），其余插件照常装配——Core 不因一个坏插件起不来（6.5「其他插件不受影响」），
// 也不会降级成空基座（随包 base 仍在）。归因不到第三方时让 Open 失败（诚实报错）。
func (s *Session) assembleInitial(ctx context.Context) (corecontracts.Assembly, error) {
	s.refreshEnvVars()
	discovered, warnings := s.discoverThirdParty()
	s.logWarnings(warnings)
	s.mu.Lock()
	s.discovered = discovered
	s.failedPlugins = map[string]string{}
	for _, w := range warnings {
		if w.PluginID != "" {
			s.failedPlugins[w.PluginID] = w.String()
		}
	}
	s.mu.Unlock()
	meta := s.listPluginMeta(ctx)
	productIDs := productIDSet(s.product)
	extras, disabled, optIn := pluginSelection(discovered, meta, s.baseScripts, s.frontendScripts, productIDs)
	asm, err := s.assembler.AssembleWithExtras(ctx, s.product, s.baseScripts, extras, disabled, optIn, s.appDeps)
	if err == nil {
		if serr := s.syncPluginMeta(ctx, discovered, meta, nil); serr != nil {
			return nil, serr
		}
		return asm, nil
	}
	// 隔离重试：先把装配错误归因到第三方插件并摘掉；第三方摘完仍失败时，
	// 再归因到「用户显式启用、产品未声明」的随包插件——它们是用户后加的可选能力，
	// 坏掉不应该让整个产品起不来（产品声明的插件失败仍然是产品失败）。
	remaining := append([]discovery.Plugin(nil), discovered...)
	dropOptIn := map[string]bool{}
	failures := map[string]string{}
	for err != nil {
		id := blameThirdParty(err, remaining)
		isolatedOfficial := false
		if id == "" {
			id = blameOptIn(err, optIn, productIDs, dropOptIn)
			isolatedOfficial = id != ""
		}
		if id == "" {
			return nil, err
		}
		failures[id] = err.Error()
		if s.cfg.Log != nil {
			kind := "第三方插件"
			if isolatedOfficial {
				kind = "可选的随包插件"
			}
			s.cfg.Log("warn", kind+"装配失败，已隔离: "+failures[id], map[string]any{"pluginId": id})
		}
		if isolatedOfficial {
			dropOptIn[id] = true
		} else {
			kept := make([]discovery.Plugin, 0, len(remaining)-1)
			for _, p := range remaining {
				if p.ID() != id {
					kept = append(kept, p)
				}
			}
			remaining = kept
		}
		extras, disabled, optIn = pluginSelection(remaining, meta, s.baseScripts, s.frontendScripts, productIDs)
		for dropID := range dropOptIn {
			delete(optIn, dropID)
		}
		asm, err = s.assembler.AssembleWithExtras(ctx, s.product, s.baseScripts, extras, disabled, optIn, s.appDeps)
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.discovered = remaining
	s.failedPlugins = failures
	s.mu.Unlock()
	if serr := s.syncPluginMeta(ctx, remaining, meta, failures); serr != nil {
		return nil, serr
	}
	return asm, nil
}

// refreshEnvVars 在每次装配前重读环境变量文件（~/.adago/env.json +
// <workspace>/.adago/env.json，工作区覆盖全局）：改完 env.json 后 plugin.reload
// 重建装配即可生效。读取失败只记警告，不影响装配。
func (s *Session) refreshEnvVars() {
	vars, warnings := loadManagedEnv(s.cfg.Workspace, s.cfg.ProvidersPath)
	for _, w := range warnings {
		if s.cfg.Log != nil {
			s.cfg.Log("warn", "环境变量: "+w, nil)
		}
	}
	s.appDeps.EnvVars = vars
}

// discoverThirdParty 扫描工作区与全局插件目录，合并成一份第三方快照：
// 工作区优先、与随包插件同 id 的忽略并记警告（6.1、6.9）。
func (s *Session) discoverThirdParty() ([]discovery.Plugin, []discovery.Warning) {
	var warns []discovery.Warning
	ws, w, err := discovery.LoadDirDetailed(discovery.WorkspaceRoot(s.cfg.Workspace), discovery.SourceWorkspace)
	if err != nil {
		warns = append(warns, discovery.Warning{Message: err.Error()})
	}
	warns = append(warns, w...)
	if root, gerr := discovery.GlobalRoot(); gerr == nil {
		gl, gw, rerr := discovery.LoadDirDetailed(root, discovery.SourceGlobal)
		if rerr != nil {
			warns = append(warns, discovery.Warning{Message: rerr.Error()})
		}
		warns = append(warns, gw...)
		merged, mw := discovery.MergeByPriority(ws, gl)
		ws = merged
		warns = append(warns, mw...)
	} else {
		warns = append(warns, discovery.Warning{Message: gerr.Error()})
	}
	baseIDs := map[string]bool{}
	for _, sc := range s.baseScripts {
		if sc.Manifest != nil {
			baseIDs[sc.Manifest.ID] = true
		}
	}
	out := make([]discovery.Plugin, 0, len(ws))
	for _, p := range ws {
		if baseIDs[p.ID()] {
			warns = append(warns, discovery.Warning{
				PluginID: p.ID(), Dir: p.Dir,
				Message: "与随包插件同 id，忽略磁盘版本（随包插件不可被同名包覆盖，6.9）",
			})
			continue
		}
		out = append(out, p)
	}
	return out, warns
}

// logWarnings 把发现警告送进日志通道（未配置日志时丢弃）。
func (s *Session) logWarnings(warnings []discovery.Warning) {
	if s.cfg.Log == nil {
		return
	}
	for _, w := range warnings {
		s.cfg.Log("warn", "插件发现警告: "+w.String(), map[string]any{"pluginId": w.PluginID})
	}
}

// listPluginMeta 读取 plugin_meta；读失败按空表处理（启用状态缺省 enabled）。
func (s *Session) listPluginMeta(ctx context.Context) map[string]sqlite.PluginMetaRecord {
	out := map[string]sqlite.PluginMetaRecord{}
	rows, err := s.store.ListPluginMeta(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	// 启用状态的真源在 JSON（全局共享 + 工作区覆盖）；SQLite 只剩运行元信息。
	s.overlayPluginState(ctx, out)
	return out
}

// pluginSelection 决定本次装配的第三方、被禁用的随包插件，以及用户显式启用的
// 未声明随包插件（optIn）：
//   - 第三方按 plugin_meta.enabled 过滤（缺省 enabled）；
//   - 随包插件里 enabled=false 且被产品声明的进 disabled；
//   - 产品未声明、但用户显式启用过的随包插件进 optIn，装配时追加。
func pluginSelection(discovered []discovery.Plugin, meta map[string]sqlite.PluginMetaRecord, base, frontend []host.Script, productIDs map[string]bool) ([]host.Script, map[string]bool, map[string]bool) {
	extras := make([]host.Script, 0, len(discovered))
	for _, p := range discovered {
		if rec, ok := meta[p.ID()]; ok && !rec.Enabled {
			continue
		}
		extras = append(extras, p.Script)
	}
	var disabled map[string]bool
	var optIn map[string]bool
	// 统一插件包会同时出现在 base 与 frontend（后端/前端两种加载形态）；
	// 按 id 去重，但前端-only 插件只能从 frontend 看到。
	seen := map[string]bool{}
	bundled := make([]host.Script, 0, len(base)+len(frontend))
	bundled = append(bundled, base...)
	bundled = append(bundled, frontend...)
	for _, sc := range bundled {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		if seen[id] {
			continue
		}
		seen[id] = true
		rec, ok := meta[id]
		if !ok {
			continue
		}
		if rec.UserSet && rec.Enabled {
			if optIn == nil {
				optIn = map[string]bool{}
			}
			optIn[id] = true
		}
		if !productIDs[id] {
			continue
		}
		if !rec.Enabled {
			if disabled == nil {
				disabled = map[string]bool{}
			}
			disabled[id] = true
		}
	}
	return extras, disabled, optIn
}

// productIDSet 把产品声明的插件列表转成集合。
func productIDSet(p *coreapp.Product) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	for _, id := range p.Plugins {
		out[id] = true
	}
	return out
}

// syncPluginMeta 把当前发现的插件写进 plugin_meta（6.9：id+版本+指纹）：
// enabled 只落「产品默认」（声明=启用；第三方缺省启用），user_set 恒为 0 ——
// 启用决策的真源是 JSON 状态（全局 ~/.adago/plugins.json + 工作区
// .adago/plugins.json，见 plugin_state.go），读取时由 listPluginMeta 叠加，
// 出现在 JSON 里即视为用户显式选择并覆盖本行的 enabled。
func (s *Session) syncPluginMeta(ctx context.Context, discovered []discovery.Plugin, meta map[string]sqlite.PluginMetaRecord, failures map[string]string) error {
	productIDs := productIDSet(s.product)
	for _, sc := range s.baseScripts {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		lastErr := ""
		if rec, ok := meta[id]; ok {
			lastErr = rec.LastError
		}
		rec := sqlite.PluginMetaRecord{
			ID: id, Version: sc.Manifest.Version, Enabled: productIDs[id],
			Fingerprint: fingerprintScript(sc), LastError: lastErr,
		}
		if err := s.store.UpsertPluginMeta(ctx, rec); err != nil {
			return fmt.Errorf("cli: 写随包插件 %s 的元信息失败: %w", id, err)
		}
	}
	for _, p := range discovered {
		id := p.ID()
		if id == "" || p.Script.Manifest == nil {
			continue
		}
		lastErr := ""
		if msg, bad := failures[id]; bad {
			lastErr = msg
		}
		rec := sqlite.PluginMetaRecord{
			ID: id, Version: p.Script.Manifest.Version, Enabled: true,
			Fingerprint: p.Fingerprint, LastError: lastErr,
		}
		if err := s.store.UpsertPluginMeta(ctx, rec); err != nil {
			return fmt.Errorf("cli: 写插件 %s 的元信息失败: %w", id, err)
		}
	}
	return nil
}

// blameThirdParty 从装配错误文本里归因到一个第三方插件 id。
// app/host 的装配错误都带「插件 <id>」片段；按 id 长度降序取最长命中，避免前缀误伤。
func blameThirdParty(err error, candidates []discovery.Plugin) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	best := ""
	for _, p := range candidates {
		id := p.ID()
		if id == "" || !strings.Contains(text, "插件 "+id) {
			continue
		}
		if best == "" || len(id) > len(best) {
			best = id
		}
	}
	return best
}

// blameOptIn 把装配错误归因到「用户显式启用、产品未声明」的随包插件。
//
// 只隔离这类可选插件：产品声明里的插件失败仍然是产品失败（声明就是承诺），
// 但用户在插件页临时打开、结果坏掉的可选项不应该让整个 Core 起不来。
func blameOptIn(err error, optIn, productIDs, dropped map[string]bool) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	best := ""
	for id := range optIn {
		if id == "" || productIDs[id] || dropped[id] {
			continue
		}
		if !strings.Contains(text, "插件 "+id) {
			continue
		}
		if best == "" || len(id) > len(best) {
			best = id
		}
	}
	return best
}

// fingerprintScript 是随包插件的指纹：清单身份 + 入口源码的 sha256 前 16 位
// （对应「官方包指纹取嵌入 bundle 的哈希」，6.9）。
func fingerprintScript(sc host.Script) string {
	h := sha256.New()
	if sc.Manifest != nil {
		fmt.Fprintf(h, "%s\x00%s\x00", sc.Manifest.ID, sc.Manifest.Version)
	}
	h.Write([]byte(sc.Source))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// reloadAssembly 重建装配并双代切换（6.9）：
//
//	发现第三方（警告即失败） → 按 plugin_meta 过滤 → Assembler.AssembleWithExtras 构建新代
//	→ 构建失败：保留旧代并返回错误（Core 不降级为空）
//	→ 构建成功：等待在途轮次结束（上限 30s）→ 锁内替换目录与依赖 → 关闭旧代
//
// 随包插件的启停也在这里生效（pluginSelection 把 enabled=false 的随包 id 传给装配）。
func (s *Session) reloadAssembly(ctx context.Context) error {
	s.refreshEnvVars()
	discovered, warnings := s.discoverThirdParty()
	if len(warnings) > 0 {
		msgs := make([]string, 0, len(warnings))
		for _, w := range warnings {
			msgs = append(msgs, w.String())
		}
		return fmt.Errorf("cli: 第三方插件发现失败，保留旧装配: %s", strings.Join(msgs, "; "))
	}
	meta := s.listPluginMeta(ctx)
	extras, disabled, optIn := pluginSelection(discovered, meta, s.baseScripts, s.frontendScripts, productIDSet(s.product))
	newAsm, err := s.assembler.AssembleWithExtras(ctx, s.product, s.baseScripts, extras, disabled, optIn, s.appDeps)
	if err != nil {
		return fmt.Errorf("cli: 重建装配失败，保留旧装配: %w", err)
	}
	// 等在途轮次结束再切换，避免杀掉在途的工具调用（双代语义）。
	s.waitTurnsDrained(reloadDrainTimeout)
	if err := s.installAssembly(newAsm); err != nil {
		// 新目录没接上：旧装配保持原状；新代运行时关掉，不泄漏。
		_ = newAsm.Close(context.Background())
		return err
	}
	s.mu.Lock()
	s.discovered = discovered
	s.failedPlugins = map[string]string{}
	s.mu.Unlock()
	return s.syncPluginMeta(ctx, discovered, meta, nil)
}

// discoveredSnapshot 返回第三方发现快照的副本。
func (s *Session) discoveredSnapshot() []discovery.Plugin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]discovery.Plugin(nil), s.discovered...)
}
