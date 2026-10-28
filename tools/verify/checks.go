package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/diag"
	"github.com/adag/ada-go/internal/engine"
	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/internal/plugins"
	nodemod "github.com/adag/ada-go/internal/plugins/host/node"
	"github.com/adag/ada-go/internal/proto"
)

// specProto 是 spec/proto.json 的只读视图（校验用，不承担生成职责）。
type specProto struct {
	SchemaVersion int    `json:"schemaVersion"`
	ProtoVersion  int    `json:"protoVersion"`
	APIVersion    string `json:"apiVersion"`
	Transport     struct {
		Encoding                   string `json:"encoding"`
		MaxMessageBytes            int    `json:"maxMessageBytes"`
		PingIntervalMS             int    `json:"pingIntervalMs"`
		PongTimeoutMS              int    `json:"pongTimeoutMs"`
		HandshakeTimeoutMS         int    `json:"handshakeTimeoutMs"`
		EventBuffer                int    `json:"eventBuffer"`
		SnapshotFieldTruncateBytes int    `json:"snapshotFieldTruncateBytes"`
		DeltaMergeWindowMS         int    `json:"deltaMergeWindowMs"`
		DeltaMaxLatencyMS          int    `json:"deltaMaxLatencyMs"`
	} `json:"transport"`
	Conventions struct {
		ThreadIDFormat  string   `json:"threadIdFormat"`
		EventIDPrefix   string   `json:"eventIdPrefix"`
		TimestampFormat string   `json:"timestampFormat"`
		ItemKeyPrefixes []string `json:"itemKeyPrefixes"`
	} `json:"conventions"`
	Definitions map[string]json.RawMessage `json:"definitions"`
	ErrorCodes  []struct {
		Code           int    `json:"code"`
		Name           string `json:"name"`
		Retryable      bool   `json:"retryable"`
		DiagnosticArea string `json:"diagnosticArea"`
	} `json:"errorCodes"`
	Methods []struct {
		Name                  string `json:"name"`
		Group                 string `json:"group"`
		Idempotent            bool   `json:"idempotent"`
		RequiresReadyInstance bool   `json:"requiresReadyInstance"`
		DefaultDisabled       bool   `json:"defaultDisabled"`
	} `json:"methods"`
	Events []specEvent `json:"events"`
}

// specEvent 是协议事件条目（载荷保留原文，交给 DTO 绑定表比对）。
type specEvent struct {
	Name               string          `json:"name"`
	Scope              string          `json:"scope"`
	Replayable         bool            `json:"replayable"`
	EntersModelContext bool            `json:"entersModelContext"`
	Payload            json.RawMessage `json:"payload"`
}

func readSpecProto(root string) (*specProto, string, error) {
	path := filepath.Join(root, "spec", "proto.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, path, err
	}
	var sp specProto
	if err := json.Unmarshal(raw, &sp); err != nil {
		return nil, path, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return &sp, path, nil
}

// checkGenerated 保证生成物与唯一手写源一致，并让错误码与 diag 词汇表互相咬合。
func checkGenerated(root string) error {
	sp, path, err := readSpecProto(root)
	if err != nil {
		return err
	}
	var problems []string

	if sp.ProtoVersion != proto.ProtoVersion || sp.SchemaVersion != proto.SchemaVersion || sp.APIVersion != proto.APIVersion {
		problems = append(problems, fmt.Sprintf("版本常量不同步：源 %d/%d/%s，生成 %d/%d/%s",
			sp.SchemaVersion, sp.ProtoVersion, sp.APIVersion, proto.SchemaVersion, proto.ProtoVersion, proto.APIVersion))
	}

	srcMethods := make([]string, len(sp.Methods))
	for i, m := range sp.Methods {
		srcMethods[i] = m.Name
	}
	genMethods := proto.AllMethodNames()
	missing, extra := setDiff(srcMethods, genMethods)
	if len(missing) > 0 || len(extra) > 0 {
		problems = append(problems, fmt.Sprintf("方法集合不一致：生成缺 %v，生成多 %v", missing, extra))
	}
	if len(srcMethods) != len(genMethods) {
		problems = append(problems, fmt.Sprintf("方法数量不一致：源 %d，生成 %d", len(srcMethods), len(genMethods)))
	} else {
		for i := range srcMethods {
			if srcMethods[i] != genMethods[i] {
				problems = append(problems, fmt.Sprintf("方法顺序不一致（第 %d 个）：源 %s，生成 %s", i+1, srcMethods[i], genMethods[i]))
				break
			}
		}
	}

	srcEvents := make([]string, len(sp.Events))
	for i, e := range sp.Events {
		srcEvents[i] = e.Name
	}
	evMissing, evExtra := setDiff(srcEvents, proto.AllEventNames())
	if len(evMissing) > 0 || len(evExtra) > 0 {
		problems = append(problems, fmt.Sprintf("事件集合不一致：生成缺 %v，生成多 %v", evMissing, evExtra))
	}

	// 方法元数据逐项比对（幂等性、就绪要求、默认关闭）。
	for _, m := range sp.Methods {
		gen, ok := proto.LookupMethod(m.Name)
		if !ok {
			continue
		}
		if gen.Idempotent != m.Idempotent {
			problems = append(problems, fmt.Sprintf("%s 的 idempotent 不一致", m.Name))
		}
		if gen.RequiresReadyInstance != m.RequiresReadyInstance {
			problems = append(problems, fmt.Sprintf("%s 的 requiresReadyInstance 不一致", m.Name))
		}
		if gen.DefaultDisabled != m.DefaultDisabled {
			problems = append(problems, fmt.Sprintf("%s 的 defaultDisabled 不一致", m.Name))
		}
		if gen.Group != m.Group {
			problems = append(problems, fmt.Sprintf("%s 的 group 不一致", m.Name))
		}
	}
	for _, e := range sp.Events {
		gen, ok := proto.LookupEvent(e.Name)
		if !ok {
			continue
		}
		if gen.Scope != e.Scope {
			problems = append(problems, fmt.Sprintf("%s 的 scope 不一致：源 %s，生成 %s", e.Name, e.Scope, gen.Scope))
		}
		if gen.Replayable != e.Replayable {
			problems = append(problems, fmt.Sprintf("%s 的 replayable 不一致", e.Name))
		}
		if gen.EntersModelContext != e.EntersModelContext {
			problems = append(problems, fmt.Sprintf("%s 的 entersModelContext 不一致", e.Name))
		}
	}

	// 传输与约定常量。
	t := sp.Transport
	pairs := []struct {
		name string
		src  int
		gen  int
	}{
		{"MaxMessageBytes", t.MaxMessageBytes, proto.MaxMessageBytes},
		{"PingIntervalMS", t.PingIntervalMS, proto.PingIntervalMS},
		{"PongTimeoutMS", t.PongTimeoutMS, proto.PongTimeoutMS},
		{"HandshakeTimeoutMS", t.HandshakeTimeoutMS, proto.HandshakeTimeoutMS},
		{"EventBuffer", t.EventBuffer, proto.EventBuffer},
		{"SnapshotFieldTruncateBytes", t.SnapshotFieldTruncateBytes, proto.SnapshotFieldTruncateBytes},
		{"DeltaMergeWindowMS", t.DeltaMergeWindowMS, proto.DeltaMergeWindowMS},
		{"DeltaMaxLatencyMS", t.DeltaMaxLatencyMS, proto.DeltaMaxLatencyMS},
	}
	for _, p := range pairs {
		if p.src != p.gen {
			problems = append(problems, fmt.Sprintf("传输常量 %s：源 %d，生成 %d", p.name, p.src, p.gen))
		}
	}
	if t.Encoding != proto.Encoding {
		problems = append(problems, fmt.Sprintf("Encoding：源 %q，生成 %q", t.Encoding, proto.Encoding))
	}
	if sp.Conventions.ThreadIDFormat != proto.ThreadIDFormat {
		problems = append(problems, "ThreadIDFormat 不同步")
	}
	if sp.Conventions.EventIDPrefix != proto.EventIDPrefix {
		problems = append(problems, "EventIDPrefix 不同步")
	}
	if sp.Conventions.TimestampFormat != proto.TimestampFormat {
		problems = append(problems, "TimestampFormat 不同步")
	}
	if m, e := setDiff(sp.Conventions.ItemKeyPrefixes, proto.ItemKeyPrefixes); len(m) > 0 || len(e) > 0 {
		problems = append(problems, fmt.Sprintf("ItemKeyPrefixes 不同步：缺 %v 多 %v", m, e))
	}

	// 错误码 ↔ diag 词汇表（8.5：诊断码只在一处维护）。
	if len(sp.ErrorCodes) != len(proto.ErrorCodes) {
		problems = append(problems, fmt.Sprintf("错误码数量：源 %d，生成 %d", len(sp.ErrorCodes), len(proto.ErrorCodes)))
	}
	srcNames := make([]string, 0, len(sp.ErrorCodes))
	for _, c := range sp.ErrorCodes {
		srcNames = append(srcNames, c.Name)
		gen, ok := proto.LookupError(c.Code)
		if !ok {
			problems = append(problems, fmt.Sprintf("错误码 %d(%s) 未生成", c.Code, c.Name))
			continue
		}
		if gen.Name != c.Name {
			problems = append(problems, fmt.Sprintf("错误码 %d 名称不一致：源 %s，生成 %s", c.Code, c.Name, gen.Name))
		}
		if gen.Retryable != c.Retryable {
			problems = append(problems, fmt.Sprintf("错误码 %s 的 retryable 不一致", c.Name))
		}
		if gen.DiagnosticArea != c.DiagnosticArea {
			problems = append(problems, fmt.Sprintf("错误码 %s 的 diagnosticArea 不一致：源 %s，生成 %s", c.Name, c.DiagnosticArea, gen.DiagnosticArea))
		}
		if !diag.Area(c.DiagnosticArea).Valid() {
			problems = append(problems, fmt.Sprintf("错误码 %s 的诊断区域 %q 不在 diag.Areas() 里", c.Name, c.DiagnosticArea))
		}
		entry, ok := diag.For(c.Code)
		if !ok {
			problems = append(problems, fmt.Sprintf("错误码 %d(%s) 在 internal/diag 没有诊断条目", c.Code, c.Name))
			continue
		}
		if entry.Name != c.Name {
			problems = append(problems, fmt.Sprintf("diag 表 %d 绑定到 %s，与源 %s 不符", c.Code, entry.Name, c.Name))
		}
		if string(entry.Diag.Area) != c.DiagnosticArea {
			problems = append(problems, fmt.Sprintf("错误码 %s：diag 区域 %s 与源 %s 不符", c.Name, entry.Diag.Area, c.DiagnosticArea))
		}
		if strings.TrimSpace(entry.NextStep) == "" {
			problems = append(problems, fmt.Sprintf("错误码 %s 的 diag 条目缺少「下一步」文案", c.Name))
		}
	}
	genErrNames := make([]string, 0, len(proto.ErrorCodes))
	for _, c := range proto.ErrorCodes {
		genErrNames = append(genErrNames, c.Name)
	}
	if m, e := setDiff(srcNames, genErrNames); len(m) > 0 || len(e) > 0 {
		problems = append(problems, fmt.Sprintf("错误码集合不同步：生成缺 %v 多 %v", m, e))
	}
	if _, ok := proto.ErrorByName("no_such_code"); ok {
		problems = append(problems, "ErrorByName 查询异常")
	}

	// definitions 必须被引用：没人引用的结构就是死字段。
	refs, err := collectDefinitionRefs(filepath.Join(root, "spec", "proto.json"))
	if err != nil {
		problems = append(problems, err.Error())
	}
	if orphans := unboundDefinitions(sp.Definitions, refs); len(orphans) > 0 {
		problems = append(problems, fmt.Sprintf("definitions 中 %d 个结构既没有被 $ref 使用、也没有 DTO 绑定：%s",
			len(orphans), strings.Join(orphans, ", ")))
	}
	problems = append(problems, checkDTOBindings(sp.Definitions)...)
	problems = append(problems, checkEventDTOs(sp.Events)...)

	if len(problems) > 0 {
		return fmt.Errorf("%s 与生成物有 %d 处漂移:\n  - %s", path, len(problems), strings.Join(problems, "\n  - "))
	}
	return nil
}

// collectDefinitionRefs 统计文件里所有 $ref 目标。
func collectDefinitionRefs(path string) (map[string]int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	out := map[string]int{}
	walkJSON(v, func(key string, val any) {
		if key != "$ref" {
			return
		}
		if s, ok := val.(string); ok {
			out[s]++
		}
	})
	return out, nil
}

func walkJSON(v any, fn func(key string, val any)) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			fn(k, val)
			walkJSON(val, fn)
		}
	case []any:
		for _, val := range t {
			walkJSON(val, fn)
		}
	}
}

// checkDispatch 检查 Core 的方法注册表覆盖协议全集。
func checkDispatch(root string) error {
	s := coreproto.NewServer("verify", "verify::check", os.Getpid())
	if err := s.CheckCoverage(); err != nil {
		return err
	}
	// 反向：dispatch 数量必须等于协议方法数（防止同名覆盖导致缩水）。
	if got, want := len(s.Registered()), len(proto.Methods); got != want {
		return fmt.Errorf("coreproto: 注册了 %d 个方法，协议声明 %d 个", got, want)
	}
	// 派发一次未知方法必须得到 -32601，而不是 panic 或成功。
	resp := s.Handle(nil, proto.Request{JSONRPC: "2.0", Method: "thread.nope"})
	if resp.Error == nil || resp.Error.Code != coreproto.ErrMethodNotFound {
		return fmt.Errorf("coreproto: 未知方法没有被拒绝（%+v）", resp.Error)
	}
	// 事件发射只允许协议声明的 12 个名字。
	if _, err := coreproto.NewEvent("evt.not_real", map[string]any{}); err == nil {
		return fmt.Errorf("coreproto: 未声明的事件名被接受，违反「插件不能扩展协议」（1.5.2）")
	}
	return nil
}

// checkHooks 检查 15 个点位与引擎调用方双向覆盖。
func checkHooks(root string) error {
	points := plugins.HookPoints()
	sites := engine.HookSites()
	names := make([]string, len(points))
	for i, p := range points {
		names[i] = p.Name
	}
	siteNames := make([]string, len(sites))
	for i, s := range sites {
		siteNames[i] = s.Point
	}
	missing, extra := setDiff(names, siteNames)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("钩子接线不完整：%d 个点位没有调用方 %v；%d 个调用方指向未知点位 %v",
			len(missing), missing, len(extra), extra)
	}
	if len(points) != 15 {
		return fmt.Errorf("钩子点位数量是 %d，设计 6.6 与文档声明 15", len(points))
	}
	// 成对点位必须成对（6.6）。
	for _, p := range points {
		if p.Pair == "" {
			continue
		}
		other, ok := plugins.LookupHookPoint(p.Pair)
		if !ok {
			return fmt.Errorf("点位 %s 声明的成对点位 %q 不存在", p.Name, p.Pair)
		}
		if other.Pair != p.Name {
			return fmt.Errorf("点位 %s 与 %s 的成对关系不对称", p.Name, p.Pair)
		}
	}
	// 失败关闭只允许出现在 tool.beforeCall（其它点位失败方向为开）。
	closed := 0
	for _, p := range points {
		if p.FailClosed {
			closed++
			if p.Name != "tool.beforeCall" {
				return fmt.Errorf("点位 %s 被标为失败关闭，与设计 6.6 不符", p.Name)
			}
		}
		site, _ := engine.SiteFor(p.Name)
		if site.Blocking != p.FailClosed {
			return fmt.Errorf("点位 %s 的失败关闭标记与引擎调用方不一致（契约 %v，调用方 %v）", p.Name, p.FailClosed, site.Blocking)
		}
	}
	if closed != 1 {
		return fmt.Errorf("失败关闭点位数量是 %d，设计只允许 tool.beforeCall", closed)
	}
	// 安全点的超时/异常必须是拒绝：deny 只应出现在 tool.beforeCall。
	for _, p := range points {
		for _, a := range p.Allowed {
			if a == plugins.ActionDeny && p.Name != "tool.beforeCall" {
				return fmt.Errorf("点位 %s 允许 deny，违反 6.6（deny 仅 tool.beforeCall）", p.Name)
			}
			if (a == plugins.ActionReplace || a == plugins.ActionAbort) && len(p.BuiltinOnlyActions) == 0 {
				return fmt.Errorf("点位 %s 允许 %s 却没有标成 builtin 专属", p.Name, a)
			}
		}
	}
	return nil
}

// checkSlots 检查 12 个插槽的宿主覆盖。
func checkSlots(root string) error {
	if err := web.CheckSlotCoverage(); err != nil {
		return err
	}
	if n := len(plugins.Slots()); n != 12 {
		return fmt.Errorf("插槽数量是 %d，设计 7.2 声明 12（其中 thread.tray 是 ada-go 新增的第 12 个）", n)
	}
	for _, h := range web.SlotHosts() {
		if strings.TrimSpace(h.Template) == "" || strings.TrimSpace(h.Fallback) == "" {
			return fmt.Errorf("插槽 %s 缺少宿主模板或空贡献回退", h.Slot)
		}
	}
	return nil
}

// checkCapabilities 检查能力位与安装消费者，并与清单 Schema 的枚举比对。
func checkCapabilities(root string) error {
	if err := plugins.CheckCapabilitySites(); err != nil {
		return err
	}
	info, err := loadSchema(filepath.Join(root, "spec", "manifest.schema.json"))
	if err != nil {
		return err
	}
	want := append(plugins.BackendCapabilityNames(), plugins.FrontendCapabilityNames()...)
	sort.Strings(want)

	got := append(
		normalizeEnum(info.enumOf("backend.capabilities")),
		normalizeEnum(info.enumOf("frontend.capabilities"))...,
	)
	if len(got) == 0 {
		// Schema 可能把能力枚举放在 definitions 里；退一步用整体枚举集合。
		seen := map[string]bool{}
		var all []string
		for _, l := range info.Leaves {
			for _, e := range l.Enum {
				if !seen[e] && containsStr(want, e) {
					seen[e] = true
					all = append(all, e)
				}
			}
		}
		got = all
	}
	got = dedupeSorted(got)
	missing, extra := setDiff(want, got)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("能力位与 Schema 枚举不一致：Schema 缺 %v，Schema 多 %v", missing, extra)
	}
	// ui.ask / net.sse / exec 的信任约束必须真的可判定。
	for _, c := range []plugins.Capability{plugins.CapUIAsk, plugins.CapNetSSE, plugins.CapExec} {
		if c.TrustAllows(plugins.TrustThirdParty) {
			return fmt.Errorf("能力 %q 竟然允许第三方，违反 6.2", c)
		}
	}
	if !plugins.CapSubagentInvoke.TrustAllows(plugins.TrustBuiltin) ||
		plugins.CapSubagentInvoke.TrustAllows(plugins.TrustVerified) {
		return fmt.Errorf("subagent.invoke 应只授予 builtin（6.2）")
	}
	return nil
}

// checkManifestFields 检查清单 Schema 字段与 Go 加载结构逐字一致，且每字段有消费者。
func checkManifestFields(root string) error {
	path := filepath.Join(root, "spec", "manifest.schema.json")
	info, err := loadSchema(path)
	if err != nil {
		return err
	}
	if err := info.requireConsumers("spec/manifest.schema.json"); err != nil {
		return err
	}
	schemaPaths := info.paths()
	shape := structPaths(reflect.TypeOf(plugins.Manifest{}))
	// map 字段（backend.hooks、frontend.actions）吸收两侧的子路径：
	// Schema 可能逐点枚举，Go 侧用 map，两边都只在该路径本身比对。
	schemaPaths = absorbSchemaMapChildren(info, schemaPaths, shape.Maps)
	goPaths := absorbGoMapChildren(shape.Paths, shape.Maps)
	missing, extra := setDiff(schemaPaths, goPaths)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("清单字段漂移：Schema 有但 Go 结构没有 %v；Go 结构有但 Schema 没有 %v", missing, extra)
	}
	// 钩子点位在 Schema 里逐点声明（backend.hooks.<point>），未声明的点位调用时拒绝，
	// 所以点位集合必须与注册表完全一致；插槽与角色、信任级别是枚举。
	if err := checkChildNames(info, "backend.hooks", plugins.HookPointNames(), "钩子点位"); err != nil {
		return err
	}
	if err := checkEnumMatches(info, "frontend.slots.slot", plugins.SlotNames(), "插槽名"); err != nil {
		return err
	}
	if err := checkEnumMatches(info, "backend.roles", []string{string(plugins.RoleToolPolicy)}, "插件角色"); err != nil {
		return err
	}
	if err := checkEnumMatches(info, "trust", []string{"builtin", "verified", "third-party"}, "信任级别"); err != nil {
		return err
	}
	return nil
}

// checkChildNames 比对某前缀下的直接子字段名与注册表。
func checkChildNames(info *schemaInfo, prefix string, registry []string, label string) error {
	got := childNames(info, prefix)
	if len(got) == 0 {
		return fmt.Errorf("Schema 里找不到 %s 下的 %s 声明", prefix, label)
	}
	missing, extra := setDiff(registry, got)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("%s 与 Schema 不一致：Schema 缺 %v，Schema 多 %v", label, missing, extra)
	}
	return nil
}

// childNames 返回 prefix 的直接子字段名（按 Parent 判定，属性名可以含点）。
func childNames(info *schemaInfo, prefix string) []string {
	var out []string
	for _, l := range info.Leaves {
		if l.Parent != prefix {
			continue
		}
		out = append(out, strings.TrimPrefix(l.Path, prefix+"."))
	}
	sort.Strings(out)
	return out
}

// checkEnumMatches 在某路径存在枚举时，比对枚举与注册表。
func checkEnumMatches(info *schemaInfo, field string, registry []string, label string) error {
	enum := normalizeEnum(info.enumOf(field))
	if len(enum) == 0 {
		return fmt.Errorf("Schema 里找不到 %s 的枚举（%s 必须封闭声明）", field, label)
	}
	missing, extra := setDiff(registry, enum)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("%s 与 Schema 枚举不一致：Schema 缺 %v，Schema 多 %v", label, missing, extra)
	}
	return nil
}

// checkVNode 比对 VNode Schema 的白名单与限制和 Go 校验器。
func checkVNode(root string) error {
	info, err := loadSchema(filepath.Join(root, "spec", "vnode.schema.json"))
	if err != nil {
		return err
	}
	report := frontend.Report()
	checks := []struct {
		name     string
		schema   []string
		registry []string
		label    string
	}{
		{"element.t", dedupeSorted(normalizeEnum(info.enumOf("element.t"))), report.Tags, "HTML 标签"},
		{"component.n", dedupeSorted(normalizeEnum(info.enumOf("component.n"))), report.Components, "宿主组件"},
		{"elementProps", childNames(info, "elementProps"), report.Attributes, "属性"},
		{"elementProps.type", dedupeSorted(normalizeEnum(info.enumOf("elementProps.type"))), report.InputTypes, "input[type]"},
	}
	var problems []string
	for _, c := range checks {
		if len(c.schema) == 0 {
			problems = append(problems, fmt.Sprintf("Schema 里找不到 %s 的%s白名单（单一真源必须在 spec/）", c.name, c.label))
			continue
		}
		missing, extra := setDiff(c.registry, c.schema)
		if len(missing) > 0 || len(extra) > 0 {
			problems = append(problems, fmt.Sprintf("%s白名单不一致：Schema 缺 %v，Schema 多 %v", c.label, missing, extra))
		}
	}
	limits := map[string]int{
		"maxDepth":       frontend.MaxDepth,
		"maxNodes":       frontend.MaxNodes,
		"maxStringBytes": frontend.MaxStringBytes,
		"maxOutputBytes": frontend.MaxOutputBytes,
	}
	for key, want := range limits {
		got, ok := info.RootNumbers["x-limits/"+key]
		if !ok {
			problems = append(problems, fmt.Sprintf("Schema 的 x-limits 缺少 %s", key))
			continue
		}
		if int(got) != want {
			problems = append(problems, fmt.Sprintf("%s：Schema %d，Go 校验器 %d", key, int(got), want))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("VNode 契约漂移:\n  - %s", strings.Join(problems, "\n  - "))
	}
	// 校验器自身必须拒绝危险标签与事件属性（1.5.6、7.3）。
	for _, bad := range []string{"script", "iframe", "object", "embed", "link", "style", "svg"} {
		if containsStr(report.Tags, bad) {
			return fmt.Errorf("标签 %q 出现在插件白名单里，违反「不能注入原始 HTML/内联样式」（1.5.6）", bad)
		}
	}
	if err := frontend.ValidateNode(frontend.Node{T: "div", P: map[string]any{"onClick": "x"}}); err == nil {
		return fmt.Errorf("校验器接受了事件属性 onClick，违反 7.3")
	}
	if err := frontend.ValidateNode(frontend.Node{T: "div", P: map[string]any{"href": "javascript:alert(1)"}}); err == nil {
		return fmt.Errorf("校验器接受了 javascript: URL，违反 7.3")
	}
	return nil
}

// checkProducts 校验产品声明：字段有消费者、skeleton 是空基座且能启动。
func checkProducts(root string) error {
	info, err := loadSchema(filepath.Join(root, "spec", "product.schema.json"))
	if err != nil {
		return err
	}
	if err := info.requireConsumers("spec/product.schema.json"); err != nil {
		return err
	}
	schemaPaths := info.paths()
	shape := structPaths(reflect.TypeOf(coreapp.Product{}))
	schemaPaths = absorbSchemaMapChildren(info, schemaPaths, shape.Maps)
	goPaths := absorbGoMapChildren(shape.Paths, shape.Maps)
	missing, extra := setDiff(schemaPaths, goPaths)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("产品声明字段漂移：Schema 有但 Go 结构没有 %v；Go 结构有但 Schema 没有 %v", missing, extra)
	}

	files, err := coreapp.DiscoverProducts(filepath.Join(root, "products"))
	if err != nil {
		return err
	}
	if len(files) < 2 {
		return fmt.Errorf("products/ 只有 %d 份声明，M0 要求 coding 与 skeleton 两份", len(files))
	}
	var problems []string
	var skeleton *coreapp.Product
	for _, f := range files {
		p, err := coreapp.LoadProduct(f)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if p.ID == "skeleton" {
			skeleton = p
		}
	}
	if skeleton == nil {
		problems = append(problems, "缺少 id 为 skeleton 的产品声明（空基座验收依据）")
	} else if len(skeleton.Plugins) != 0 {
		problems = append(problems, fmt.Sprintf("skeleton 的 plugins 必须是空数组，实际 %d 项", len(skeleton.Plugins)))
	}
	if skeleton != nil {
		rt, err := coreapp.Assemble(skeleton)
		if err != nil {
			problems = append(problems, fmt.Sprintf("空基座装配失败: %v", err))
		} else {
			if !rt.IsEmptyBase() {
				problems = append(problems, "空基座装配结果不为空")
			}
			if rt.ToolCount != 0 || rt.ProviderCount != 0 {
				problems = append(problems, "空基座出现了工具或供应商，违反 INV-11")
			}
			if len(rt.EmptyStateNotice()) == 0 {
				problems = append(problems, "空基座没有空态提示，界面会白屏（1.4 成功标准）")
			}
		}
	}
	// 策略门禁：有 write/exec/network 工具却没有策略插件时必须失败关闭（5.4、1.5.5）。
	noPolicy := &coreapp.Runtime{Product: skeleton, AccessesInUse: []string{"write"}, PolicyPluginActive: false}
	if err := noPolicy.Check(); err == nil {
		problems = append(problems, "缺少策略插件时写工具没有被拒绝，违反失败关闭")
	}
	if skeleton != nil {
		withPolicy := &coreapp.Runtime{Product: skeleton, AccessesInUse: []string{"write"}, PolicyPluginActive: true}
		if err := withPolicy.Check(); err != nil {
			problems = append(problems, fmt.Sprintf("有策略插件时写工具被产品门禁误拦: %v", err))
		}
	}
	if coding, err := coreapp.LoadProduct(filepath.Join(root, "products", "coding.agent.json")); err != nil {
		problems = append(problems, err.Error())
	} else {
		if len(coding.Plugins) != 10 {
			problems = append(problems, fmt.Sprintf("coding 的官方插件清单是 %d 项，设计 8.6 示例是 10 项", len(coding.Plugins)))
		}
		if !coding.Capabilities.Images || !coding.Capabilities.Skills {
			problems = append(problems, "coding 应启用 skills 与 images（8.6 示例）")
		}
		if coding.Capabilities.Subagents {
			problems = append(problems, "coding 不应启用子智能体（v1.5）")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("产品声明校验有 %d 处问题:\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
	return nil
}

// checkNodeCompat 校验 Node 矩阵、错误码与上游缺口。
func checkNodeCompat(root string) error {
	var problems []string
	if err := nodemod.CheckMatrix(); err != nil {
		problems = append(problems, err.Error())
	}
	caps := map[string]bool{}
	for _, c := range plugins.BackendCapabilities() {
		caps[string(c)] = true
	}
	for _, m := range nodemod.Modules() {
		if m.Capability != "" && !caps[string(m.Capability)] {
			problems = append(problems, fmt.Sprintf("模块 %s 依赖的能力 %q 不在能力白名单里", m.Name, m.Capability))
		}
		if m.Tier == nodemod.TierCapability && strings.TrimSpace(m.Scope) == "" {
			problems = append(problems, fmt.Sprintf("受限 IO 模块 %s 没有写明边界执行点", m.Name))
		}
	}
	// 不安装的模块必须在文档里给出了替代品（6.12.2 第四张表）。
	for _, m := range nodemod.Modules() {
		if m.Tier == nodemod.TierNever && strings.TrimSpace(m.Deviation) == "" {
			problems = append(problems, fmt.Sprintf("模块 %s 不安装却没写替代品", m.Name))
		}
	}
	blocking := nodemod.BlockingGaps()
	if len(blocking) != 3 {
		problems = append(problems, fmt.Sprintf("M1 阻断缺口应是 E-1/E-4/E-5 共 3 项，实际 %d 项", len(blocking)))
	}
	// 错误码双向咬合：6.12.4 表里列出的都必须进注册表；注册表里的都必须在文档中被记载
	// （有些码出现在 6.12.2 的「偏差」列，例如 ERR_ENCODING_NOT_SUPPORTED）。
	tableCodes, mentionedCodes, err := nodeErrorCodesFromDocs(filepath.Join(root, "docs", "12-node-compat.md"))
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		registry := make([]string, 0, len(nodemod.ErrorCodes()))
		for _, e := range nodemod.ErrorCodes() {
			if e.Code != "" {
				registry = append(registry, e.Code)
			}
		}
		missing, _ := setDiff(tableCodes, registry)
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("文档 6.12.4 表里的错误码没进注册表：%v", missing))
		}
		undocumented, _ := setDiff(registry, mentionedCodes)
		if len(undocumented) > 0 {
			problems = append(problems, fmt.Sprintf("注册表里有文档未记载的错误码：%v（先改文档再改实现）", undocumented))
		}
	}
	// Installable 的失败方向：没能力就是链接期失败，不是运行期 undefined。
	if _, err := nodemod.Installable("fs", plugins.TrustThirdParty, nil); err == nil {
		problems = append(problems, "未声明 fs.read 却允许安装 node:fs")
	}
	if _, err := nodemod.Installable("child_process", plugins.TrustThirdParty, []plugins.Capability{plugins.CapExec}); err == nil {
		problems = append(problems, "第三方信任级别拿到了 exec 能力")
	}
	if _, err := nodemod.Installable("http", plugins.TrustBuiltin, []plugins.Capability{plugins.CapNetFetch}); err == nil {
		problems = append(problems, "node:http 被安装，违反 6.12.2")
	}
	if _, err := nodemod.Installable("path", plugins.TrustThirdParty, nil); err != nil {
		problems = append(problems, fmt.Sprintf("纯计算模块 path 被拒: %v", err))
	}
	if len(problems) > 0 {
		return fmt.Errorf("Node 兼容矩阵有 %d 处问题:\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
	return nil
}

// checkUI 在 a_da 基线导入后生效：tokens/CSS 副本必须带来源头注释，且不出现硬编码色与奇数尺寸。
func checkUI(root string) error {
	staticDir := filepath.Join(root, "internal", "gateway", "web", "static")
	tokens := filepath.Join(staticDir, "tokens.css")
	raw, err := os.ReadFile(tokens)
	if err != nil {
		return fmt.Errorf("internal/gateway/web/static/tokens.css 缺失: %w", err)
	}
	text := string(raw)
	if !strings.Contains(text, "a_da") || !strings.Contains(text, "SHA-256") {
		return fmt.Errorf("tokens.css 头部没有记录 a_da 来源与 SHA-256（7.7.1）")
	}
	hex := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	badLines := []string{}
	inComment := false
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		// 注释文本里允许引用具体颜色（7.7.2 约束的是样式规则）。上游 tokens.css
		// 的注释会跨行，单靠「以 /* 开头」会漏掉延续行，因此跟踪块注释状态。
		if inComment {
			if strings.Contains(trimmed, "*/") {
				inComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inComment = true
			}
			continue
		}
		if strings.Contains(line, "--ph-") && strings.HasPrefix(trimmed, "--") {
			continue // token 定义处允许写颜色
		}
		if hex.MatchString(line) {
			badLines = append(badLines, fmt.Sprintf("%d: %s", i+1, trimmed))
		}
	}
	if len(badLines) > 0 {
		return fmt.Errorf("业务 CSS 出现硬编码色（7.7.2：颜色只引用 --ph-*）:\n  - %s", strings.Join(badLines, "\n  - "))
	}
	for _, name := range []string{"app.css", "ui/components.css", "ui/layout.css", "ui/widgets.css", "ui/shell.css", "ui/pages.css"} {
		if _, err := os.Stat(filepath.Join(staticDir, name)); err != nil {
			return fmt.Errorf("缺少 a_da 副本 %s（7.7.1 复用清单）", name)
		}
	}
	return nil
}

func uiBaselinePending(root string) (bool, string) {
	if _, err := os.Stat(filepath.Join(root, "internal", "gateway", "web", "static", "tokens.css")); err != nil {
		return true, "a_da UI 基线尚未导入，M2 生效"
	}
	return false, ""
}

// nodeErrorCodesFromDocs 从 12-node-compat.md 解析错误码：
//   - tableCodes：6.12.4「错误映射」小节里表格第一列的码（一行可能有多个，用 / 分隔）；
//   - mentionedCodes：全文出现过的所有反引号全大写码（宽松集合）。
func nodeErrorCodesFromDocs(path string) (tableCodes, mentionedCodes []string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	text := string(raw)
	codeRe := regexp.MustCompile("`([A-Z][A-Z0-9_]{2,})`")
	for _, m := range codeRe.FindAllStringSubmatch(text, -1) {
		mentionedCodes = append(mentionedCodes, m[1])
	}

	inSection := false
	rowRe := regexp.MustCompile(`^\|\s*(.+?)\s*\|`)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			switch {
			case strings.Contains(trimmed, "错误映射"):
				inSection = true
			case inSection:
				inSection = false
			}
			continue
		}
		if !inSection {
			continue
		}
		m := rowRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		for _, c := range codeRe.FindAllStringSubmatch(m[1], -1) {
			tableCodes = append(tableCodes, c[1])
		}
	}
	if len(tableCodes) == 0 {
		return nil, nil, fmt.Errorf("在 %s 没解析到 6.12.4 错误码表", path)
	}
	return dedupeSorted(tableCodes), dedupeSorted(mentionedCodes), nil
}

// helpers

func normalizeEnum(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.Trim(s, `"`))
	}
	return out
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
