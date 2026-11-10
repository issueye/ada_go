package corerpc

import (
	"context"
	"time"
)

// WatchParent 轮询父进程；父进程消失时调用 onGone（Core 自行退出，避免孤儿）。
//
// 设计 4.2 要求 Core 能发现 Gateway 消失；pid<=0 表示没有父进程约束。
func WatchParent(ctx context.Context, pid int, onGone func()) {
	if pid <= 0 || onGone == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !parentAlive(pid) {
				onGone()
				return
			}
		}
	}
}
