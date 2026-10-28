package domain

import (
	"errors"
	"fmt"
)

// Code 是业务错误码（4 位应用码），码表与名称逐条照 08 §8.5。
// JSON-RPC 标准错误码只用于传输层；业务错误一律用本表。
type Code int

const (
	// CodeWorkspaceMissing 工作区目录不存在（1001 workspace_missing，01 §4.2）。
	CodeWorkspaceMissing Code = 1001
	// CodeCoreUnavailable Core 未就绪/崩溃（1002 core_unavailable，08 §8.1）。
	CodeCoreUnavailable Code = 1002
	// CodeCoreBusy 实例正在回收/启动（1003 core_busy）。
	CodeCoreBusy Code = 1003
	// CodeWorkspaceLocked 另一个 Core 持有工作区锁（1004 workspace_locked，含持有者 pid，04 §5.7）。
	CodeWorkspaceLocked Code = 1004

	// CodeThreadBusy 线程已有运行中轮次（2001 thread_busy，02 §5.3 步骤 1、INV-7）。
	CodeThreadBusy Code = 2001
	// CodeTurnAborted 用户取消（2002 turn_aborted）。
	CodeTurnAborted Code = 2002
	// CodeBudgetExceeded 显式配置的墙钟/token/成本上限超限；默认不启用，步数不设限（2003，02 §5.3、08 §8.5）。
	CodeBudgetExceeded Code = 2003
	// CodeContextOverflow 压缩失败且无法继续（2004 context_overflow，04 §5.7、02 §5.3）。
	CodeContextOverflow Code = 2004
	// CodeLoopDetected 连续重复相同的工具调用、模型未改道（2005 loop_detected，02 §5.3 循环检测）。
	CodeLoopDetected Code = 2005

	// CodeToolNotFound 工具已被禁用（3001 tool_not_found；禁用插件后能力必须消失，INV-11）。
	CodeToolNotFound Code = 3001
	// CodeToolInvalidArgs Schema 校验失败（3002，02 §5.4：校验失败直接生成 error 回执）。
	CodeToolInvalidArgs Code = 3002
	// CodePluginTimeout 插件调用超时（3003，06 §6.4：超时记 plugin_timeout 并按钩子失败方向处理）。
	CodePluginTimeout Code = 3003
	// CodePluginCrashed 插件运行时重建（3004，06 §6.4 回收策略）。
	CodePluginCrashed Code = 3004
	// CodePluginDenied 能力/信任不允许（3005，05 §6.2、06 §6.9）。
	CodePluginDenied Code = 3005

	// CodeAuthRequired 未登录/cookie 过期（4001，01 §4.3）。
	CodeAuthRequired Code = 4001
	// CodeCSRFInvalid 表单 token 不匹配（4002）。
	CodeCSRFInvalid Code = 4002
	// CodeOriginDenied Origin/Host 校验失败（4003，防 DNS rebinding）。
	CodeOriginDenied Code = 4003

	// CodeProviderError 模型返回错误（5001；首个增量前由基座包装层重试，03 §5.5）。
	CodeProviderError Code = 5001
	// CodeProtocolError 适配器流解析失败（5002；含“没有终止信号的 EOF”，03 §5.5）。
	CodeProtocolError Code = 5002
	// CodeStorageBusy SQLite 写锁等待超时（5003，04 §5.7：busy_timeout 与退避重试）。
	CodeStorageBusy Code = 5003
	// CodeStorageCorrupt WAL/数据库损坏，需要 doctor 或备份恢复（5004）。
	CodeStorageCorrupt Code = 5004
)

// allCodes 覆盖 08 §8.5 的全部 21 个业务错误码；verify 门禁可用它核对 spec/proto.json。
var allCodes = []Code{
	CodeWorkspaceMissing, CodeCoreUnavailable, CodeCoreBusy, CodeWorkspaceLocked,
	CodeThreadBusy, CodeTurnAborted, CodeBudgetExceeded, CodeContextOverflow, CodeLoopDetected,
	CodeToolNotFound, CodeToolInvalidArgs, CodePluginTimeout, CodePluginCrashed, CodePluginDenied,
	CodeAuthRequired, CodeCSRFInvalid, CodeOriginDenied,
	CodeProviderError, CodeProtocolError, CodeStorageBusy, CodeStorageCorrupt,
}

// AllCodes 返回错误码表的稳定副本（供契约测试/verify 使用）。
func AllCodes() []Code { return append([]Code(nil), allCodes...) }

// CodeName 返回 08 §8.5 的名称列（错误码的 wire 名）。
func CodeName(code Code) string {
	switch code {
	case CodeWorkspaceMissing:
		return "workspace_missing"
	case CodeCoreUnavailable:
		return "core_unavailable"
	case CodeCoreBusy:
		return "core_busy"
	case CodeWorkspaceLocked:
		return "workspace_locked"
	case CodeThreadBusy:
		return "thread_busy"
	case CodeTurnAborted:
		return "turn_aborted"
	case CodeBudgetExceeded:
		return "budget_exceeded"
	case CodeContextOverflow:
		return "context_overflow"
	case CodeLoopDetected:
		return "loop_detected"
	case CodeToolNotFound:
		return "tool_not_found"
	case CodeToolInvalidArgs:
		return "tool_invalid_args"
	case CodePluginTimeout:
		return "plugin_timeout"
	case CodePluginCrashed:
		return "plugin_crashed"
	case CodePluginDenied:
		return "plugin_denied"
	case CodeAuthRequired:
		return "auth_required"
	case CodeCSRFInvalid:
		return "csrf_invalid"
	case CodeOriginDenied:
		return "origin_denied"
	case CodeProviderError:
		return "provider_error"
	case CodeProtocolError:
		return "protocol_error"
	case CodeStorageBusy:
		return "storage_busy"
	case CodeStorageCorrupt:
		return "storage_corrupt"
	default:
		return "unknown"
	}
}

// IsRetryable 报告该错误是否属于“可安全自动重试”一类：
// 传输/瞬时层（core 未就绪、实例忙、SQLite busy、provider 429/5xx/网络）。
// 注意 provider 的重试只允许发生在首个增量之前（03 §5.5），
// 已经开始输出后不自动重试，避免重复内容；业务/策略类错误一律不可重试。
func IsRetryable(code Code) bool {
	switch code {
	case CodeCoreUnavailable, // 08 §8.1：断线自动重连一次。
		CodeCoreBusy,      // 08 §8.5：实例正在回收/启动，稍后可用。
		CodeProviderError, // 03 §5.5：429/5xx/网络错误，仅首个增量前。
		CodeStorageBusy:   // 04 §5.7：SQLITE_BUSY 指数退避重试，最多 3 次。
		return true
	default:
		return false
	}
}

// Error 是业务错误：码 + 名称 + 人话说明 + 可选诊断码与底层原因。
// 页面只显示码 + 人话 + 下一步，不显示堆栈（08 §8.5）。
type Error struct {
	Code   Code   `json:"code"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	// Diag 是诊断码字符串（ADG-<AREA>-<NNN>），可为空。
	Diag string `json:"diag,omitempty"`
	err  error
}

// NewError 构造业务错误；detail 是给人看的一句话。
func NewError(code Code, detail string) *Error {
	return &Error{Code: code, Name: CodeName(code), Detail: detail}
}

// WrapError 在业务错误上附加底层原因（不改变对外码）。
func WrapError(e *Error, cause error) *Error {
	w := *e
	w.err = cause
	return &w
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	if e.Diag != "" {
		return fmt.Sprintf("%s (%d %s) [%s]", e.Detail, int(e.Code), e.Name, e.Diag)
	}
	return fmt.Sprintf("%s (%d %s)", e.Detail, int(e.Code), e.Name)
}

// Unwrap 返回底层原因，支持 errors.As/Is 链。
func (e *Error) Unwrap() error { return e.err }

// AsError 把任意 error 归一化为 *Error；非业务错误返回 false。
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// DiagArea 是诊断码的区域段：ADG-<AREA>-<NNN> 的 AREA。
// 区域集合照 08 §8.5（例如 ADG-MODEL-002、ADG-PLUGIN-004）。
type DiagArea string

const (
	DiagWS     DiagArea = "WS"     // 工作区/实例生命周期。
	DiagCore   DiagArea = "CORE"   // Core 进程/装配。
	DiagThread DiagArea = "THREAD" // 线程/轮次/压缩。
	DiagTool   DiagArea = "TOOL"   // 工具执行与回执。
	DiagPlugin DiagArea = "PLUGIN" // 插件运行时与治理。
	DiagAuth   DiagArea = "AUTH"   // 鉴权/CSRF/Origin。
	DiagModel  DiagArea = "MODEL"  // 模型流与 provider 桥。
	DiagStore  DiagArea = "STORE"  // SQLite/锁/备份。
	DiagUI     DiagArea = "UI"     // SSR/SSE/投影。
	DiagConfig DiagArea = "CFG"    // 配置与产品声明。
)

// Diagnostic 是诊断码值对象：格式 ADG-<AREA>-<NNN>，NNN 为 0-999 三位零填充。
// 词汇表一处维护（08 §8.5：文档指向 internal/diag/codes.go；M0 契约桩先落在 domain，
// 待 internal/diag 包建立时以本类型为准迁移）。
type Diagnostic struct {
	Area DiagArea
	Num  int
}

// NewDiagnostic 构造诊断码；num 超出 0..999 时截断到 999（防御式，避免生成畸形码）。
func NewDiagnostic(area DiagArea, num int) Diagnostic {
	if num < 0 {
		num = 0
	}
	if num > 999 {
		num = 999
	}
	return Diagnostic{Area: area, Num: num}
}

// String 输出 ADG-<AREA>-<NNN>。
func (d Diagnostic) String() string {
	return fmt.Sprintf("ADG-%s-%03d", d.Area, d.Num)
}

// WithDiag 把诊断码附加到业务错误的副本上（不修改原对象）。
func (e *Error) WithDiag(d Diagnostic) *Error {
	w := *e
	w.Diag = d.String()
	return &w
}
