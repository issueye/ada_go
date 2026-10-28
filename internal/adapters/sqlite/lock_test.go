package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// TestWorkspaceLockSecondAcquirerRejected 验证「同工作区第二个持有者被拒 + 拿到持有者 pid」
// 的错误形状（同进程内的第二个句柄也走同一条 OS 锁路径）。
//
// 这条用例不替代多进程用例（TestWorkspaceLockSecondProcess*）：
// 同进程只能证明「错误码/pid 读取」这一半，跨进程独占性由后面的真进程用例证明。
func TestWorkspaceLockSecondAcquirerRejected(t *testing.T) {
	t.Parallel()
	_, dataDir := newWorkspace(t)
	hash := "abcd1234ef56"

	first, err := AcquireWorkspaceLock(dataDir, hash)
	if err != nil {
		t.Fatalf("第一次加锁失败: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	_, err = AcquireWorkspaceLock(dataDir, hash)
	if err == nil {
		t.Fatalf("第二个加锁请求竟然成功了")
	}
	le, ok := AsLocked(err)
	if !ok {
		t.Fatalf("错误不是 *LockedError：%T %v", err, err)
	}
	if le.HolderPID != os.Getpid() {
		t.Fatalf("持有者 pid=%d，期望本进程 %d", le.HolderPID, os.Getpid())
	}
	if le.Code() != domain.CodeWorkspaceLocked {
		t.Fatalf("业务码=%d，期望 %d workspace_locked", le.Code(), domain.CodeWorkspaceLocked)
	}
	de := le.Domain()
	if de.Diag == "" || !strings.HasPrefix(de.Diag, "ADG-WS-") {
		t.Fatalf("诊断码应该是 ADG-WS-xxx，得到 %q", de.Diag)
	}

	// 释放之后必须能重新加锁（锁文件/pid 文件残留不代表占用，04 §5.7）。
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := AcquireWorkspaceLock(dataDir, hash)
	if err != nil {
		t.Fatalf("释放后重新加锁失败（残留文件被当成占用了？）: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("Release 2: %v", err)
	}
}

// TestWorkspaceLockNeverOpensDatabase 验证「拿不到锁就不打开数据库」：
// 第二个 Core 被拒时，state.db 必须仍然是第一个进程写的那个（没有第二份 WAL/没有新对象）。
func TestWorkspaceLockNeverOpensDatabase(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	st := openStore(t, testConfig(t, ws, dataDir))
	th := mustThread(t, st, "第一个 Core 的线程")

	cfg2 := testConfig(t, ws, dataDir)
	_, err := OpenCore(ctx, cfg2)
	if err == nil {
		t.Fatalf("第二个 Core 竟然启动了")
	}
	le, ok := AsLocked(err)
	if !ok {
		t.Fatalf("错误必须是 *LockedError，得到 %T: %v", err, err)
	}
	if le.HolderPID != os.Getpid() {
		t.Fatalf("持有者 pid=%d，期望 %d", le.HolderPID, os.Getpid())
	}
	// 拒绝路径不碰数据库：本进程的 state.db 仍是唯一一份，第一个 Core 的数据完好。
	if _, err := os.Stat(StateDBPath(dataDir)); err != nil {
		t.Fatalf("state.db 不见了: %v", err)
	}
	got, err := st.GetThread(ctx, th.ID)
	if err != nil {
		t.Fatalf("第二个 Core 被拒后，第一个 Core 读不到自己的线程（说明数据库被动过）: %v", err)
	}
	if got.ID != th.ID {
		t.Fatalf("线程漂移：%q vs %q", got.ID, th.ID)
	}
}

// TestWorkspaceLockSecondProcessRejected 是真·多进程用例：
// 用测试二进制自己当「第一个 Core」的持有者进程，本进程作为第二个 Core 被拒。
//
// 观察到的 Windows 行为（本机实跑，写进汇报）：gofrs/flock 在 Windows 用
// LockFileEx(LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY)，抢不到时返回
// ERROR_LOCK_VIOLATION(33)，被库翻译成 TryLock()=false、err=nil，因此本包得到
// 明确的「已被占用」而不是一个模糊的系统错误。
//
// 不并行原因：助手进程持有的锁与临时目录绑定，并行时调度抖动会让 pid 文件写入
// 与本机杀软/索引服务的文件句柄检查互相干扰，出现非确定性失败。
func TestWorkspaceLockSecondProcessRejected(t *testing.T) {
	ws, dataDir := newWorkspace(t)
	hash, err := WorkspaceHash(ws)
	if err != nil {
		t.Fatalf("WorkspaceHash: %v", err)
	}

	h := startHelper(t, "lock-holder", ws, dataDir, map[string]string{"ADA_TEST_HASH": hash})
	line, err := h.waitFor(20*time.Second, func(s string) bool { return strings.HasPrefix(s, "READY") })
	if err != nil {
		t.Fatalf("助手进程没有拿到锁: %v", err)
	}
	holderPID, err := parseReadyPID(line)
	if err != nil {
		t.Fatalf("解析 %q: %v", line, err)
	}
	if holderPID == os.Getpid() {
		t.Fatalf("助手进程报告了本进程 pid：%d", holderPID)
	}

	_, err = OpenCore(context.Background(), testConfig(t, ws, dataDir))
	le, ok := AsLocked(err)
	if !ok {
		h.t.Fatalf("第二个 Core 必须被拒并给出 *LockedError，得到 %T: %v", err, err)
	}
	if le.Code() != domain.CodeWorkspaceLocked {
		t.Fatalf("业务码=%d（%s），期望 workspace_locked", le.Code(), domain.CodeName(le.Code()))
	}
	if le.HolderPID != holderPID {
		t.Fatalf("错误里的持有者 pid=%d，期望助手进程 %d\n锁文件=%s", le.HolderPID, holderPID, le.Path)
	}
	t.Logf("%s：第二个 Core 被拒，持有者 pid=%d（助手进程），码=%s",
		runtime.GOOS, le.HolderPID, domain.CodeName(le.Code()))

	// 助手进程退出后锁必须由 OS 释放（04 §5.7），本进程随后要能启动。
	h.signalDone()
	if _, err := h.waitFor(20*time.Second, func(s string) bool { return strings.HasPrefix(s, "EXIT") }); err != nil {
		t.Fatalf("助手进程没有正常收工: %v", err)
	}
	st := openStore(t, testConfig(t, ws, dataDir))
	if _, err := st.CreateThread(context.Background(), ws, "锁释放后启动"); err != nil {
		t.Fatalf("锁释放后仍不能写: %v", err)
	}
}

// TestWorkspaceLockAutoReleaseOnProcessExit 验证「锁随进程退出自动释放」：
// 助手进程被 kill（非正常退出，没跑任何 Release）之后，本进程必须能拿到锁。
//
// 不并行原因：同上（依赖真实的 OS 锁生命周期）。
func TestWorkspaceLockAutoReleaseOnProcessExit(t *testing.T) {
	ws, dataDir := newWorkspace(t)
	hash, err := WorkspaceHash(ws)
	if err != nil {
		t.Fatalf("WorkspaceHash: %v", err)
	}
	h := startHelper(t, "lock-holder", ws, dataDir, map[string]string{"ADA_TEST_HASH": hash})
	if _, err := h.waitFor(20*time.Second, func(s string) bool { return strings.HasPrefix(s, "READY") }); err != nil {
		t.Fatalf("助手进程没拿到锁: %v", err)
	}

	// 强杀：模拟 Core 崩溃。锁文件与 .pid 都留在原地。
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_, _ = h.cmd.Process.Wait()

	if _, err := os.Stat(LockPath(dataDir, hash)); err != nil {
		t.Fatalf("锁文件应该还在（本包不删它）: %v", err)
	}
	// OS 必须已经回收了锁；否则 OpenCore 会失败。
	st := openStore(t, testConfig(t, ws, dataDir))
	if _, err := st.CreateThread(context.Background(), ws, "崩溃后重启"); err != nil {
		t.Fatalf("崩溃后重启不能写: %v", err)
	}
}

func parseReadyPID(line string) (int, error) {
	// 协议：READY pid=<n>
	_, rest, ok := strings.Cut(line, "pid=")
	if !ok {
		return 0, errors.New("行里没有 pid")
	}
	return strconv.Atoi(strings.TrimSpace(rest))
}

// TestPathsFollowDesign 检查落点形状：state.db / locks/ws-<hash>.lock（04 §5.7）。
func TestPathsFollowDesign(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	hash, err := WorkspaceHash(ws)
	if err != nil {
		t.Fatalf("WorkspaceHash: %v", err)
	}
	if got := StateDBPath(dataDir); filepath.Base(got) != "state.db" {
		t.Fatalf("state.db 路径不对: %s", got)
	}
	lp := LockPath(dataDir, hash)
	if filepath.Base(lp) != "ws-"+hash+".lock" {
		t.Fatalf("锁文件名不对: %s", lp)
	}
	if filepath.Dir(lp) != LockDir(dataDir) {
		t.Fatalf("锁不在 locks/ 目录下: %s", lp)
	}
	if filepath.Dir(LockDir(dataDir)) != filepath.Clean(dataDir) {
		t.Fatalf("锁目录必须挂在 dataDir 下（scope 之外）: %s", LockDir(dataDir))
	}
}

// TestWorkspaceKeyNormalization 验证身份键把 E:\x、E:/x/、大小写变体折叠到同一个键
// （01 §4.2；只在 Windows 上断言盘符折叠，其它平台断言尾斜杠与 Clean 一致性）。
func TestWorkspaceKeyNormalization(t *testing.T) {
	t.Parallel()
	ws, _ := newWorkspace(t)
	base, err := WorkspaceKey(ws)
	if err != nil {
		t.Fatalf("WorkspaceKey: %v", err)
	}
	withSlash := filepath.Clean(ws) + string(filepath.Separator)
	alt, err := WorkspaceKey(withSlash)
	if err != nil {
		t.Fatalf("WorkspaceKey(带尾分隔符): %v", err)
	}
	if alt != base {
		t.Fatalf("尾分隔符没折叠：%q vs %q", base, alt)
	}
	if runtime.GOOS == "windows" {
		upper, err := WorkspaceKey(strings.ToUpper(ws))
		if err != nil {
			t.Fatalf("WorkspaceKey(大写): %v", err)
		}
		if upper != base {
			t.Fatalf("Windows 没折叠大小写：%q vs %q", base, upper)
		}
	}
	h1, err := WorkspaceHash(ws)
	if err != nil {
		t.Fatalf("WorkspaceHash: %v", err)
	}
	if len(h1) != 12 {
		t.Fatalf("身份键哈希长度=%d，期望 12（sha256[:12]）", len(h1))
	}
	if _, err := WorkspaceKey("   "); err == nil {
		t.Fatalf("空工作区路径必须报错")
	}
}
