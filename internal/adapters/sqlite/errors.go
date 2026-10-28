package sqlite

import (
	"errors"
	"fmt"
	"strings"

	"github.com/adag/ada-go/internal/diag"
	"github.com/adag/ada-go/internal/domain"
	"modernc.org/sqlite"
)

// SQLite 主错误码（modernc.org/sqlite 的 *Error.Code() 低 8 位）。
// 只在本包内以常量形式出现，避免拉进 modernc.org/sqlite/lib 的内部枚举表。
const (
	sqliteOkMain     = 0
	sqliteBusyMain   = 5  // SQLITE_BUSY：另一个写者持有写锁。
	sqliteLockedMain = 6  // SQLITE_LOCKED：表级锁（共享缓存下常见）。
	sqliteReadOnly   = 8  // SQLITE_READONLY：mode=ro 连接上的任何写。
	sqliteCorrupt    = 11 // SQLITE_CORRUPT：数据库/WAL 损坏。
)

// diagFor 取应用错误码绑定的诊断码（internal/diag 是唯一映射表）；
// 查不到时退回 STORE 区域，保证错误永远带诊断码。
// 返回 domain.Diagnostic 是因为 domain.Error.WithDiag 收的就是它（08 §8.5 的迁移期，
// 见 internal/domain/errors.go 注释：码表在 domain，词汇表在 diag）。
func diagFor(code domain.Code) domain.Diagnostic {
	if e, ok := diag.For(int(code)); ok {
		return domain.NewDiagnostic(domain.DiagArea(e.Diag.Area), e.Diag.Num)
	}
	return domain.NewDiagnostic(domain.DiagStore, 999)
}

// fail 构造带诊断码的业务错误（08 §8.5：页面只显示码 + 人话 + 下一步）。
func fail(code domain.Code, detail string, cause error) *domain.Error {
	e := domain.NewError(code, detail).WithDiag(diagFor(code))
	if cause != nil {
		return domain.WrapError(e, cause)
	}
	return e
}

// storageBusy 表示 SQLITE_BUSY/SQLITE_LOCKED 在退避重试后仍未成功（04 §5.7）。
func storageBusy(detail string, cause error) *domain.Error {
	return fail(domain.CodeStorageBusy, detail, cause)
}

// storageCorrupt 表示数据库/WAL/schema 状态不可信，需要 doctor 或备份恢复（04 §5.7、09 §10）。
func storageCorrupt(detail string, cause error) *domain.Error {
	return fail(domain.CodeStorageCorrupt, detail, cause)
}

// invalidInput 表示调用方给的形状不符合存储契约（append-only、枚举、上限）。
// 用 3002（参数不符合 Schema）：码表里它是唯一的「输入不符合契约」类错误，
// 语义在 02 §5.4 是按工具调用描述的，本包借用它承载「存储入参非法」。
func invalidInput(detail string) *domain.Error {
	return fail(domain.CodeToolInvalidArgs, detail, nil)
}

// asError 把 *domain.Error 连同原因归一化（供上层 errors.As 判定码）。
func asError(err error) (*domain.Error, bool) { return domain.AsError(err) }

// sqliteMainCode 取 SQLite 错误的主码（低 8 位）；非 SQLite 错误返回 -1。
func sqliteMainCode(err error) int {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return -1
	}
	return se.Code() & 0xff
}

// isBusyError 报告 err 是否属于 SQLITE_BUSY/SQLITE_LOCKED 一类（可退避重试，04 §5.7）。
//
// 同时兜底字符串判定：database/sql 会包装驱动错误，某些路径（例如 busy handler
// 超时后的 "database is locked"）在驱动层已丢失结构化码。
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	switch sqliteMainCode(err) {
	case sqliteBusyMain, sqliteLockedMain:
		return true
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// isCorruptError 报告 err 是否是 SQLite 判定的一致性损坏（04 §5.7 故障语义）。
func isCorruptError(err error) bool {
	if err == nil {
		return false
	}
	if sqliteMainCode(err) == sqliteCorrupt {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_corrupt") || strings.Contains(msg, "database disk image is malformed")
}

// isReadOnlyError 报告 err 是否是只读连接上的写尝试（04 §5.7：CLI 只读）。
func isReadOnlyError(err error) bool { return sqliteMainCode(err) == sqliteReadOnly }

// LockedError 是「工作区已被另一个 Core 占用」：携带持有者 pid（04 §5.7 故障语义、
// domain.CodeWorkspaceLocked / ADG-WS-002）。
//
// 它是可判定错误：errors.As(err, &*LockedError{}) 或直接取 Domain()。
// 出现该错误时本包一定没有打开过 state.db（不打开数据库是硬约束）。
type LockedError struct {
	// Path 是锁文件路径（<dataDir>/locks/ws-<hash>.lock）。
	Path string
	// HolderPID 是锁文件里记录的持有者进程号；读不到时为 0。
	HolderPID int
	// Holder 是持有者描述（hostname:pid），可为空。
	Holder string
	// cause 是底层 OS 错误（例如 Windows 的「另一个程序已锁定文件的一部分」）。
	cause error
}

func (e *LockedError) Error() string {
	if e.HolderPID > 0 {
		return fmt.Sprintf("工作区已被另一个 Core 占用（持有者 pid=%d）: %s [%s]",
			e.HolderPID, e.Path, diagFor(domain.CodeWorkspaceLocked))
	}
	return fmt.Sprintf("工作区已被另一个 Core 占用（读不到持有者 pid）: %s [%s]",
		e.Path, diagFor(domain.CodeWorkspaceLocked))
}

// Unwrap 返回底层 OS 错误。
func (e *LockedError) Unwrap() error { return e.cause }

// Code 让 LockedError 自证业务码（与 domain.Error 一致的判定面）。
func (e *LockedError) Code() domain.Code { return domain.CodeWorkspaceLocked }

// Domain 转成对外的业务错误（coreproto/gateway 只需 domain.Error）。
func (e *LockedError) Domain() *domain.Error {
	detail := e.Error()
	if e.cause != nil {
		detail = detail + "：" + e.cause.Error()
	}
	return fail(domain.CodeWorkspaceLocked, detail, e.cause)
}

// AsLocked 判定 err 是否为工作区占用错误并取出持有者 pid。
func AsLocked(err error) (*LockedError, bool) {
	var le *LockedError
	if errors.As(err, &le) {
		return le, true
	}
	return nil, false
}
