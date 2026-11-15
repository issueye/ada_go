package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/agent_core/plugins/provider"
	"github.com/adag/ada-go/internal/common/ports"
)

// providerConfigStore 是供应商配置的存储接口：provider.Store 是 legacy 文件实现，
// provider.GlobalStore 用 global.config 并兼容旧文件。
type providerConfigStore interface {
	Load() ([]provider.Config, bool, error)
	Save([]provider.Config) error
	Path() string
}

func newProviderConfigStore(path string) providerConfigStore {
	if gs, err := provider.NewGlobalStore(path); err == nil {
		return gs
	} else {
	}
	return provider.NewStore(path)
}

// ModelRef 返回当前生效模型。
func (s *Session) ModelRef() ports.ModelRef { return s.modelRef() }

// ModelError 返回模型配置问题（nil 表示可用）。
func (s *Session) ModelError() error { return s.modelErr }

// ProviderIDs 返回当前可用的 provider 实例 id（model.list 数据源）。
func (s *Session) ProviderIDs() []string {
	asm := s.Assembly()
	if asm == nil || asm.ProviderRegistry() == nil {
		return nil
	}
	return asm.ProviderRegistry().Providers()
}

// ProviderModels 返回某个 provider 的模型能力。
func (s *Session) ProviderModels(providerID string) []host.ModelInfo {
	asm := s.Assembly()
	if asm == nil || asm.ProviderRegistry() == nil {
		return nil
	}
	return asm.ProviderRegistry().Models(providerID)
}

// ProviderConfigs 返回当前供应商配置（provider.list 数据源）。
func (s *Session) ProviderConfigs() []provider.Config {
	asm := s.Assembly()
	if asm == nil || asm.ProviderRegistry() == nil {
		return nil
	}
	return asm.ProviderRegistry().Configs()
}

// ResolveModel 在装配好的 provider 目录里解析 provider/model，并补齐能力位。
// 返回 false 表示 provider 或 model 未在 asm.ProviderRegistry() 中声明。
func (s *Session) ResolveModel(providerID, modelID string) (ports.ModelRef, bool) {
	if providerID == "" || modelID == "" {
		return ports.ModelRef{}, false
	}
	asm := s.Assembly()
	if asm == nil || asm.ProviderRegistry() == nil {
		return ports.ModelRef{}, false
	}
	for _, id := range s.asm.ProviderRegistry().Providers() {
		if id != providerID {
			continue
		}
		for _, m := range s.asm.ProviderRegistry().Models(id) {
			if m.ID != modelID {
				continue
			}
			return ports.ModelRef{
				ProviderID:       id,
				ModelID:          m.ID,
				ContextWindow:    int64(m.ContextWindow),
				MaxOutput:        int64(m.MaxOutput),
				Input:            append([]string(nil), m.Input...),
				SupportsTools:    m.SupportsTools,
				SupportsThinking: m.SupportsThinking,
				ThinkingLevel:    firstThinkingLevel(m.ThinkingLevels),
			}, true
		}
	}
	return ports.ModelRef{}, false
}

// SetWorkspaceModel 设置工作区级模型覆盖（调用方负责先校验 provider/model）。
func (s *Session) SetWorkspaceModel(ref ports.ModelRef) {
	s.mu.Lock()
	saved := ref
	s.workspaceMod = &saved
	s.mu.Unlock()
}

// SetThreadModel 设置线程级模型覆盖（调用方负责先校验 provider/model）。
func (s *Session) SetThreadModel(threadID string, ref ports.ModelRef) {
	if threadID == "" {
		return
	}
	s.mu.Lock()
	if s.threadModels == nil {
		s.threadModels = map[string]ports.ModelRef{}
	}
	s.threadModels[threadID] = ref
	s.mu.Unlock()
}

// ForgetThreadModel 清理线程级模型覆盖（线程删除时调用，避免 map 单调增长）。
func (s *Session) ForgetThreadModel(threadID string) {
	s.mu.Lock()
	delete(s.threadModels, threadID)
	s.mu.Unlock()
	s.forgetThreadModelState(threadID)
}

// ModelRefFor 返回线程当前生效模型；threadID 为空时返回工作区级模型。
func (s *Session) ModelRefFor(threadID string) ports.ModelRef {
	s.mu.Lock()
	override := s.threadOverrideLocked(threadID)
	s.mu.Unlock()
	if override != nil {
		if ref, ok := s.ResolveModel(override.ProviderID, override.ModelID); ok {
			if override.ThinkingLevel != "" {
				ref.ThinkingLevel = override.ThinkingLevel
			}
			return ref
		}
		return *override
	}
	return s.defaultModelRef()
}

// firstThinkingLevel 返回模型的第一档推理等级；没有档位时为空。
func firstThinkingLevel(levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	return levels[0]
}

// providerCatalog 实现 host.Context.Providers（capability: model.admin）。
// 返回所有配置（含停用项，页面需要展示开关）；API Key 只给「是否配置 + 尾号」。
func (s *Session) providerCatalog() host.ProviderCatalog {
	s.RefreshProviderConfigs()
	s.providersMu.Lock()
	cfgs := cloneProviderConfigs(s.providerConfigs)
	s.providersMu.Unlock()
	cat := host.ProviderCatalog{Providers: make([]host.ProviderInfo, 0, len(cfgs))}
	for _, c := range cfgs {
		cat.Providers = append(cat.Providers, s.providerInfo(c))
	}
	cur := s.ModelRef()
	cat.Current = host.ModelRef{ProviderID: cur.ProviderID, ModelID: cur.ModelID, ThinkingLevel: cur.ThinkingLevel}
	return cat
}

// selectModelFromPlugin 实现 host.Context.SelectModel（capability: model.admin）。
// 与 model.set 同一套校验：provider/model 必须在当前装配里存在；线程级切换先校验线程。
func (s *Session) selectModelFromPlugin(ctx context.Context, providerID, modelID, threadID string) (host.ModelRef, error) {
	ref, ok := s.ResolveModel(providerID, modelID)
	if !ok {
		return host.ModelRef{}, fmt.Errorf("模型 %s/%s 未在装配中声明", providerID, modelID)
	}
	if threadID != "" {
		if _, err := s.store.GetThread(ctx, threadID); err != nil {
			return host.ModelRef{}, err
		}
		if err := s.SaveThreadModel(ctx, threadID, ref); err != nil {
			return host.ModelRef{}, err
		}
	} else {
		if err := s.SaveWorkspaceModel(ctx, ref); err != nil {
			return host.ModelRef{}, err
		}
	}
	return host.ModelRef{ProviderID: ref.ProviderID, ModelID: ref.ModelID, ThinkingLevel: ref.ThinkingLevel}, nil
}

// migrateProviderThinkingLevels 把旧的单档 thinkingLevel 迁移成 thinkingLevels。
func migrateProviderThinkingLevels(cfgs []provider.Config) {
	for i := range cfgs {
		for j := range cfgs[i].Models {
			m := &cfgs[i].Models[j]
			if len(m.ThinkingLevels) == 0 && m.ThinkingLevel != "" {
				if level, err := provider.NormalizeThinkingLevel(m.ThinkingLevel); err == nil &&
					level != "" && level != "off" {
					m.ThinkingLevels = []string{level}
					m.SupportsThinking = true
				}
			}
			m.ThinkingLevel = ""
		}
	}
}

// defaultProvidersPath 返回跨工作区共享的供应商配置位置。
// 用户主目录不可用时退回当前数据目录，保证 Core 仍能启动。
func defaultProvidersPath(dataDir string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(dataDir, "providers.json")
	}
	return filepath.Join(home, ".adago", "providers.json")
}

// applyProviderConfigs 把共享的 providers.json 注入当前装配的 Registry。
// 首次启动先迁移旧的「每工作区一份」配置，避免用户升级后丢失已配置的供应商；
// 没有可迁移配置时才从适配器插件播种（保留旧产品开箱即用的行为）。
// 配置读取/写入失败只记警告：供应商配置坏掉不应该让整个 Core 起不来。
func (s *Session) applyProviderConfigs(asm corecontracts.Assembly) {
	if asm == nil || asm.ProviderRegistry() == nil {
		return
	}
	cfgs, exists, err := s.providerStore.Load()
	if err != nil {
		s.logWarn("供应商配置读取失败，沿用插件默认: " + err.Error())
		return
	}
	if !exists {
		if legacy, ok := s.legacyProviderConfigs(asm); ok {
			cfgs = legacy
		} else {
			cfgs = seedProviderConfigs(asm, s.BaseURL(), s.hasSecret)
		}
		// 播种为空时不落盘：保留「尚未初始化」状态，之后打开其他工作区
		// 仍有机会迁移到那里已有的用户配置。
		if len(cfgs) > 0 {
			if err := s.providerStore.Save(cfgs); err != nil {
				s.logWarn("供应商配置首次写入失败: " + err.Error())
			}
		}
	}
	migrateProviderThinkingLevels(cfgs)
	asm.ProviderRegistry().SetConfigs(cfgs)
	s.providersMu.Lock()
	s.providerConfigs = cloneProviderConfigs(cfgs)
	s.providersMu.Unlock()
	s.ensureCurrentModel(asm)
}

// RefreshProviderConfigs 重新读取共享供应商配置。
// 多工作区共用一个 providers.json 时，其他 Core 保存后本 Core 需要刷新，
// 否则模型列表/供应商页会一直停留在启动时的快照。文件不存在或损坏时保留现状。
func (s *Session) RefreshProviderConfigs() {
	asm := s.Assembly()
	if asm == nil || asm.ProviderRegistry() == nil {
		return
	}
	cfgs, exists, err := s.providerStore.Load()
	if err != nil || !exists {
		return
	}
	migrateProviderThinkingLevels(cfgs)
	asm.ProviderRegistry().SetConfigs(cfgs)
	s.providersMu.Lock()
	s.providerConfigs = cloneProviderConfigs(cfgs)
	s.providersMu.Unlock()
	s.ensureCurrentModel(asm)
}

// legacyProviderConfigs 查找升级前按工作区保存的供应商配置。
// 优先当前工作区；否则扫描同级工作区，取最近修改的一份用户配置。
// 只有确实含用户改动（密钥/代理/请求头/自定义端点或供应商）才迁移，
// 避免把旧版本自动播种的默认项带进共享配置。
func (s *Session) legacyProviderConfigs(asm corecontracts.Assembly) ([]provider.Config, bool) {
	path := s.providerStore.Path()
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	current := filepath.Join(s.cfg.DataDir, "providers.json")
	if samePath(path, current) {
		return nil, false // 本来就用工作区配置，不需要迁移
	}
	if cfgs, ok := s.loadUserProviderConfigs(asm, current); ok {
		return cfgs, true
	}
	root := filepath.Dir(s.cfg.DataDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	var best []provider.Config
	var bestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name(), "providers.json")
		if samePath(candidate, path) {
			continue
		}
		cfgs, ok := s.loadUserProviderConfigs(asm, candidate)
		if !ok {
			continue
		}
		info, statErr := os.Stat(candidate)
		if statErr != nil {
			continue
		}
		if best == nil || info.ModTime().After(bestTime) {
			best = cfgs
			bestTime = info.ModTime()
		}
	}
	return best, best != nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return strings.EqualFold(aa, bb)
}

// loadUserProviderConfigs 读取一份旧配置；没有用户改动时视为「可重新播种」。
// 适配器默认端点由 provider 插件声明；仍是默认端点/无密钥的条目是旧版本自动播种的，
// 迁移时丢弃，避免升级后又冒出一套未配置的供应商。
func (s *Session) loadUserProviderConfigs(asm corecontracts.Assembly, path string) ([]provider.Config, bool) {
	cfgs, exists, err := provider.NewStore(path).Load()
	if err != nil || !exists {
		return nil, false
	}
	out := make([]provider.Config, 0, len(cfgs))
	for _, c := range cfgs {
		if s.userModifiedProvider(asm, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func (s *Session) userModifiedProvider(asm corecontracts.Assembly, c provider.Config) bool {
	if strings.TrimSpace(c.APIKey) != "" || strings.TrimSpace(c.Proxy) != "" ||
		len(c.Headers) > 0 || c.Disabled {
		return true
	}
	meta, ok := adapterMetaIn(asm, c.Protocol)
	if !ok {
		return true
	}
	base := strings.TrimSpace(c.BaseURL)
	return base != "" && !strings.EqualFold(base, meta.DefaultBaseURL)
}

// seedProviderConfigs 用已装配的适配器声明播种默认供应商。
// 端点、密钥名与显示名由 provider 插件声明；只有显式 BaseURL 或对应密钥已配置时才播种，
// 否则新工作区会凭空出现一套不可用的模型（用户并没有配置过这个供应商）。
func seedProviderConfigs(asm corecontracts.Assembly, baseURL string, hasCredential func(string) bool) []provider.Config {
	out := make([]provider.Config, 0, len(asm.ProviderRegistry().AdapterIDs()))
	for _, id := range asm.ProviderRegistry().AdapterIDs() {
		meta, ok := asm.ProviderRegistry().AdapterMeta(id)
		if !ok || len(meta.Models) == 0 {
			continue
		}
		protocol := meta.Protocol
		cfgBase := baseURL
		if cfgBase == "" {
			if meta.SecretName == "" || hasCredential == nil || !hasCredential(meta.SecretName) {
				continue
			}
			cfgBase = meta.DefaultBaseURL
		}
		name := meta.DisplayName
		if name == "" {
			name = id
		}
		cfg := provider.Config{
			ID: id, Name: name, Protocol: protocol,
			BaseURL: cfgBase, Models: make([]provider.Model, 0, len(meta.Models)),
		}
		for _, m := range meta.Models {
			cfg.Models = append(cfg.Models, provider.Model{
				ID: m.ID, DisplayName: m.DisplayName,
				ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
				Input: m.Input, SupportsTools: m.SupportsTools,
				SupportsThinking: m.SupportsThinking,
			})
		}
		out = append(out, cfg)
	}
	return out
}

// hasSecret 报告某个密钥名是否已通过环境变量/secrets 提供。
func (s *Session) hasSecret(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	_, ok := s.secret(name)
	return ok
}

// adapterMetaForProtocol 按协议找当前装配里的适配器元数据。
func (s *Session) adapterMetaForProtocol(protocol provider.Protocol) (provider.AdapterMeta, bool) {
	return adapterMetaIn(s.Assembly(), protocol)
}

func adapterMetaIn(asm corecontracts.Assembly, protocol provider.Protocol) (provider.AdapterMeta, bool) {
	if asm == nil || asm.ProviderRegistry() == nil {
		return provider.AdapterMeta{}, false
	}
	adapterID, ok := asm.ProviderRegistry().AdapterIDForProtocol(protocol)
	if !ok {
		return provider.AdapterMeta{}, false
	}
	return asm.ProviderRegistry().AdapterMeta(adapterID)
}

// ensureCurrentModel 在供应商/模型被删改后保证当前选择仍然有效；无效时选第一个可用模型。
func (s *Session) ensureCurrentModel(asm corecontracts.Assembly) {
	if asm == nil || asm.ProviderRegistry() == nil || s.cfg.Fake || s.cfg.ModelClient != nil {
		return
	}
	cur := s.ModelRef()
	if modelUsable(asm, cur.ProviderID, cur.ModelID) {
		return
	}
	for _, pid := range asm.ProviderRegistry().Providers() {
		models := asm.ProviderRegistry().Models(pid)
		if len(models) == 0 {
			continue
		}
		m := models[0]
		s.SetWorkspaceModel(ports.ModelRef{
			ProviderID: pid, ModelID: m.ID,
			ContextWindow: int64(m.ContextWindow), MaxOutput: int64(m.MaxOutput),
			SupportsTools: m.SupportsTools, SupportsThinking: m.SupportsThinking,
			ThinkingLevel: firstThinkingLevel(m.ThinkingLevels),
		})
		return
	}
}

func modelUsable(asm corecontracts.Assembly, providerID, modelID string) bool {
	for _, pid := range asm.ProviderRegistry().Providers() {
		if pid != providerID {
			continue
		}
		for _, m := range asm.ProviderRegistry().Models(pid) {
			if m.ID == modelID {
				return true
			}
		}
	}
	return false
}

// providerInfo 在配置信息上补充「环境变量/secrets 已提供密钥」的状态。
func (s *Session) providerInfo(c provider.Config) host.ProviderInfo {
	info := providerInfoOf(c)
	info.Available = true
	if asm := s.Assembly(); asm != nil && asm.ProviderRegistry() != nil {
		info.Available = asm.ProviderRegistry().ConfigAvailable(c.ID)
	}
	if info.HasAPIKey {
		return info
	}
	name := ""
	if meta, ok := s.adapterMetaForProtocol(c.Protocol); ok {
		name = meta.SecretName
	}
	if s.hasSecret(name) {
		info.HasAPIKey = true
		info.APIKeyHint = "环境变量/secrets"
	}
	return info
}

// normalizeProviderProxy 校验供应商代理地址；空表示直连。
func normalizeProviderProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("代理地址不合法，需要 http(s):// 或 socks5:// 地址")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5":
		return raw, nil
	default:
		return "", fmt.Errorf("代理只支持 http/https/socks5，得到 %s", u.Scheme)
	}
}

// normalizeProviderHeaders 校验自定义请求头：拒绝 Host/Content-Length，
// 限制数量与长度，避免把连接级字段写坏。
func normalizeProviderHeaders(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > 32 {
		return nil, errors.New("自定义请求头最多 32 个")
	}
	out := make(map[string]string, len(in))
	for rawKey, rawValue := range in {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		if len(key) > 128 || len(rawValue) > 4096 {
			return nil, fmt.Errorf("请求头 %q 过长", key)
		}
		if strings.ContainsAny(key, " \t\r\n:") {
			return nil, fmt.Errorf("请求头名 %q 含非法字符", key)
		}
		switch strings.ToLower(key) {
		case "host", "content-length":
			return nil, fmt.Errorf("不能自定义连接级请求头 %q", key)
		}
		out[key] = strings.TrimSpace(rawValue)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func normalizeProviderModels(in []host.ProviderModel) ([]provider.Model, error) {
	out := make([]provider.Model, 0, len(in))
	seen := map[string]bool{}
	for _, m := range in {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return nil, errors.New("模型 id 不能为空")
		}
		if len(id) > 200 {
			return nil, errors.New("模型 id 太长")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		input := m.Input
		if len(input) == 0 {
			input = []string{"text"}
		}
		allowed := input[:0]
		for _, v := range input {
			if v == "text" || v == "image" {
				allowed = append(allowed, v)
			}
		}
		if m.ContextWindow < 0 || m.MaxOutput < 0 {
			return nil, fmt.Errorf("模型 %s 的窗口/输出上限不能为负数", id)
		}
		levels, err := provider.NormalizeThinkingLevels(m.ThinkingLevels)
		if err != nil {
			return nil, fmt.Errorf("模型 %s: %w", id, err)
		}
		out = append(out, provider.Model{
			ID: id, DisplayName: strings.TrimSpace(m.DisplayName),
			ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
			Input: allowed, SupportsTools: m.SupportsTools,
			SupportsThinking: m.SupportsThinking || len(levels) > 0,
			ThinkingLevels:   levels, Disabled: m.Disabled,
		})
	}
	return out, nil
}

func providerInfoOf(c provider.Config) host.ProviderInfo {
	info := host.ProviderInfo{
		ID: c.ID, DisplayName: c.Name, Protocol: string(c.Protocol),
		BaseURL: c.BaseURL, Proxy: c.Proxy, Headers: cloneHeaders(c.Headers), Disabled: c.Disabled,
		HasAPIKey:  strings.TrimSpace(c.APIKey) != "",
		APIKeyHint: maskAPIKey(c.APIKey),
		Models:     make([]host.ProviderModel, 0, len(c.Models)),
	}
	for _, m := range c.Models {
		info.Models = append(info.Models, host.ProviderModel{
			ID: m.ID, DisplayName: m.DisplayName,
			ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
			Input: m.Input, SupportsTools: m.SupportsTools,
			SupportsThinking: m.SupportsThinking, ThinkingLevels: append([]string(nil), m.ThinkingLevels...),
			Disabled: m.Disabled,
		})
	}
	return info
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "已配置"
	}
	return "••••" + key[len(key)-4:]
}

func cloneProviderConfigs(in []provider.Config) []provider.Config {
	out := make([]provider.Config, 0, len(in))
	for _, c := range in {
		out = append(out, c.Clone())
	}
	return out
}

func (s *Session) threadOverrideLocked(threadID string) *ports.ModelRef {
	if threadID != "" {
		if ref, ok := s.threadModels[threadID]; ok {
			return &ref
		}
	}
	if s.workspaceMod != nil {
		ref := *s.workspaceMod
		return &ref
	}
	return nil
}

// modelRef 由产品/CLI 配置与 provider 声明的能力合成（工作区级）。
func (s *Session) modelRef() ports.ModelRef { return s.ModelRefFor("") }

// defaultModelRef 是没有任何覆盖时的模型选择（--fake > 首个可用 provider 的首个模型）。
func (s *Session) defaultModelRef() ports.ModelRef {
	asm := s.Assembly()
	if s.cfg.Fake || s.cfg.ModelClient != nil {
		return ports.ModelRef{ProviderID: "fake", ModelID: "fake-1", ContextWindow: 128000, SupportsTools: true}
	}
	var providerIDs []string
	if asm != nil && asm.ProviderRegistry() != nil {
		providerIDs = asm.ProviderRegistry().Providers()
	}
	providerID := strings.TrimSpace(s.cfg.Provider)
	if providerID == "" && len(providerIDs) > 0 {
		providerID = providerIDs[0]
	}
	modelID := strings.TrimSpace(s.cfg.Model)
	if modelID == "" && providerID != "" && asm != nil && asm.ProviderRegistry() != nil {
		for _, m := range asm.ProviderRegistry().Models(providerID) {
			modelID = m.ID
			break
		}
	}
	if providerID != "" && modelID != "" {
		if ref, ok := s.ResolveModel(providerID, modelID); ok {
			return ref
		}
	}
	toolCount := 0
	if asm != nil {
		toolCount = len(asm.ToolCatalog().List(context.Background()))
	}
	if providerID == "" {
		return ports.ModelRef{}
	}
	ref := ports.ModelRef{
		ProviderID: providerID, ModelID: modelID,
		ContextWindow: 128000, SupportsTools: toolCount > 0,
	}
	if len(providerIDs) == 1 {
		// 产品可能直接写插件 id；桥支持这种写法，但能力仍来自唯一 provider。
		ref.ProviderID = providerIDs[0]
	}
	return ref
}
