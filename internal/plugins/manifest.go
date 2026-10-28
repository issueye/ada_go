package plugins

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Manifest 是 plugin.json 的加载形态：官方插件与第三方插件共用同一结构（6.2、1.5.5）。
//
// 每个字段的校验点都是它的消费者；adag verify 拿这里的 json tag 集合与
// spec/manifest.schema.json 的 property 集合逐字比对，任一边多出字段即构建失败。
type Manifest struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	APIVersion  int             `json:"apiVersion"`
	Description string          `json:"description"`
	Trust       Trust           `json:"trust"`
	Sig         string          `json:"sig,omitempty"`
	Backend     *Backend        `json:"backend,omitempty"`
	Frontend    *Frontend       `json:"frontend,omitempty"`
	Settings    json.RawMessage `json:"settings,omitempty"`
}

// Backend 是后端清单段；不声明即纯前端插件。
type Backend struct {
	Entry        string                `json:"entry"`
	Capabilities []Capability          `json:"capabilities"`
	Env          []string              `json:"env"`
	Roles        []Role                `json:"roles"`
	Secrets      []string              `json:"secrets"`
	Hooks        map[string]HookAction `json:"hooks"`
	// Pool 保留给后续版本：v1 固定每插件 1 个 Runtime，>1 时装配失败（6.4）。
	Pool int `json:"pool,omitempty"`
}

// Frontend 是前端清单段（7.5）。
type Frontend struct {
	Capabilities []Capability             `json:"capabilities"`
	Slots        []SlotContribution       `json:"slots"`
	Pages        []PageContribution       `json:"pages"`
	Nav          []NavContribution        `json:"nav"`
	Dashboard    []DashboardContribution  `json:"dashboard"`
	Actions      map[string]ActionBinding `json:"actions"`
	Styles       []string                 `json:"styles"`
	Scripts      []string                 `json:"scripts"`
	Assets       string                   `json:"assets"`
}

// SlotContribution 把一个 TSX 模板挂到某个稳定插槽。
type SlotContribution struct {
	Slot     string `json:"slot"`
	Template string `json:"template"`
	Order    int    `json:"order"`
}

// PageContribution 是 /ext/{id}/{slug} 独立页面。
type PageContribution struct {
	Slug     string `json:"slug"`
	Template string `json:"template"`
	Title    string `json:"title"`
}

// NavContribution 是侧栏导航项。
type NavContribution struct {
	Label string `json:"label"`
	Slug  string `json:"slug"`
	Icon  string `json:"icon"`
	Order int    `json:"order"`
}

// DashboardContribution 是概览页卡片。
type DashboardContribution struct {
	Widget   string `json:"widget"`
	Template string `json:"template"`
	Order    int    `json:"order"`
}

// ActionBinding 把界面 binding 名绑到某个插件的后端动作（6.2：跨插件只传 JSON 结果）。
type ActionBinding struct {
	Action    string `json:"action"`
	Read      bool   `json:"read"`
	Backend   string `json:"backend"`
	Confirm   string `json:"confirm,omitempty"`
	TimeoutMS int    `json:"timeoutMs,omitempty"`
}

// DefaultBackendEntry 是 backend.entry 的默认值（6.2）。
const DefaultBackendEntry = "index.ts"

// reservedEnvPrefix 是宿主保留的环境变量前缀；清单声明也会被拒绝（6.13）。
const reservedEnvPrefix = "ADAG_"

// LoadManifest 解析 plugin.json 原文。未知字段的处理策略由宿主决定：主版本一致时
// 忽略并记录（6.2），因此这里不启用 DisallowUnknownFields。
func LoadManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("plugins: 解析清单失败: %w", err)
	}
	if err := m.Validate(""); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate 做装配期校验。dirName 非空时额外要求 id 等于目录名（6.2）。
func (m *Manifest) Validate(dirName string) error {
	if m == nil {
		return fmt.Errorf("plugins: 清单为空")
	}
	if !ValidID(m.ID) {
		return fmt.Errorf("plugins: 插件 id %q 不满足 ^[a-z][a-z0-9-]{1,63}$", m.ID)
	}
	if dirName != "" && m.ID != dirName {
		return fmt.Errorf("plugins: 插件 id %q 必须等于目录名 %q", m.ID, dirName)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("plugins: %s 缺少 name", m.ID)
	}
	if !isSemver(m.Version) {
		return fmt.Errorf("plugins: %s 的 version %q 不是 semver", m.ID, m.Version)
	}
	if m.APIVersion == 0 {
		return fmt.Errorf("plugins: %s 缺少 apiVersion", m.ID)
	}
	if m.APIVersion != APIVersionMajor {
		// 主版本不匹配直接拒绝加载；次版本兼容由宿主忽略未知字段（6.2）。
		return fmt.Errorf("plugins: %s 声明 apiVersion=%d，宿主只支持主版本 %d", m.ID, m.APIVersion, APIVersionMajor)
	}
	if !m.Trust.Valid() {
		if m.Trust == "" {
			m.Trust = TrustThirdParty // 默认第三方、受限（6.2）
		} else {
			return fmt.Errorf("plugins: %s 的 trust %q 未知", m.ID, m.Trust)
		}
	}
	if m.Trust == TrustVerified && strings.TrimSpace(m.Sig) == "" {
		return fmt.Errorf("plugins: %s 声明 trust=verified 但没有 sig", m.ID)
	}
	if m.Backend == nil && m.Frontend == nil {
		return fmt.Errorf("plugins: %s 既没有 backend 也没有 frontend，没有可加载内容", m.ID)
	}
	if err := m.validateBackend(); err != nil {
		return err
	}
	return m.validateFrontend()
}

func (m *Manifest) validateBackend() error {
	b := m.Backend
	if b == nil {
		return nil
	}
	if b.Entry == "" {
		b.Entry = DefaultBackendEntry
	}
	if b.Pool > 1 {
		// 6.4：v1 固定每插件 1 个 Runtime；pool 保留给后续版本，声明即装配失败。
		return fmt.Errorf("plugins: %s 请求 backend.pool=%d，v1 固定每插件 1 个 Runtime", m.ID, b.Pool)
	}
	if !validEntryPath(b.Entry) {
		return fmt.Errorf("plugins: %s 的 backend.entry %q 必须是插件目录内的相对路径（禁止绝对路径、反斜杠、盘符与 . / .. 段）", m.ID, b.Entry)
	}
	if err := ValidateCapabilityList(b.Capabilities, m.Trust, true); err != nil {
		return fmt.Errorf("plugins: %s: %w", m.ID, err)
	}
	for _, name := range b.Env {
		if strings.HasPrefix(name, reservedEnvPrefix) {
			return fmt.Errorf("plugins: %s 的 backend.env 不能声明保留前缀 %q", m.ID, reservedEnvPrefix)
		}
		for _, s := range b.Secrets {
			if s == name {
				return fmt.Errorf("plugins: %s 把 %q 同时写进 env 与 secrets；密钥只能经 secret() 取得（6.13）", m.ID, name)
			}
		}
	}
	for _, r := range b.Roles {
		known := false
		for _, k := range Roles() {
			if k == r {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("plugins: %s 声明未知角色 %q", m.ID, r)
		}
	}
	if err := ValidateHookDeclaration(b.Hooks, m.Trust); err != nil {
		return fmt.Errorf("plugins: %s: %w", m.ID, err)
	}
	return nil
}

func (m *Manifest) validateFrontend() error {
	f := m.Frontend
	if f == nil {
		return nil
	}
	if err := ValidateCapabilityList(f.Capabilities, m.Trust, false); err != nil {
		return fmt.Errorf("plugins: %s: %w", m.ID, err)
	}
	if len(f.Scripts) > 0 && !hasCap(f.Capabilities, CapFrontendScript) {
		return fmt.Errorf("plugins: %s 声明了 scripts 但没有 frontend.script 能力（7.5）", m.ID)
	}
	if m.Trust != TrustBuiltin && m.Trust != TrustVerified && len(f.Scripts) > 0 {
		return fmt.Errorf("plugins: %s 是 %s，不加载插件 JS（7.5、6.9）", m.ID, m.Trust)
	}
	seenSlot := make(map[string]bool, len(f.Slots))
	for _, s := range f.Slots {
		if !IsSlotName(s.Slot) {
			return fmt.Errorf("plugins: %s 声明未知插槽 %q", m.ID, s.Slot)
		}
		if strings.TrimSpace(s.Template) == "" {
			return fmt.Errorf("plugins: %s 在插槽 %s 上没有 template", m.ID, s.Slot)
		}
		key := s.Slot + "\x00" + s.Template
		if seenSlot[key] {
			return fmt.Errorf("plugins: %s 在插槽 %s 上重复贡献 %q", m.ID, s.Slot, s.Template)
		}
		seenSlot[key] = true
	}
	sort.SliceStable(f.Slots, func(i, j int) bool { return f.Slots[i].Order < f.Slots[j].Order })
	for binding, a := range f.Actions {
		if !ValidID(binding) && !isSnakeID(binding) {
			return fmt.Errorf("plugins: %s 的动作 binding %q 命名非法", m.ID, binding)
		}
		if strings.TrimSpace(a.Action) == "" {
			return fmt.Errorf("plugins: %s 的动作 binding %q 没有 action 名", m.ID, binding)
		}
		if strings.TrimSpace(a.Backend) == "" {
			return fmt.Errorf("plugins: %s 的动作 binding %q 没有 backend 目标插件", m.ID, binding)
		}
		if !a.Read && a.Confirm == "" {
			// 写动作必须用户点击并给出确认文案（6.3、7.6）。
			return fmt.Errorf("plugins: %s 的写动作 %q 缺少 confirm 文案", m.ID, binding)
		}
		if a.Read && !hasCap(f.Capabilities, CapBackendRead) {
			return fmt.Errorf("plugins: %s 预取只读动作 %q 但没有 backend.read 能力", m.ID, binding)
		}
	}
	if len(f.Actions) > MaxBackendActions {
		return fmt.Errorf("plugins: %s 声明了 %d 个动作，超过上限 %d（7.5）", m.ID, len(f.Actions), MaxBackendActions)
	}
	for _, p := range f.Pages {
		if strings.TrimSpace(p.Slug) == "" || strings.TrimSpace(p.Template) == "" {
			return fmt.Errorf("plugins: %s 的页面贡献缺少 slug 或 template", m.ID)
		}
	}
	return nil
}

func hasCap(caps []Capability, want Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// validEntryPath 校验 backend.entry 是「插件目录内的相对路径」（6.2）。
//
// 规则刻意保守：允许 index.ts、dist/index.js、src/entry.ts 这类带扩展名与子目录的入口；
// 拒绝绝对路径、反斜杠、盘符、空段与 `.`/`..` 段。条目里的 `..` 与绝对路径是插件
// 逃逸目录的入口，安装期与装配期都要拒绝（09 §9.1 供应链、6.9 安装校验）。
func validEntryPath(entry string) bool {
	if entry == "" || strings.HasPrefix(entry, "/") || strings.ContainsAny(entry, `\:`) {
		return false
	}
	for _, seg := range strings.Split(entry, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// isSnakeID 允许动作 binding 用工具名的命名风格（^[a-z][a-z0-9_]{1,63}$，6.3）。
func isSnakeID(s string) bool {
	if len(s) < 2 || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isSemver 是宽松的 semver 形状检查（数字.数字.数字，允许 -pre / +build）。
func isSemver(s string) bool {
	main, _, _ := strings.Cut(s, "+")
	core, pre, hasPre := strings.Cut(main, "-")
	if hasPre && strings.TrimSpace(pre) == "" {
		return false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
