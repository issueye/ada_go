// Command adag 是 ada-go 的唯一二进制：一个可执行文件，两种进程角色（设计 3.1）。
//
//	subcommands: gateway | core | plugin | doctor | verify | export | backup | version
//
// M0 只冻结契约，因此除 verify / doctor / core --self-check / version 之外的命令
// 一律明确失败关闭（打印所属里程碑与非 0 退出码），不假装能用、不静默返回成功。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/corerpc"
	"github.com/adag/ada-go/internal/diag"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/proto"
	"github.com/adag/ada-go/internal/version"
	"github.com/adag/ada-go/tools/verify"
)

const usage = `adag —— 本地优先的 Agent 系统（单二进制，两角色）

用法：
  adag gateway    [--addr 127.0.0.1:52353]      Gateway：鉴权 + SSR + SSE（M2）
  adag core       --workspace <dir> [--product <file>] [--self-check]
                                                Agent Core：JSON-RPC over 回环 WebSocket（M2）
  adag chat       [--workspace <dir>] [--fake]  交互式会话（M1）
  adag print      [--workspace <dir>] --prompt <text>
                                                单轮执行并输出（M1）
  adag threads    [--workspace <dir>]           列出线程（M1）
  adag events     --thread <id>                 以 JSONL 导出线程事件/entry（M1）
  adag plugin     <list|install|reload>         插件管理（M3 落地）
  adag doctor     [--fake] [--data-dir <dir>]   无模型依赖的启动自检
  adag verify     [检查名...]                    开发门禁：generated dispatch hooks slots
                                                  capabilities manifest vnode products docs
                                                  nodecompat ui boundary
  adag export     --thread <id> [--format jsonl|markdown] [--out <file>]
                                                只读导出会话（M1）
  adag backup     --to <file>                   在线一致性备份（M1）
  adag version                                  版本与协议信息

里程碑说明见 docs/development-plan.md。`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "adag:", err)
		os.Exit(exitCode(err))
	}
}

// exitCode：契约未落地与检查失败都用 1，用法错误用 2。
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := err.(*usageError); ok {
		_ = e
		return 2
	}
	return 1
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stdout, usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, usage)
		return nil
	case "gateway":
		return cmdGateway(rest, stdout)
	case "core":
		return cmdCore(rest, stdout)
	case "chat":
		return cmdChat(rest, stdout, stderr)
	case "print":
		return cmdPrint(rest, stdout)
	case "threads":
		return cmdThreads(rest, stdout)
	case "events":
		return cmdEvents(rest, stdout)
	case "plugin":
		return cmdPlugin(rest, stdout)
	case "doctor":
		return cmdDoctor(rest, stdout)
	case "verify":
		return cmdVerify(rest, stdout)
	case "export":
		return cmdExport(rest, stdout)
	case "backup":
		return cmdBackup(rest, stdout)
	case "version":
		return cmdVersion(stdout)
	default:
		fmt.Fprintln(stderr, usage)
		return &usageError{msg: "未知命令 " + cmd}
	}
}

// notImplemented 把「契约已冻结、实现未落地」说清楚：属于哪个里程碑、下一步做什么。
func notImplemented(cmd, milestone string) error {
	entry, _ := diag.For(proto.CodeCoreUnavailable)
	return fmt.Errorf("adag %s 还没有实现（%s）。\n  诊断码 %s\n  下一步：见 docs/development-plan.md 对应里程碑；当前可用命令：verify、doctor、core --self-check、version",
		cmd, milestone, entry.Diag.String())
}

// cmdGateway 启动 M2 的 HTTP 外壳（鉴权、SSR、SSE 与 Core 监督）。
func cmdGateway(args []string, stdout io.Writer) error {
	return runGateway(args, stdout)
}

// cmdCore 支持 --self-check（只验证装配与 dispatch 覆盖）与默认的运行模式：
// 打开工作区、装配插件、监听回环 WebSocket，提供 Core JSON-RPC 服务。
func cmdCore(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("core", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "工作区目录（不存在则拒绝拉起，见 4.2）")
	product := fs.String("product", defaultProductPath(), "产品声明文件")
	parentPID := fs.Int("parent-pid", 0, "Gateway 父进程 pid（心跳与孤儿防护）")
	listen := fs.String("listen", "127.0.0.1:0", "监听地址；非回环必须设置 ADAG_CORE_TOKEN")
	selfCheck := fs.Bool("self-check", false, "只验证装配与 dispatch 覆盖，不启动循环")
	baseURL := fs.String("base-url", "", "OpenAI 兼容端点（provider 插件用）")
	provider := fs.String("provider", "openai", "provider id")
	model := fs.String("model", "gpt-4o", "模型 id")
	dataDir := fs.String("data-dir", "", "数据目录（默认 ~/.adago/workspaces/<hash>）")
	fake := fs.Bool("fake", false, "使用确定性回显模型（不需要密钥）")
	var allowHosts stringList
	fs.Var(&allowHosts, "allow-host", "额外出网 host/端口（可重复）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := coreapp.LoadProduct(*product)
	if err != nil {
		return err
	}
	ws := *workspace
	if ws == "" {
		ws = "."
	}
	if *selfCheck {
		bridge, berr := host.NewHTTPBridge(host.HTTPBridgeConfig{AllowHosts: []string{"api.openai.com"}})
		if berr != nil {
			return berr
		}
		defer bridge.Close()
		hc := host.Context{Workspace: ws, HTTP: bridge}
		asm, err := app.LoadOfficial(context.Background(), *product, app.Deps{Context: hc, BaseURL: *baseURL})
		if err != nil {
			return err
		}
		defer func() { _ = asm.Close(context.Background()) }()
		server := coreproto.NewServer(p.ID, instanceID(p.ID, *workspace), os.Getpid())
		if err := server.CheckCoverage(); err != nil {
			return err
		}
		rt := asm.Runtime
		out := map[string]any{
			"product":           p.ID,
			"plugins":           len(p.Plugins),
			"tools":             rt.ToolCount,
			"providers":         rt.ProviderCount,
			"methodsDispatched": len(server.Registered()),
			"protoVersion":      version.ProtocolVersion,
			"emptyBase":         rt.IsEmptyBase(),
			"emptyStateNotice":  rt.EmptyStateNotice(),
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if *workspace == "" {
		return &usageError{msg: "adag core 需要 --workspace（为空气目录起进程只会白占资源，见 4.2）"}
	}
	abs, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("adag core: 工作区不存在 %s（workspace_missing，ADG-WS-001）: %w", abs, err)
	}
	if *parentPID == 0 {
		return &usageError{msg: "adag core 需要 --parent-pid：Core 必须能发现父进程消失并自行退出（4.2）"}
	}

	token := strings.TrimSpace(os.Getenv("ADAG_CORE_TOKEN"))
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("adag core: 监听 %s 失败: %w", *listen, err)
	}
	defer ln.Close()
	if !listenIsLoopback(ln) && token == "" {
		return fmt.Errorf("adag core: 非回环监听必须设置 ADAG_CORE_TOKEN（4.3）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sess, err := cli.Open(serveCtx, cli.Config{
		Workspace:  *workspace,
		DataDir:    *dataDir,
		Product:    *product,
		Provider:   *provider,
		Model:      *model,
		BaseURL:    *baseURL,
		AllowHosts: allowHosts,
		Fake:       *fake,
		In:         strings.NewReader(""),
		Out:        io.Discard,
		Err:        os.Stderr,
	})
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close(context.Background()) }()

	rpc, err := corerpc.New(corerpc.Options{
		Session: sess, Context: serveCtx, Token: token, OnShutdown: cancel,
	})
	if err != nil {
		return err
	}
	defer rpc.Close()
	if err := rpc.Core().CheckCoverage(); err != nil {
		return err
	}
	go corerpc.WatchParent(serveCtx, *parentPID, cancel)

	// 握手行：Gateway 5 秒内读取（4.2）。token 只走环境变量，不进命令行。
	handshake := map[string]any{
		"endpoint":     ln.Addr().String(),
		"pid":          os.Getpid(),
		"product":      p.ID,
		"version":      version.String(),
		"protoVersion": version.ProtocolVersion,
		"workspace":    abs,
		"readyAt":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(handshake); err != nil {
		return err
	}
	return rpc.Serve(serveCtx, ln)
}

// listenIsLoopback 判断监听地址是否只落在回环上。
func listenIsLoopback(ln net.Listener) bool {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return false
	}
	return addr.IP != nil && addr.IP.IsLoopback()
}

func instanceID(product, workspace string) string {
	return product + "::" + filepath.Base(filepath.Clean(workspace))
}

// cmdPlugin：M3 落地。
func cmdPlugin(args []string, stdout io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if sub == "" {
		return &usageError{msg: "adag plugin 需要子命令：list | install | reload"}
	}
	return notImplemented("plugin "+sub, "M3：完整插件平台（安装、发现、信任、热重载）")
}

// cmdVerify 跑开发门禁。
func cmdVerify(args []string, stdout io.Writer) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	patterns := args
	if len(patterns) == 0 {
		patterns = []string{"all"}
	}
	results, err := verify.Run(stdout, root, patterns)
	if err != nil {
		return err
	}
	if err := verify.Summarize(stdout, results); err != nil {
		return err
	}
	return nil
}

// cmdDoctor 是无模型依赖的启动自检（设计 10）。
// 已接：配置/产品/目录、SQLite 完整性与 schema、工作区锁持有者、provider 配置形状；
// 端口占用留给 M2 Gateway。
func cmdDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fake := fs.Bool("fake", false, "不依赖真实模型：用产品声明与空基座做自检")
	dataDir := fs.String("data-dir", "", "数据目录（默认按工作区哈希推到 ~/.adago/workspaces/<hash>）")
	workspace := fs.String("workspace", ".", "工作区目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}

	report := func(name string, err error) error {
		if err != nil {
			fmt.Fprintf(stdout, "FAIL  %-16s %v\n", name, err)
			return err
		}
		fmt.Fprintf(stdout, "OK    %-16s\n", name)
		return nil
	}

	var firstErr error
	note := func(name, why string) {
		fmt.Fprintf(stdout, "SKIP  %-16s %s\n", name, why)
	}

	if err := report("仓库根", checkRepoRoot(root)); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := report("spec 文件", checkSpecFiles(root)); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := report("产品声明", checkProductsBoot(root)); err != nil && firstErr == nil {
		firstErr = err
	}
	dir := *dataDir
	if dir == "" {
		if abs, aerr := filepath.Abs(*workspace); aerr == nil {
			if defaultDir, derr := sqlite.DefaultDataDir(abs); derr == nil {
				dir = defaultDir
			}
		}
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".adago")
		}
	}
	if err := report("数据目录", checkDataDir(dir)); err != nil && firstErr == nil {
		firstErr = err
	}
	if *fake {
		note("provider(fake)", checkFakeProvider())
	} else {
		note("provider", "未加 --fake：真实供应商连通性检查需要密钥；配置形状检查见官方 provider 插件的测试")
	}
	if _, err := os.Stat(sqlite.StateDBPath(dir)); err != nil {
		note("sqlite", "该数据目录还没有 state.db（工作区尚未被 Core/CLI 启动过）")
	} else if err := checkSQLite(dir); err != nil {
		if firstErr == nil {
			firstErr = err
		}
		fmt.Fprintf(stdout, "FAIL  %-16s %v\n", "sqlite", err)
	} else {
		fmt.Fprintf(stdout, "OK    %-16s 完整性 / WAL / schema 检查通过\n", "sqlite")
	}
	if err := checkLock(*workspace, dir); err != nil {
		if firstErr == nil {
			firstErr = err
		}
		fmt.Fprintf(stdout, "FAIL  %-16s %v\n", "locks", err)
	} else {
		fmt.Fprintf(stdout, "OK    %-16s 锁状态正常（空闲或由运行中的 Core 持有）\n", "locks")
	}
	note("ports", "端口占用检查在 M2 Gateway 落地后接入")

	fmt.Fprintln(stdout, "")
	if firstErr != nil {
		return fmt.Errorf("doctor 发现问题（首个：%w）；诊断码见 internal/diag", firstErr)
	}
	fmt.Fprintln(stdout, "doctor: 自检通过（M1 范围）。Gateway 端口与 UI 检查随 M2 接入。")
	return nil
}

func cmdVersion(stdout io.Writer) error {
	lines := []string{
		"adag " + version.String(),
		"product: " + version.Product + " / binary: " + version.Binary,
		fmt.Sprintf("plugin API: %d.%d", version.APIVersionMajor, version.APIVersionMinor),
		fmt.Sprintf("gateway↔core protocol: %d", version.ProtocolVersion),
		fmt.Sprintf("methods: %d, events: %d, errorCodes: %d",
			len(proto.AllMethodNames()), len(proto.AllEventNames()), len(proto.ErrorCodes)),
	}
	fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	return nil
}
