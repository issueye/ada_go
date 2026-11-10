package host

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	mojsnode "github.com/MoeForge/moejs/node"

	"github.com/adag/ada-go/internal/agent_core/plugins/host/nodemods"
	"github.com/adag/ada-go/internal/common/binpaths"
)

// 本文件实现 host.InstallCapabilities：把每个 Runtime 需要的宿主桥与 node 模块装上（对系统插件与第三方一致）。
//
// 能力位与模块白名单已整体移除（2026-11-10 决策）：宿主不再按清单裁剪「装什么」，
// 也不再有链接期 ERR_ACCESS_DENIED。仍然保留的是**功能边界**本身：
//   - node:fs 的成员包装（上限、审计、工作区根）；
//   - 审计：每次受控调用经 Context.Log 落一条结构化记录，密钥与 token 脱敏；
//   - 超时、输出截断与取消贯穿。
func init() {
	InstallCapabilities = installCapabilities
	registerBuiltinModules()
}

// registerBuiltinModules 把「宿主能装出哪些模块」登记进模块表（load.go 的 Registry）。
//
// 静态 import 走 moduleLoader.hostModule → 这里；动态 import()/import.meta 走
// Importer.Resolver → hostModuleSource。两条路必须给出同一份模块实例，
// 否则同一个 node:fs 会拿到两个绑定（12 §6.12.3）。
func registerBuiltinModules() {
	upstream := func(spec string) func(*CapabilitySet) (*moejs.Module, error) {
		return func(*CapabilitySet) (*moejs.Module, error) {
			// 上游 node 包只服务 fs/fs.promises/buffer；模块是进程级编译一次的。
			return mojsnode.Resolver(nil)(nil, spec)
		}
	}
	fsProvider := func(*CapabilitySet) (*moejs.Module, error) {
		return mojsnode.Resolver(nil)(nil, "node:fs")
	}
	fsPromisesProvider := func(*CapabilitySet) (*moejs.Module, error) {
		return mojsnode.Resolver(nil)(nil, "node:fs/promises")
	}
	virtual := func(name, src string) func(*CapabilitySet) (*moejs.Module, error) {
		return func(*CapabilitySet) (*moejs.Module, error) {
			return compileVirtualModule(name, src)
		}
	}
	RegisterModuleProvider("buffer", upstream("node:buffer"))
	RegisterModuleProvider("fs", fsProvider)
	RegisterModuleProvider("fs/promises", fsPromisesProvider)
	RegisterModuleProvider("process", virtual(moduleProcess, processShim))
	RegisterModuleProvider("child_process", virtual(moduleChildProcess, childProcessShim))
	RegisterModuleProvider(moduleAgentHost, virtual(moduleAgentHost, agentHostShim))
	// 宿主自建的纯计算模块：实现一份注册一份（没实现的保持「尚未提供」）。
	for _, name := range nodemods.Modules() {
		src, ok := nodemods.Source(name)
		if !ok {
			continue
		}
		RegisterModuleProvider(name, virtual(name, src))
	}
}

// 错误码（12 §6.12.4 与 node/matrix.go 的错误码表逐条对应，不新增码）。
const (
	codeAccessDenied       = "ERR_ACCESS_DENIED"
	codeModuleNotFound     = "ERR_MODULE_NOT_FOUND"
	codeNativeAddon        = "ERR_NATIVE_ADDON_UNSUPPORTED"
	codeFileTooLarge       = "ERR_FS_FILE_TOO_LARGE"
	codeMaxBuffer          = "ERR_CHILD_PROCESS_STDIO_MAXBUFFER"
	codeTimerLimit         = "ERR_TIMER_LIMIT"
	codeOutOfRange         = "ERR_OUT_OF_RANGE"
	codeInvalidArgType     = "ERR_INVALID_ARG_TYPE"
	codeTimeout            = "ETIMEDOUT"
	codeAbort              = "ABORT_ERR"
	codeEncodingNotSupport = "ERR_ENCODING_NOT_SUPPORTED"
)

// policyError 是链接期与动态 import 的判定结果。
//
// Go 侧（moejs.Link 的 resolver）只看 Error() 文本，因此文本里必须自带错误码与缺失能力名；
// JS 侧由 thrower 把它还原成带 code 与 capability 字段的 Error 对象。
type policyError struct {
	Code    string
	Message string
	Fields  map[string]any
}

func (e *policyError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return e.Code + ": " + e.Message
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, e.Fields[k]))
	}
	return fmt.Sprintf("%s: %s（%s）", e.Code, e.Message, strings.Join(parts, " "))
}

// jsName 给出该错误码在 JS 侧的 Error 子类名（12 §6.12.4 的 name 列）。
func jsName(code string) string {
	switch code {
	case codeInvalidArgType:
		return "TypeError"
	case codeOutOfRange:
		return "RangeError"
	case codeAbort:
		return "AbortError"
	default:
		return "Error"
	}
}

// jsKind 把错误码映射到 engine 的 Error 家族。
func jsKind(code string) engine.ErrorKind {
	switch code {
	case codeInvalidArgType:
		return engine.KindTypeError
	case codeOutOfRange:
		return engine.KindRangeError
	case codeAbort:
		return engine.KindError // AbortError 靠 name 字段区分
	default:
		return engine.KindError
	}
}

// throwCoded 在 Loop 上构造 Node 形状的错误并返回 r.Throw 的结果（JS 侧可 `err.code` 分支）。
func throwCoded(r *engine.Realm, code, message string, fields map[string]any) error {
	v := r.NewErrorValue(jsKind(code), engine.FromGoString(message))
	o := v.AsObject()
	if o == nil {
		return r.Throw(v)
	}
	name := jsName(code)
	_ = o.SetProp(r, r.KeyFromGoString("name"), moejs.String(name))
	set := func(key string, val any) {
		gv, err := r.FromGo(val)
		if err != nil {
			return
		}
		_ = o.SetProp(r, r.KeyFromGoString(key), gv)
	}
	set("code", code)
	for k, val := range fields {
		set(k, val)
	}
	return r.Throw(v)
}

// baseForLog 只保留路径尾段：宿主绝对路径不进日志与脚本（12 §6.12.1、09 §9.2）。
func baseForLog(p string) string {
	if p == "" {
		return ""
	}
	return path.Base(filepath.ToSlash(p))
}

// capsState 是一个 Runtime 的成员与模块安装结果：上限、审计与宿主回调都在这一份状态里。
//
// ctx.fs / ctx.http / node:fs / node:child_process 共用同一个 capsState，
// 因此「一条访问路径」不是口头承诺：同一 Scope、同一出网边界、同一审计器、同一能力位。
type capsState struct {
	rt     *mojsRuntime
	loop   *mojsLoop
	caps   *CapabilitySet
	limits Limits
	// baseEnv 是环境变量模块注入的子进程基础环境（装配期快照，同名覆盖宿主环境）。
	baseEnv map[string]string
	// binDirs 是服务程序目录（工作区 .adago/bin + 用户级 ~/.adago/bin），
	// 启动子进程时前置到 PATH。
	binDirs []binpaths.Dir

	// inst 由 runFactory 接上：agent:host 的模块体在装配早期求值，
	// 但它的导出函数要等到入口工厂运行时才知道该往哪个实例登记。
	inst *Instance

	mu      sync.Mutex
	hc      Context // 当前调用的宿主上下文（UpdateCallContext 换）
	fs      *fsGuard
	ex      *execState
	proc    *processState
	api     nodeCloser
	process moejs.Value
	agent   moejs.Value
	child   moejs.Value
	timers  *atomic.Int64

	installed map[string]provider
	// managedProcs 是本插件经 ctx.processes 注册、需要随 Runtime 关闭而注销的进程
	// （name → scope）。由 cap_processsupervise.go 维护，CloseCapabilities 消费。
	managedProcs map[string]string
	// processes 是安装期捕获的进程监督桥：关闭时要靠它注销，不能依赖「最近一次调用」
	// 的 Context（那个可能来自另一个插件或一个没有桥的测试上下文）。
	processes ProcessSupervisor
}

// nodeCloser 是 *mojsnode.API 的最小依赖面（便于测试里断言关闭后不再泄漏句柄）。
type nodeCloser interface {
	Close() error
}

// states 按 Runtime 指针记录安装结果：CloseCapabilities 与 agent:host 的取用都走这里。
var states sync.Map // *mojsRuntime → *capsState

func registerState(rt *mojsRuntime, st *capsState) { states.Store(rt, st) }

// stateFor 取回某 Runtime 的安装结果。
func stateFor(rt *mojsRuntime) (*capsState, bool) {
	v, ok := states.Load(rt)
	if !ok {
		return nil, false
	}
	return v.(*capsState), true
}

// CloseCapabilities 释放该 Runtime 已安装的能力（上游 fs 句柄与在跑的子进程）。
//
// runtime.go 的 Plugin.Close 必须调用它：node.API 持着工作区目录句柄与 worker 池，
// 不关就是每个插件一条泄漏（06 §6.4「关闭 Runtime 并取消它启动的所有异步工作」）。
func CloseCapabilities(rt *mojsRuntime) error {
	if rt == nil {
		return nil
	}
	v, ok := states.Load(rt)
	if !ok {
		return nil
	}
	st := v.(*capsState)
	states.Delete(rt)
	// 被管进程的引用挂在这个 Runtime 上：先释放，再关本 Runtime 自己的子进程。
	// 顺序反过来没有坏处，但先放被管进程能让「插件禁用 → 调度停止」尽快生效。
	st.releaseManagedProcesses()
	st.mu.Lock()
	ex, proc, api := st.ex, st.proc, st.api
	st.mu.Unlock()
	var errs []error
	if ex != nil {
		errs = append(errs, ex.shutdown())
	}
	if proc != nil {
		proc.shutdown()
	}
	if api != nil {
		errs = append(errs, api.Close())
	}
	return errors.Join(errs...)
}

// UpdateCallContext 在一次调用开始前换入新的宿主上下文（06 §6.4：Context 是每调用的）。
//
// 能力装在装配期做一次，Context 每调用都不同（Workspace/Thread/Signal/HTTP 回调）；
// runtime.go 必须在每次 CallTool/CallHook/CallAction/CallProviderStream 前调用它，
// 否则 agent:host 拿到的是装配期那份上下文。传 nil 的 Signal 视为「不取消」。
func UpdateCallContext(rt *mojsRuntime, hc Context) error {
	st, ok := stateFor(rt)
	if !ok {
		return fmt.Errorf("host: Runtime %p 还没装过能力", rt)
	}
	st.mu.Lock()
	st.hc = hc
	st.mu.Unlock()
	return nil
}

// currentContext 返回当前宿主上下文快照（安装期写入，调用期换入）。
func (st *capsState) currentContext() Context {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.hc
}

// installCapabilities 是 host.InstallCapabilities 的实现（init 里单点赋值）。
func installCapabilities(rt *mojsRuntime, loop *mojsLoop, caps *CapabilitySet, hc Context) error {
	if rt == nil || loop == nil {
		return errors.New("host: 装能力需要 Runtime 与 Loop")
	}
	if caps == nil {
		return errors.New("host: 装能力需要 CapabilitySet：没有它无从判断该装什么")
	}
	if _, ok := stateFor(rt); ok {
		return errors.New("host: 该 Runtime 已装过能力，拒绝重复安装（能力表必须唯一）")
	}
	st := &capsState{
		rt: rt, loop: loop, caps: caps, hc: hc,
		limits: currentLimits(), installed: map[string]provider{},
	}
	if dirs, derr := binpaths.Dirs(st.caps.WorkspaceRoot); derr == nil {
		st.binDirs = dirs
	}
	if len(hc.EnvVars) > 0 {
		st.baseEnv = make(map[string]string, len(hc.EnvVars))
		for key, value := range hc.EnvVars {
			st.baseEnv[key] = value
		}
	}

	// 1) 上游 stdlib：node:fs / node:fs/promises / node:buffer（总是安装，写成员不再按能力裁剪）。
	if err := st.installUpstream(); err != nil {
		return err
	}
	// 2) 宿主自建模块的 Go 侧实现（顺序：guard 原语 → process → agent:host → child_process）。
	if err := st.installGuardPrimitives(); err != nil {
		return err
	}
	if err := st.installProcess(); err != nil {
		return err
	}
	// 全局 console 必须总是存在（模块没被 import 也要能 log）。
	if err := st.installConsole(); err != nil {
		return err
	}
	if err := st.installHostBridge(); err != nil {
		return err
	}
	if err := st.installExec(); err != nil {
		return err
	}
	st.proc = newProcessState()
	// 3) 求值期：import.meta 填 binding、node:fs 成员包装（上限与审计）。
	if err := st.chainImportHooks(); err != nil {
		return err
	}
	registerState(rt, st)
	st.audit("host", "install", capsID(caps), 0, "ok", map[string]any{
		"trust": string(caps.Trust),
	})
	return nil
}

func capsID(caps *CapabilitySet) string {
	if caps == nil {
		return ""
	}
	return string(caps.Trust)
}

// installUpstream 装 moejs node 包给的三个模块，并把 fs 的上限与审计交给 fsGuard。
func (st *capsState) installUpstream() error {
	opts := mojsnode.Options{Buffer: true}
	guard, err := newFSGuard(st)
	if err != nil {
		return err
	}
	st.fs = guard
	opts.FS = guard.options()
	api, err := mojsnode.InstallLoop(st.loop, opts)
	if err != nil {
		return fmt.Errorf("host: 安装 node:fs/node:buffer 失败: %w", err)
	}
	st.api = api
	return nil
}

// chainImportHooks 把 import.meta / import() 的包装挂到本 Runtime。
//
// 上游的 node.Install 已经包过一层（填 node:fs 的 binding），这里是第二层：
//   - 先让上一层填它自己的 binding；
//   - 再填宿主自建模块的 binding、并对 node:fs 系做成员包装（上限与审计）。
//
// 模块白名单已移除：宿主不再否决任何 node:* 模块。
//
// 注意不要直接改 Importer 的 hooks 结构体：它被同进程内所有 Runtime 共享（moejs 文档），
// 因此必须复制一份再 SetImportHooks。
func (st *capsState) chainImportHooks() error {
	re := st.rt.Realm()
	h := &engine.ImportHooks{}
	if prev := re.ImportHooks(); prev != nil {
		*h = *prev
	}
	prevMeta := h.Meta

	h.Meta = func(r *engine.Realm, module any, meta *engine.Object) error {
		name := ""
		if m, ok := module.(*moejs.Module); ok {
			name = m.Name()
		}
		if prevMeta != nil {
			if err := prevMeta(r, module, meta); err != nil {
				return err
			}
		}
		return st.fillMeta(r, name, meta)
	}
	re.SetImportHooks(h)
	return nil
}

// fillMeta 填宿主自建模块的 binding，并对上游 fs 的 binding 加上限与审计。
func (st *capsState) fillMeta(r *engine.Realm, name string, meta *engine.Object) error {
	binding, ok := st.moduleBinding(TrimModulePrefix(name))
	if ok {
		return defineBinding(r, meta, binding)
	}
	// 上游 fs / fs/promises：上一层已填 binding，这里按能力裁剪成员并加上限与审计。
	switch TrimModulePrefix(name) {
	case moduleFS, moduleFSPromises:
		if st.fs == nil {
			return nil
		}
		v, found := meta.GetOwnDataValue(r.KeyFromGoString("binding"))
		if !found || !v.IsObject() {
			return nil
		}
		return st.fs.wrapBindings(r, v.AsObject(), TrimModulePrefix(name) == moduleFSPromises)
	}
	return nil
}

// defineBinding 把宿主对象挂成 import.meta.binding（与上游同一形状：模块体只读这一个属性）。
func defineBinding(r *engine.Realm, meta *engine.Object, binding moejs.Value) error {
	var d engine.PropertyDescriptor
	d.SetValue(binding)
	d.SetWritable(true)
	d.SetEnumerable(true)
	d.SetConfigurable(true)
	return meta.DefinePropertyOrThrow(r, r.KeyFromGoString("binding"), d)
}

// signalOf 返回当前调用的取消信号（nil Context 时给 Background，绝不返回 nil ctx）。
func (st *capsState) signalOf() context.Context {
	if sc := st.currentContext().Signal; sc != nil {
		return sc
	}
	return context.Background()
}

var (
	// secretKeyRe 命中「参数名像密钥」的键（12 §6.12.1(3)：审计脱敏）。
	secretKeyRe = regexp.MustCompile(`(?i)(api[_.-]?key|secret|token|passwo?r?d|authorization|cookie|bearer|credential|access[_.-]?key)`)
	// bearerRe 命中 "Bearer xxx" / "sk-xxx" 这类值。
	bearerRe = regexp.MustCompile(`(?i)\b(bearer|basic|token|apikey|api_key|sk|ghp|xoxb)\b[=:\s]+([^\s"'&,;]{6,})`)
)

// redact 把密钥与 token 从审计文本里抹掉：只留键名与 ***。
func redact(s string) string {
	if s == "" {
		return ""
	}
	out := secretKeyRe.ReplaceAllStringFunc(s, func(m string) string { return m + "=***" })
	out = bearerRe.ReplaceAllStringFunc(out, func(m string) string {
		i := strings.LastIndexAny(m, "=: ")
		if i < 0 {
			return "***"
		}
		return m[:i+1] + "***"
	})
	return out
}

// redactValue 是字段级的脱敏：键名本身像密钥时整段替换。
func redactValue(key, value string) string {
	if secretKeyRe.MatchString(key) {
		return "***"
	}
	return redact(value)
}

// audit 写一条受控调用审计（12 §6.12.1(3)）。
//
// 只走 Context.Log：宿主不开文件、不自己接 slog（09 §10 由 Core 统一落 audit 表）。
// 记录字段固定为：模块、方法、作用域内目标（路径/主机/命令）、字节数与结果。
func (st *capsState) audit(module, method, target string, bytes int, result string, extra map[string]any) {
	hc := st.currentContext()
	if hc.Log == nil {
		return
	}
	fields := map[string]any{
		"module":   module,
		"method":   method,
		"target":   redact(target),
		"bytes":    bytes,
		"result":   result,
		"pluginId": hc.PluginID,
		"runId":    hc.RunID,
		"callId":   hc.CallID,
		"threadId": hc.ThreadID,
	}
	for k, v := range extra {
		if s, ok := v.(string); ok {
			fields[k] = redactValue(k, s)
			continue
		}
		fields[k] = v
	}
	level := "info"
	if result != "ok" {
		level = "warn"
	}
	hc.Log(level, "host.capability."+module+"."+method, fields)
}

// auditError 记一次失败：把 JS 错误对象里的 code 摘出来当结果字段。
func (st *capsState) auditError(module, method, target string, err error) {
	result := "error"
	if err != nil {
		var exc *engine.Exception
		if errors.As(err, &exc) && exc.Value.IsObject() {
			r := st.rt.Realm()
			if cv, ok := exc.Value.AsObject().GetOwnDataValue(r.KeyFromGoString("code")); ok {
				result = cv.String()
			} else {
				result = exc.Value.String()
			}
		} else {
			result = err.Error()
		}
	}
	st.audit(module, method, target, 0, result, nil)
}

// compileVirtualModule 编译（一次进程级）宿主自建的模块：与上游 node 包一致，
// 模块实例由所有 Runtime 共享，每次 import 得到的差别只在 import.meta 的 binding。
func compileVirtualModule(name, source string) (*moejs.Module, error) {
	m, err := moejs.Compile(name, source)
	if err != nil {
		return nil, fmt.Errorf("host: 编译模块 %s 失败: %w", name, err)
	}
	return m, nil
}
