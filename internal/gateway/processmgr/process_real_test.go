package processmgr

// 真实进程测试：用测试二进制自身作为 helper 进程（跨平台，不依赖 node/shell），
// 覆盖「真的能启动、能读到输出、停止后进程消失」这条链路；其余用例都注入假进程。

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperEnv 标记「这个测试二进制当前以 helper 身份运行」。
const helperEnv = "PROCESSMGR_TEST_HELPER"

// helperChildEnv 让 helper 再派生一个孙进程（只认一次，不向下继承）。
const helperChildEnv = helperEnv + "_CHILD"

// helperProcessEnv 给 helper 进程构造环境：剥掉 CHILD 标记后再按需加回，
// 否则孙进程会继续派生，形成无限递归的进程树。
func helperProcessEnv(withChild bool) []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(name, helperChildEnv) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, helperEnv+"=1")
	if withChild {
		out = append(out, helperChildEnv+"=1")
	}
	return out
}

// TestHelperProcess 不是真正的测试：它只在被 ProcessManager 拉起时执行。
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("只在被 ProcessManager 拉起时作为 helper 运行")
	}
	// 若要求，派生且只派生一个孙进程，用于验证整棵进程树被回收。
	if os.Getenv(helperChildEnv) == "1" {
		child := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
		child.Env = helperProcessEnv(false)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Println("child-start-failed:", err)
		} else {
			fmt.Println("child-pid:", child.Process.Pid)
			go func() { _ = child.Wait() }()
		}
	}
	fmt.Println("helper-ready pid:", os.Getpid())
	// 长跑，直到被父进程杀掉。
	for {
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRealProcessStartStop 走生产 spawn：启动、读输出、停止、重启、注销。
func TestRealProcessStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实进程测试")
	}
	m := newTestManager(t, Options{
		Spawn:        execSpawn,
		StartTimeout: 15 * time.Second,
	})

	ref, err := m.Register("plug", ProcessSpec{
		Name: "helper", Entry: os.Args[0],
		Args:      []string{"-test.run=TestHelperProcess"},
		Env:       map[string]string{helperEnv: "1"},
		StartMode: StartOnEnable,
		// Windows 上 taskkill /T 本身要 1–2s，给停止留余量，避免在慢机器上误报超时。
		StopTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	st, _ := m.Status(ref)
	if !st.Running || st.PID <= 0 {
		t.Fatalf("真实进程未启动: %+v", st)
	}
	pid := st.PID

	// 输出必须进日志环（helper 打印 helper-ready）。
	waitFor(t, 10*time.Second, func() bool {
		logs, _ := m.Logs(ref, 0)
		for _, line := range logs {
			if strings.Contains(line.Text, "helper-ready") {
				return true
			}
		}
		return false
	})

	if err := m.Stop(ref); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if pidAlive(t, pid) {
		t.Fatalf("停止后进程 %d 仍在运行", pid)
	}

	// 再次 Ensure 能重新拉起（Stop 保留登记）。
	if _, err := m.Ensure(ref); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	st, _ = m.Status(ref)
	if !st.Running || st.PID == pid {
		t.Fatalf("重新拉起应换新 pid: old=%d new=%+v", pid, st)
	}
	newPID := st.PID
	if err := m.Unregister(ref); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if pidAlive(t, newPID) {
		t.Fatalf("注销后进程 %d 仍在运行", newPID)
	}
}

// TestRealProcessTreeReclaimed 覆盖「停止后整棵进程树无残留」：
// 被管进程再派生一个孙进程，停止时必须两者都消失。
func TestRealProcessTreeReclaimed(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实进程测试")
	}
	m := newTestManager(t, Options{
		Spawn:        execSpawn,
		StartTimeout: 15 * time.Second,
	})
	ref, err := m.Register("plug", ProcessSpec{
		Name: "tree", Entry: os.Args[0],
		Args:        []string{"-test.run=TestHelperProcess"},
		Env:         map[string]string{helperEnv: "1", helperEnv + "_CHILD": "1"},
		StartMode:   StartOnEnable,
		StopTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	st, _ := m.Status(ref)
	if !st.Running {
		t.Fatalf("真实进程未启动: %+v", st)
	}

	// 从日志里取孙进程 pid（helper 打印 child-pid）。
	var childPID int
	waitFor(t, 10*time.Second, func() bool {
		logs, _ := m.Logs(ref, 0)
		for _, line := range logs {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line.Text), "child-pid:"); ok {
				if n, convErr := strconv.Atoi(strings.TrimSpace(rest)); convErr == nil && n > 0 {
					childPID = n
					return true
				}
			}
		}
		return false
	})
	if !pidAlive(t, childPID) {
		t.Fatalf("孙进程 %d 未被拉起", childPID)
	}

	if err := m.Stop(ref); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if pidAlive(t, st.PID) {
		t.Fatalf("停止后直接子进程 %d 仍在运行", st.PID)
	}
	// 进程树回收可能略滞后于父进程退出（taskkill /T 是异步的）。
	waitFor(t, 5*time.Second, func() bool { return !pidAlive(t, childPID) })
	if pidAlive(t, childPID) {
		t.Fatalf("停止后孙进程 %d 仍在运行（进程树未回收）", childPID)
	}
}
