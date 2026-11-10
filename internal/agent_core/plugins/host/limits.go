package host

// 本文件收拢成员与模块安装需要的常量与上限（原先与 module_policy.go 同文件）。
//
// 能力位与模块白名单已整体移除（2026-11-10 决策）；模块名常量与上限仍然需要：
// 它们描述的是宿主内建模块的形状与资源边界，不是「谁被允许装什么」。

import (
	"sync"
	"time"
)

// 模块名（归一后，不带 node: 前缀）。
const (
	moduleProcess      = "process"
	moduleChildProcess = "child_process"
	moduleAgentHost    = "agent:host"
	moduleFS           = "fs"
	moduleFSPromises   = "fs/promises"
	moduleBuffer       = "buffer"
)

// TrimModulePrefix 去掉 node: 前缀（模块表与绑定的键不带前缀）。
func TrimModulePrefix(spec string) string {
	const p = "node:"
	if len(spec) >= len(p) && spec[:len(p)] == p {
		return spec[len(p):]
	}
	return spec
}

// Limits 是宿主桥的字节与数量上限（09 §9.5、12 §6.13）。
//
// 超限一律报错而不是截断；进模型回执的 50KB/2000 行截断是另一层（02 §5.4），不在这里。
type Limits struct {
	// MaxFileBytes 是 fs 单次读写上限（16MB）。
	MaxFileBytes int
	// MaxDirEntries 是单次目录列举条目上限（10k）。
	MaxDirEntries int
	// MaxPathBytes 是路径字节上限（4096）。
	MaxPathBytes int
	// MaxFetchBytes 是 ctx.http 响应上限（16MB）。
	MaxFetchBytes int
	// MaxExecOutputBytes 是 child_process 捕获上限（4MB）。
	MaxExecOutputBytes int
	// MaxExecTimeout 是 child_process 的超时上限（120s）：脚本给更大值按本值夹住。
	MaxExecTimeout time.Duration
	// MaxBackgroundTimeout 是后台命令的生命周期上限（30m）：超时杀整棵进程树。
	MaxBackgroundTimeout time.Duration
	// MaxBackgroundTasks 是每 Runtime 同时保留的后台任务数上限（8，含已结束未回收）。
	MaxBackgroundTasks int
	// MaxTimers 是每 Runtime 未完成定时器上限（256）。
	MaxTimers int
}

// DefaultLimits 返回 09 §9.5 表格里的默认值。
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:         16 << 20,
		MaxDirEntries:        10000,
		MaxPathBytes:         4096,
		MaxFetchBytes:        16 << 20,
		MaxExecOutputBytes:   4 << 20,
		MaxExecTimeout:       120 * time.Second,
		MaxBackgroundTimeout: 30 * time.Minute,
		MaxBackgroundTasks:   8,
		MaxTimers:            256,
	}
}

// limitsMu 保护 limits：上限本身是宿主配置，测试用小阈值才能几毫秒跑完 16MB/10k 的边界。
var limitsMu sync.Mutex

var limits = DefaultLimits()

// SetLimits 覆盖全局上限（装配期一次；测试用小阈值）。零值字段回落默认值。
func SetLimits(l Limits) {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	limits = mergeLimits(l, DefaultLimits())
}

// currentLimits 读当前上限快照。
func currentLimits() Limits {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	return limits
}

func mergeLimits(have, fallback Limits) Limits {
	if have.MaxFileBytes <= 0 {
		have.MaxFileBytes = fallback.MaxFileBytes
	}
	if have.MaxDirEntries <= 0 {
		have.MaxDirEntries = fallback.MaxDirEntries
	}
	if have.MaxPathBytes <= 0 {
		have.MaxPathBytes = fallback.MaxPathBytes
	}
	if have.MaxFetchBytes <= 0 {
		have.MaxFetchBytes = fallback.MaxFetchBytes
	}
	if have.MaxExecOutputBytes <= 0 {
		have.MaxExecOutputBytes = fallback.MaxExecOutputBytes
	}
	if have.MaxExecTimeout <= 0 {
		have.MaxExecTimeout = fallback.MaxExecTimeout
	}
	if have.MaxBackgroundTimeout <= 0 {
		have.MaxBackgroundTimeout = fallback.MaxBackgroundTimeout
	}
	if have.MaxBackgroundTasks <= 0 {
		have.MaxBackgroundTasks = fallback.MaxBackgroundTasks
	}
	if have.MaxTimers <= 0 {
		have.MaxTimers = fallback.MaxTimers
	}
	return have
}
