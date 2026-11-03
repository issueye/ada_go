// Package cronproc 守护独立 cron 调度器二进制（mcp_projects/cron 的 cron.exe）。
//
// 网关是常驻进程，cron 的生命周期与它对齐：网关启动后调用 Start 拉起 cron
// （传入网关地址与 Bearer 令牌），崩溃后指数退避重启；网关 Shutdown 时一并回收。
// 找不到二进制不报错——定时任务是可选能力，降级为不启用并记一条日志。
//
// 二进制定位顺序：显式配置 → ADAG_CRON_BIN → 可执行文件同目录 → 开发布局
// （<exe>/../mcp_projects/cron/bin）。cron 自带 --parent-pid 双保险：即使
// 守护循环出 bug，父进程死亡它也会自行退出。
package cronproc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Options 是守护配置。
type Options struct {
	// Binary 是 cron 可执行文件路径；空表示按默认顺序定位。
	Binary string
	// GatewayURL 与 Token 传给 cron（--gateway-url/--token，agent 动作必需）。
	GatewayURL string
	Token      string
	// Logf 是守护日志（nil 用标准 log）。
	Logf func(format string, args ...any)
}

// Daemon 是一个 cron 子进程的守护器。
type Daemon struct {
	opts    Options
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	binPath string

	mu     sync.Mutex
	exited bool
}

const (
	// restartBase 与 restartMax 是重启退避的起点与上限。
	restartBase = 1 * time.Second
	restartMax  = 30 * time.Second
	// stableAfter 内没有退出就重置退避（连续崩溃只线性升温）。
	stableAfter = 60 * time.Second
)

// Start 拉起并守护 cron 二进制；立即返回，重启循环在后台运行。
// 二进制定位失败时返回 Disabled 的 Daemon（不启用，不报错）。
func Start(ctx context.Context, opts Options) *Daemon {
	dctx, cancel := context.WithCancel(ctx)
	d := &Daemon{opts: opts, ctx: dctx, cancel: cancel, done: make(chan struct{})}
	logf := d.logf

	bin, ok := resolve(opts.Binary)
	if !ok {
		logf("cron 二进制未找到（ADAG_CRON_BIN 或可执行文件同目录），定时任务不启用")
		close(d.done)
		return d
	}
	d.binPath = bin
	logf("cron 调度器：%s", bin)
	go d.loop()
	return d
}

// Disabled 报告守护是否因找不到二进制而未启用。
func (d *Daemon) Disabled() bool { return d.binPath == "" }

// Shutdown 停止守护循环并杀掉 cron 子进程；幂等。
func (d *Daemon) Shutdown(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.cancel()
	select {
	case <-d.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (d *Daemon) logf(format string, args ...any) {
	if d.opts.Logf != nil {
		d.opts.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// resolve 按 Options.Binary → ADAG_CRON_BIN → 可执行文件同目录 → 开发布局
// 的顺序定位 cron 二进制；返回是否找到。
func resolve(explicit string) (string, bool) {
	candidates := []string{}
	if p := strings.TrimSpace(explicit); p != "" {
		candidates = append(candidates, p)
	}
	if p := strings.TrimSpace(os.Getenv("ADAG_CRON_BIN")); p != "" {
		candidates = append(candidates, p)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, binName()))
		// 开发布局：仓库内 mcp_projects/cron/bin。
		candidates = append(candidates, filepath.Join(dir, "..", "mcp_projects", "cron", "bin", binName()))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, true
		}
	}
	return "", false
}

func binName() string {
	if runtime.GOOS == "windows" {
		return "cron.exe"
	}
	return "cron"
}

// loop 反复 spawn cron，直到 ctx 取消；崩溃按指数退避重启。
func (d *Daemon) loop() {
	defer close(d.done)
	defer d.cancel()
	logf := d.logf
	backoff := restartBase
	for {
		if d.ctx.Err() != nil {
			return
		}
		started := time.Now()
		if err := d.spawnOnce(); err != nil && d.ctx.Err() == nil {
			logf("cron 进程退出（运行 %s）: %v", time.Since(started).Round(time.Second), err)
		}
		if d.ctx.Err() != nil {
			return
		}
		if time.Since(started) > stableAfter {
			backoff = restartBase
		}
		logf("cron 将在 %s 后重启", backoff)
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < restartMax {
			backoff *= 2
			if backoff > restartMax {
				backoff = restartMax
			}
		}
	}
}

// spawnOnce 启动一个 cron 子进程并等待其退出。
func (d *Daemon) spawnOnce() error {
	cmd := exec.CommandContext(d.ctx, d.binPath,
		"--gateway-url", d.opts.GatewayURL,
		"--token", d.opts.Token,
		"--parent-pid", fmt.Sprint(os.Getpid()),
	)
	// cron 的日志直接透传到网关 stderr，加前缀区分；不缓存不截断——
	// 调度器的输出就是排障的第一手资料。
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cronproc: 创建 stdout 管道失败: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cronproc: 启动 %s 失败: %w", d.binPath, err)
	}
	go copyCronLog(d.logf, stdout)
	return cmd.Wait()
}

// copyCronLog 把 cron 的输出按行转成守护日志。
func copyCronLog(logf func(string, ...any), r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<10)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			logf("cron| %s", line)
		}
	}
}
