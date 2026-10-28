// Command adag 是 ada-go 的唯一二进制：一个可执行文件，两种进程角色（设计 3.1）。
//
//	subcommands: gateway | core | plugin | doctor | verify | export | backup | version
//
// M0 只冻结契约，因此除 verify / doctor / core --self-check / version 之外的命令
// 一律明确失败关闭（打印所属里程碑与非 0 退出码），不假装能用、不静默返回成功。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/diag"
	"github.com/adag/ada-go/internal/proto"
	"github.com/adag/ada-go/internal/version"
	"github.com/adag/ada-go/tools/verify"
)

const usage = `adag —— 本地优先的 Agent 系统（单二进制，两角色）

用法：
  adag gateway    [--addr 127.0.0.1:52353]      Gateway：HTTP/SSR/SSE（M2 落地）
  adag core       --workspace <dir> [--product <file>] [--self-check]
                                                Agent Core：每工作区一个进程（M1 落地）
  adag plugin     <list|install|reload>         插件管理（M3 落地）
  adag doctor     [--fake] [--data-dir <dir>]   无模型依赖的启动自检
  adag verify     [检查名...]                    开发门禁：generated dispatch hooks slots
                                                  capabilities manifest vnode products docs
                                                  nodecompat ui boundary
  adag export     --thread <id>                 只读导出会话（M1-B 落地）
  adag backup     --to <file>                   在线一致性备份（M1-B 落地）
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
	case "plugin":
		return cmdPlugin(rest, stdout)
	case "doctor":
		return cmdDoctor(rest, stdout)
	case "verify":
		return cmdVerify(rest, stdout)
	case "export", "backup":
		return notImplemented(cmd, "M1-B：SQLite 会话存储与只读 CLI")
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

// cmdGateway：M2 落地。这里只声明监听与安全检查的意图，不假装可服务。
func cmdGateway(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:52353", "监听地址（非回环必须同时给 --token 与 TLS 或 --allow-plaintext）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return notImplemented("gateway "+*addr, "M2：Gateway、SSR 与可用界面")
}

// cmdCore 支持 --self-check：用产品声明驱动空基座装配，验证「不装任何插件也能启动」。
func cmdCore(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("core", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "工作区目录（不存在则拒绝拉起，见 4.2）")
	product := fs.String("product", "products/skeleton.agent.json", "产品声明文件")
	parentPID := fs.Int("parent-pid", 0, "Gateway 父进程 pid（心跳与孤儿防护）")
	listen := fs.String("listen", "127.0.0.1:0", "只监听回环的临时端口")
	selfCheck := fs.Bool("self-check", false, "只验证装配与 dispatch 覆盖，不启动循环")
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := coreapp.LoadProduct(*product)
	if err != nil {
		return err
	}
	rt, err := coreapp.Assemble(p)
	if err != nil {
		return err
	}
	server := coreproto.NewServer(p.ID, instanceID(p.ID, *workspace), os.Getpid())
	if err := server.CheckCoverage(); err != nil {
		return err
	}

	if *selfCheck {
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

	// 握手行：Gateway 5 秒内读取（4.2）。token 只走环境变量，不进命令行。
	handshake := map[string]any{
		"endpoint":     *listen,
		"pid":          os.Getpid(),
		"product":      p.ID,
		"version":      version.String(),
		"protoVersion": version.ProtocolVersion,
	}
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(handshake); err != nil {
		return err
	}
	return notImplemented("core（运行循环）", "M1：RunTurn、SQLite 会话与插件宿主")
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

// cmdDoctor 是无模型依赖的启动自检（设计 10）。M0 覆盖配置/产品/边界/目录四项，
// 数据库与网络盘检查在 M1-B/M5 接入；未接到的检查显式说明，而不是默默跳过。
func cmdDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fake := fs.Bool("fake", false, "不依赖真实模型：用产品声明与空基座做自检")
	dataDir := fs.String("data-dir", "", "数据目录（默认 ~/.adago）")
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
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".adago")
	}
	if err := report("数据目录", checkDataDir(dir)); err != nil && firstErr == nil {
		firstErr = err
	}
	if *fake {
		note("provider(fake)", checkFakeProvider())
	} else {
		note("provider", "未加 --fake：真实供应商连通性检查属于 M1-E 之后")
	}
	note("sqlite", "数据库完整性/WAL/网络盘检查在 M1-B 落地后接入")
	note("locks", "工作区锁持有者检查在 M1-B 落地后接入")
	note("ports", "端口占用检查在 M2 Gateway 落地后接入")

	fmt.Fprintln(stdout, "")
	if firstErr != nil {
		return fmt.Errorf("doctor 发现问题（首个：%w）；诊断码见 internal/diag", firstErr)
	}
	fmt.Fprintln(stdout, "doctor: 自检通过（M0 范围）。真实运行能力随里程碑逐步接入。")
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
