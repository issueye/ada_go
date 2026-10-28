package main

// adag gateway —— M2 的 HTTP 外壳：鉴权、SSR、SSE 与 Core 进程监督。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/adag/ada-go/internal/gateway"
)

func runGateway(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:52353", "监听地址（M2 第一切片只允许回环）")
	var workspaces stringList
	fs.Var(&workspaces, "workspace", "工作区目录（可重复；默认 .）")
	product := fs.String("product", defaultProductPath(), "产品声明文件")
	dataDir := fs.String("data-dir", "", "Core 数据目录")
	provider := fs.String("provider", "openai", "provider id")
	model := fs.String("model", "gpt-4o", "模型 id")
	baseURL := fs.String("base-url", "", "OpenAI 兼容端点")
	fake := fs.Bool("fake", false, "Core 使用确定性回显模型")
	maxCores := fs.Int("max-cores", 8, "同时存活的 Core 上限")
	idle := fs.Duration("idle-reap", gateway.DefaultIdle, "空闲 Core 回收时间")
	var allowHosts stringList
	fs.Var(&allowHosts, "allow-host", "Core 额外出网 host/端口（可重复）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(workspaces) == 0 {
		workspaces = stringList{"."}
	}
	if err := requireLoopback(*addr); err != nil {
		return err
	}

	accessToken := strings.TrimSpace(os.Getenv("ADAG_GATEWAY_TOKEN"))
	generated := accessToken == ""
	cfg := gateway.Config{
		Workspaces: workspaces, Product: *product,
		Provider: *provider, Model: *model, BaseURL: *baseURL,
		DataDir: *dataDir, AllowHosts: allowHosts, Fake: *fake,
		Token:     accessToken,
		CoreToken: strings.TrimSpace(os.Getenv("ADAG_CORE_TOKEN")),
		MaxCores:  *maxCores, Idle: *idle,
		Secure: false,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	gw, err := gateway.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = gw.Shutdown(context.Background()) }()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("adag gateway: 监听 %s 失败: %w", *addr, err)
	}
	defer ln.Close()
	srv := &http.Server{
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	out := map[string]any{
		"endpoint":   "http://" + ln.Addr().String(),
		"workspace":  gw.Workspace(),
		"workspaces": gw.Workspaces(),
		"product":    gw.Product().ID,
		"pid":        os.Getpid(),
		"readyAt":    time.Now().UTC().Format(time.RFC3339Nano),
	}
	if generated {
		// 首次配对码：只在自动生成时打印一次；用户设置环境变量后不回显。
		out["token"] = gw.AccessToken()
	}
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(out); err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// requireLoopback 拒绝非回环监听：M2 第一切片不做 TLS/配对码登录。
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("adag gateway: 地址 %q 非法: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("adag gateway: M2 第一切片只允许回环监听（收到 %q）；非回环需要 TLS 与配对流程", addr)
	}
	return nil
}
