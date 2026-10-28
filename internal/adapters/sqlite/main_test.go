package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// helperEnv 把测试二进制自己变成「第二个进程」：多进程锁与 kill -9 崩溃恢复
// 都必须用真实进程（同进程里的第二个句柄不能证明 OS 锁的行为）。
// 用 os.Args[0]（go test 编译出的测试二进制）自调用，不需要 testdata 里的额外程序。
const helperEnv = "ADA_SQLITE_TEST_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "lock-holder":
		os.Exit(helperLockHolder())
	case "wal-writer":
		os.Exit(helperWalWriter())
	}
	os.Exit(m.Run())
}

// helperLockHolder 持住工作区独占锁，直到父进程关闭 stdin（或超时自动退出）。
//
// stdout 协议：
//
//	READY pid=<pid>            已拿到锁（父进程据此继续）
//	EXIT reason=<...>          退出原因（超时/父进程断开）
func helperLockHolder() int {
	dataDir := os.Getenv("ADA_TEST_DATA_DIR")
	hash := os.Getenv("ADA_TEST_HASH")
	if dataDir == "" || hash == "" {
		fmt.Println("ERROR missing env")
		return 2
	}
	lock, err := AcquireWorkspaceLock(dataDir, hash)
	if err != nil {
		fmt.Println("ERROR acquire:", err)
		return 1
	}
	fmt.Printf("READY pid=%d\n", os.Getpid())

	// 等父进程关掉 stdin 管道；同时留一个上限，避免测试崩了留下孤儿进程持锁。
	in := bufio.NewScanner(os.Stdin)
	done := make(chan struct{})
	go func() {
		in.Scan() // 父进程写什么都算「可以收工」；管道关闭同样返回 false
		close(done)
	}()
	select {
	case <-done:
		_ = lock.Release()
		fmt.Println("EXIT reason=parent-signal")
	case <-time.After(120 * time.Second):
		_ = lock.Release()
		fmt.Println("EXIT reason=timeout")
	}
	return 0
}

// helperWalWriter 用真实 Core 句柄追加 N 条 entry 并逐条报告，然后原地等待被 kill -9。
//
// stdout 协议：COMMITTED <seq> 每提交一条；ALL_DONE <n> 全部提交完。
// 关键：这个进程绝不 Close，也不删 -wal/-shm——它就是要以「非正常退出」的样子消失。
func helperWalWriter() int {
	ws := os.Getenv("ADA_TEST_WORKSPACE")
	dataDir := os.Getenv("ADA_TEST_DATA_DIR")
	n, err := strconv.Atoi(os.Getenv("ADA_TEST_COUNT"))
	if err != nil || ws == "" || dataDir == "" {
		fmt.Println("ERROR bad env:", err)
		return 2
	}
	ctx := context.Background()
	st, err := OpenCore(ctx, Config{
		Workspace: ws, DataDir: dataDir,
		WriterQueueSize: 8, UsageFlushWindow: 10 * time.Millisecond, CloseTimeout: time.Second,
	})
	if err != nil {
		fmt.Println("ERROR opencore:", err)
		return 1
	}
	th, err := st.CreateThread(ctx, ws, "crash-thread")
	if err != nil {
		fmt.Println("ERROR thread:", err)
		return 1
	}
	var parent string
	for i := 0; i < n; i++ {
		e, err := st.AppendEntry(ctx, ports.Entry{
			ThreadID: th.ID, ParentID: parent, Kind: domain.EntryMessage,
			Role: domain.RoleUser, Payload: json.RawMessage(`{"text":"wal-"` + strconv.Itoa(i) + `"}`),
		})
		if err != nil {
			fmt.Println("ERROR append:", err)
			return 1
		}
		parent = e.ID
		fmt.Printf("COMMITTED %d\n", e.Seq)
	}
	fmt.Printf("ALL_DONE %d\n", n)

	// 写完就等死：保持进程活着（持着写连接与锁），让父进程 kill。
	select {}
}

// startHelper 启动一个助手进程并返回它（stdin 管道用来发「收工」信号）。
type helperProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  *os.File
	lines  chan string
	stderr *bytes.Buffer
}

func startHelper(t *testing.T, mode, workspace, dataDir string, extra map[string]string) *helperProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试二进制路径失败: %v", err)
	}
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("建 stdin 管道失败: %v", err)
	}
	env := append(os.Environ(),
		helperEnv+"="+mode,
		"ADA_TEST_WORKSPACE="+workspace,
		"ADA_TEST_DATA_DIR="+dataDir,
	)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command(exe, "-test.run=TestHelperNoop", "-test.timeout=5m")
	cmd.Env = env
	cmd.Stdin = pipeR
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("接助手进程 stdout 失败: %v", err)
	}
	errBuf := &bytes.Buffer{}
	cmd.Stderr = errBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("启动助手进程失败: %v", err)
	}
	_ = pipeR.Close()
	h := &helperProc{t: t, cmd: cmd, stdin: pipeW, lines: make(chan string, 8192), stderr: errBuf}
	go func() {
		defer close(h.lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
	}()
	t.Cleanup(func() {
		_ = h.stdin.Close()
		if h.cmd.Process != nil {
			_, _ = h.cmd.Process.Wait()
		}
	})
	return h
}

// TestHelperNoop 只是助手进程的一个占位测试名：真正的分支在 TestMain 里按环境变量分派。
// 它保证 go test 的二进制在 helper 模式下不会顺手跑完整个测试集。
func TestHelperNoop(*testing.T) {}

// waitFor 读助手进程 stdout 直到出现满足 pred 的行（超时即失败，避免挂住整个测试）。
func (h *helperProc) waitFor(timeout time.Duration, pred func(string) bool) (string, error) {
	h.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				return "", fmt.Errorf("助手进程输出已结束，未等到期望行；stderr=%s", h.stderr.String())
			}
			if pred(line) {
				return line, nil
			}
		case <-deadline:
			return "", fmt.Errorf("等助手进程输出超时；stderr=%s", h.stderr.String())
		}
	}
}

// signalDone 通知助手进程收工。
func (h *helperProc) signalDone() {
	_, _ = h.stdin.WriteString("done\n")
	_ = h.stdin.Close()
}
