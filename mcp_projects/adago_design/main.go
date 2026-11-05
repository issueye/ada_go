// adago-design 是设计体系 MCP：内置多套设计体系 Token 与组件库，
// 提供浏览器无限画布（摆放/对比组件预览、截图回传）。
//
// 形态：
//
//	adago-design.exe            # 只跑画布 Web 服务（默认 --open 打开浏览器）
//	adago-design.exe mcp        # stdio MCP + 内嵌画布服务（Agent 接入）
//	adago-design.exe version    # 打印版本
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"adagodesign/internal/app"
	"adagodesign/internal/config"
	"adagodesign/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[adago-design] ")

	cmd := "serve"
	rest := os.Args[1:]
	if len(rest) > 0 {
		switch rest[0] {
		case "mcp", "serve", "version", "help":
			cmd = rest[0]
			rest = rest[1:]
		}
	}
	var err error
	switch cmd {
	case "version":
		fmt.Println("adago-design", config.Version)
	case "help":
		printHelp()
	case "mcp":
		err = runMCP(rest)
	default:
		err = runServe(rest)
	}
	if err != nil {
		log.Fatalf("退出: %v", err)
	}
}

func printHelp() {
	fmt.Print(`adago-design ` + config.Version + ` — 设计体系 MCP（浏览器无限画布）

用法:
  adago-design [serve] [flags]   启动画布 Web 服务（默认自动打开浏览器）
  adago-design mcp [flags]       stdio MCP 模式（内嵌画布服务，供 Agent 使用）
  adago-design version           打印版本号

Flags:
  -port int      画布 HTTP 端口，默认 8788（被占用自动 +1 探测）
  -data string   数据目录（默认 %APPDATA%\adago-design，可被 ADAGO_DESIGN_DATA 覆盖）
  -open          启动后打开系统默认浏览器（serve 模式默认开启；mcp 模式需显式指定，
                 或设置 ADAGO_DESIGN_OPEN=1）
  -theme string  画布界面主题 dark|light，默认 dark
`)
}

// runServe 人工查看模式：只跑画布服务（默认打开浏览器）。
func runServe(args []string) error {
	cfg, err := config.FromFlags("adago-design serve", args)
	if err != nil {
		return err
	}
	// serve 模式默认打开浏览器，除非显式 -open=false / -no-open / 环境变量关闭
	if !config.HasOpenFlag(args) && os.Getenv("ADAGO_DESIGN_OPEN") != "1" {
		cfg.Open = !hasNoOpen(args)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := app.Start(ctx, cfg)
	if err != nil {
		return err
	}
	log.Printf("按 Ctrl+C 退出。")
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Stop(shutdownCtx)
}

func hasNoOpen(args []string) bool {
	for _, a := range args {
		if a == "-no-open" || a == "--no-open" {
			return true
		}
	}
	return false
}

// runMCP Agent 接入模式：stdio MCP + 内嵌画布服务。
func runMCP(args []string) error {
	cfg, err := config.FromFlags("adago-design mcp", args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := app.Start(ctx, cfg)
	if err != nil {
		return err
	}
	// 截图任务 GC，防长驻泄漏
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				srv.Tasks().GC()
			}
		}
	}()

	deps := mcpserver.Deps{
		Store:   srv.Store(),
		Tasks:   srv.Tasks(),
		BaseURL: func() string { return srv.BaseURL },
		DataDir: cfg.DataDir,
	}
	server := mcpserver.NewServer(deps)
	log.Printf("MCP(stdio) 已就绪，画布: %s", srv.BaseURL)
	runErr := server.Run(ctx, &mcp.StdioTransport{})
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Stop(shutdownCtx)
	return runErr
}
