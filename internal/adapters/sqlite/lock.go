package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// WorkspaceLock 是一个工作区的 OS 独占锁句柄（04 §5.7「写者唯一」的第一道闸门）。
//
// 语义：
//   - 锁是 <dataDir>/locks/ws-<hash>.lock 上的内核文件锁（Unix flock、Windows
//     LockFileEx 区域锁，两者都由 github.com/gofrs/flock 统一），不是「文件存在即占用」；
//   - 随进程退出由 OS 自动释放，锁文件残留不代表仍被占用（04 §5.7），因此本包
//     从不删除锁文件；
//   - 拿不到锁返回 *LockedError（workspace_locked + 持有者 pid），调用方在此之前
//     不可能打开 state.db；
//   - 锁目录在插件与工具的 scope 之外（ports/scope.go 的 ReadOnly 标记由 scope
//     适配器实施；本包只保证锁不与数据库文件混在同一层，且不落在插件可写路径里）。
//
// 持有者 pid 为什么不写进 .lock 本体（Windows 实测结论，M1-B）：
// Windows 的 LockFileEx 是**字节区域锁**，gofrs/flock 锁住偏移 0 起的 1 个字节。
// 该区域对「其它句柄」的读写一律返回 ERROR_LOCK_VIOLATION——包括持锁进程自己
// 新开的第二个句柄（实测错误文本：The process cannot access the file because
// another process has locked a portion of the file.）。而 flock 不导出它持有的
// 文件句柄，所以持锁后无法再往 .lock 里写 pid。
// 因此持有者信息放在同目录的 ws-<hash>.pid：只有持锁者会写它（写成功即证明持有），
// 抢锁失败的一方读它取 pid；读不到仍然拒绝启动（失败方向），只是错误里 pid 未知。
type WorkspaceLock struct {
	path    string
	dataDir string
	hash    string
	fl      *flock.Flock
	pid     int
	// acquiredAt 只用于锁文件内容与诊断输出。
	acquiredAt time.Time
}

// DataDir 返回锁所属的数据目录（Restore 需要它定位 state.db）。
func (l *WorkspaceLock) DataDir() string { return l.dataDir }

// Path 返回锁文件路径。
func (l *WorkspaceLock) Path() string { return l.path }

// PID 返回本进程的持有者 pid。
func (l *WorkspaceLock) PID() int { return l.pid }

// PIDPath 返回持有者信息文件路径（<lockdir>/ws-<hash>.pid）。
func PIDPath(lockPath string) string { return strings.TrimSuffix(lockPath, ".lock") + ".pid" }

// AcquireWorkspaceLock 非阻塞地取得工作区独占锁。
//
// 失败时返回 *LockedError（携带能从 .pid 读到的持有者 pid）。本函数不会打开
// 数据库，也不会因为「锁文件残留」而自行清理任何东西。
func AcquireWorkspaceLock(dataDir, hash string) (*WorkspaceLock, error) {
	dir := LockDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sqlite: 创建锁目录失败 %s: %w", dir, err)
	}
	path := LockPath(dataDir, hash)
	if filepath.Dir(path) != dir {
		// 防御式：哈希或路径被塞进 ".." 之类的东西时，宁可拒绝也不把锁写到 scope 外。
		return nil, invalidInput(fmt.Sprintf("锁路径异常：%s 不在 %s 下", path, dir))
	}

	fl := flock.New(path, flock.SetPermissions(0o600))
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("sqlite: 加工作区锁失败 %s: %w", path, err)
	}
	if !ok {
		pid, holder := readLockHolder(PIDPath(path), 100*time.Millisecond)
		return nil, &LockedError{Path: path, HolderPID: pid, Holder: holder}
	}

	l := &WorkspaceLock{
		path: path, dataDir: dataDir, hash: hash, fl: fl,
		pid: os.Getpid(), acquiredAt: time.Now().UTC(),
	}
	if werr := writeLockHolder(PIDPath(path), l.pid); werr != nil {
		// 写不进持有者信息也要保持锁的语义：先解锁，再拒绝启动。
		// 没有可报告的持有者信息的第二个 Core 会被拒（本进程也起不来），
		// 这比「起来了但运维看不见谁占着」更安全。
		_ = fl.Unlock()
		return nil, fmt.Errorf("sqlite: 写入锁持有者信息失败 %s: %w", PIDPath(path), werr)
	}
	return l, nil
}

// Release 显式释放锁（进程退出时 OS 也会释放；Store.Close 走这条路）。
//
// 只删自己写的 .pid；.lock 永远留着（04 §5.7：锁文件残留不代表占用，删它没有意义，
// 而且删了会让并发的第三个进程读不到持有者）。
func (l *WorkspaceLock) Release() error {
	if l == nil || l.fl == nil {
		return nil
	}
	if err := l.fl.Unlock(); err != nil {
		return fmt.Errorf("sqlite: 释放工作区锁失败 %s: %w", l.path, err)
	}
	l.fl = nil
	_ = os.Remove(PIDPath(l.path))
	return nil
}

// writeLockHolder 写入持有者信息：第一行 pid，第二行 hostname。
// 只在持锁期间调用；抢锁失败的一方只读它、从不写它。
func writeLockHolder(path string, pid int) error {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	content := strconv.Itoa(pid) + "\n" + strings.TrimSpace(host) + "\n"
	return os.WriteFile(path, []byte(content), 0o600)
}

// readLockHolder 尽力读出 .pid 里记录的持有者（pid、hostname）。
//
// wait>0 时做有界重试：抢到锁的进程是「先 TryLock 成功、再写 .pid」，
// 两者之间有微秒级窗口，另一方在这段时间里失败就读不到 pid。
// 重试结束仍读不到返回 (0, "")：调用方照样拒绝启动（失败方向），只是错误里 pid 未知。
func readLockHolder(path string, wait time.Duration) (int, string) {
	deadline := time.Now().Add(wait)
	for {
		if pid, holder, ok := parseLockHolder(path); ok {
			return pid, holder
		}
		if time.Now().After(deadline) {
			return 0, ""
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func parseLockHolder(path string) (int, string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	holder := ""
	if len(lines) > 1 {
		holder = strings.TrimSpace(lines[1])
	}
	if holder == "" {
		holder = "pid=" + strconv.Itoa(pid)
	} else {
		holder = holder + ":" + strconv.Itoa(pid)
	}
	return pid, holder, true
}
