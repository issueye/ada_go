// 命令 cron 是 ada-go 的独立调度器：只做定时任务，把消息送达 Core。
//
// 由 adag gateway 拉起守护（传入网关地址，令牌经环境变量 ADAG_CRON_TOKEN 注入），
// 也可手动启动：
//
//	cron.exe --gateway-url http://127.0.0.1:8080 --token XXX
//
// 任务定义在 ~/.adago/cron.json（cron-manager 插件管理页读写）；到期后把
// AI 任务经 POST {gateway}/api/cron/fire 送达网关，由网关负责 Acquire Core、
// thread.create 与 turn.start。HTTP 动作已移除，只保留 AI 任务。
//
// 本程序刻意不 import 主模块（mcp_projects 各模块自包含的惯例）；任务文件
// 与 cron 表达式与主模块 internal/cronstore 逐字节镜像（verify 的 cron-sync
// 检查拦截漂移）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"adagcron/internal/runner"
)

func main() {
	var (
		gatewayURL  = flag.String("gateway-url", "", "网关地址（AI 任务必需，如 http://127.0.0.1:8080）")
		token       = flag.String("token", "", "网关 Bearer 令牌（AI 任务必需；也可用环境变量 ADAG_CRON_TOKEN）")
		parentPID   = flag.Int("parent-pid", 0, "父进程 PID：非 0 时父进程退出则本进程退出")
		intervalSec = flag.Int("interval", 5, "tick 周期（秒）")
		file        = flag.String("file", "", "任务文件路径（默认 ~/.adago/cron.json）")
		lock        = flag.String("lock", "", "锁文件路径（默认 ~/.adago/cron.lock）")
	)
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.LUTC)

	// 令牌优先取环境变量：守护拉起时经环境注入，不进进程命令行（同用户进程
	// 可读到别的进程的 argv）；--token 保留给手动调试。
	if strings.TrimSpace(*token) == "" {
		*token = strings.TrimSpace(os.Getenv("ADAG_CRON_TOKEN"))
	}

	if *parentPID > 0 && !parentAlive(*parentPID) {
		log.Fatalf("父进程 %d 已不存在，退出", *parentPID)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *parentPID > 0 {
		go watchParent(ctx, *parentPID, stop)
	}

	interval := time.Duration(*intervalSec) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	r := runner.New(*file, *lock, *gatewayURL, *token)
	r.Run(ctx, interval)
}

// parentAlive 报告父进程是否仍在运行。
func parentAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Windows 的 FindProcess+Signal(0) 不可用；用 Signal(nil) 语义差异兜底：
	// 对 Windows 休眠进程 signal 会报错但进程存在。这里按平台区分。
	if runtime.GOOS == "windows" {
		return p != nil
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// watchParent 周期检查父进程存活，父进程退出则取消 ctx。
func watchParent(ctx context.Context, pid int, stop context.CancelFunc) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !parentAlive(pid) {
				fmt.Fprintf(os.Stderr, "cron: 父进程 %d 已退出，调度器随之退出\n", pid)
				stop()
				return
			}
		}
	}
}
