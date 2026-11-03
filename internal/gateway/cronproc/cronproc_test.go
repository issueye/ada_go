package cronproc

import (
	"fmt"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolveFindsExplicitBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, binName())
	if err := os.WriteFile(bin, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("写探针文件: %v", err)
	}
	got, ok := resolve(bin)
	if !ok || got != bin {
		t.Fatalf("resolve(%q) = %q, %v", bin, got, ok)
	}
}

func TestResolveMissingBinaryDisabled(t *testing.T) {
	_, ok := resolve(filepath.Join(t.TempDir(), "不存在的路径", binName()))
	if ok {
		t.Fatalf("不存在的路径不应被解析成功")
	}
}

func TestStartWithMissingBinaryIsDisabled(t *testing.T) {
	d := Start(context.Background(), Options{Binary: filepath.Join(t.TempDir(), "nope", binName())})
	if !d.Disabled() {
		t.Fatalf("定位失败应标记 Disabled")
	}
	// done 已关闭：Shutdown 立即返回。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestShutdownStopsRestartLoop(t *testing.T) {
	// 用一个「永远找不到」的二进制起守护（loop 会一直退避重试 spawn），
	// Shutdown 必须在有限时间内让 loop 退出——验证取消传播。
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", binName())
	ctx := context.Background()
	d := Start(ctx, Options{Binary: missing})

	done := make(chan struct{})
	go func() {
		_ = d.Shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Shutdown 未在超时内返回")
	}
}

func TestCopyCronLogSplitsLines(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, strings.TrimPrefix(fmt.Sprintf(format, args...), "cron| "))
	}
	copyCronLog(logf, strings.NewReader("第一行\n第二行\n\n第三行（无行尾）"))
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 3 {
		t.Fatalf("应输出 3 行（空行跳过），得到 %d: %q", len(lines), lines)
	}
	if lines[0] != "第一行" || lines[2] != "第三行（无行尾）" {
		t.Fatalf("内容不符: %q", lines)
	}
}
