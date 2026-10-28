package main

// CLI 会话面（开发计划 M1-G）：chat / print / threads / events / export / backup。
//
// 这里只做参数解析与输出；装配、引擎与存储接线在 internal/cli。

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/domain"
)

// stringList 是可重复的字符串参数（--allow-host a --allow-host b）。
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type sessionFlags struct {
	workspace   string
	dataDir     string
	product     string
	provider    string
	model       string
	baseURL     string
	fake        bool
	interactive bool
	events      string
	thread      string
	allowHosts  stringList
}

func addSessionFlags(fs *flag.FlagSet, f *sessionFlags, interactive bool) {
	fs.StringVar(&f.workspace, "workspace", ".", "工作区目录")
	fs.StringVar(&f.dataDir, "data-dir", "", "数据目录（默认 ~/.adago/workspaces/<hash>）")
	fs.StringVar(&f.product, "product", defaultProductPath(), "产品声明文件")
	fs.StringVar(&f.provider, "provider", "openai", "provider id 或 provider 插件 id")
	fs.StringVar(&f.model, "model", "gpt-4o", "模型 id（provider 必须声明过）")
	fs.StringVar(&f.baseURL, "base-url", "", "OpenAI 兼容端点（默认 https://api.openai.com/v1）")
	fs.BoolVar(&f.fake, "fake", false, "使用确定性回显模型（不需要 API key）")
	fs.BoolVar(&f.interactive, "interactive", interactive, "把审批/提问转到终端（默认 chat 开启，print 关闭）")
	fs.StringVar(&f.events, "events", "", "把领域事件写成 JSONL 文件")
	fs.StringVar(&f.thread, "thread", "", "继续已有线程")
	fs.Var(&f.allowHosts, "allow-host", "额外出网 host/端口（可重复）")
}

func defaultProductPath() string {
	root, err := repoRoot()
	if err == nil {
		p := filepath.Join(root, "products", "headless.agent.json")
		if _, serr := os.Stat(p); serr == nil {
			return p
		}
	}
	return filepath.Join("products", "headless.agent.json")
}

func (f *sessionFlags) open(ctx context.Context, interactive bool) (*cli.Session, error) {
	return cli.Open(ctx, cli.Config{
		Workspace:   f.workspace,
		DataDir:     f.dataDir,
		Product:     f.product,
		Provider:    f.provider,
		Model:       f.model,
		BaseURL:     f.baseURL,
		AllowHosts:  f.allowHosts,
		Fake:        f.fake,
		Interactive: interactive,
		ThreadID:    f.thread,
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		EventsPath:  f.events,
	})
}

func cmdPrint(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("print", flag.ContinueOnError)
	var f sessionFlags
	addSessionFlags(fs, &f, false)
	prompt := fs.String("prompt", "", "提示词（位置参数也接受）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := *prompt
	if text == "" {
		text = strings.Join(fs.Args(), " ")
	}
	if strings.TrimSpace(text) == "" {
		return &usageError{msg: "adag print 需要提示词（--prompt 或位置参数）"}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sess, err := f.open(ctx, false)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close(context.Background()) }()
	if f.thread != "" {
		if err := sess.UseThread(ctx, f.thread); err != nil {
			return err
		}
	}
	term, err := sess.Run(ctx, text)
	fmt.Fprintln(stdout)
	if err != nil {
		return err
	}
	if term != domain.TerminalCompleted {
		return fmt.Errorf("轮次终态是 %s（不是 completed）", term)
	}
	return nil
}

func cmdChat(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	var f sessionFlags
	addSessionFlags(fs, &f, true)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sess, err := f.open(ctx, true)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close(context.Background()) }()
	if f.thread != "" {
		if err := sess.UseThread(ctx, f.thread); err != nil {
			return err
		}
	} else {
		th, err := sess.NewThread(ctx, "CLI 会话")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "线程 %s（/exit 退出，/new 新线程，/thread <id> 切换）\n", th.ID)
	}

	for {
		fmt.Fprint(stderr, "> ")
		line, err := sess.ReadLine()
		if err != nil && strings.TrimSpace(line) == "" {
			fmt.Fprintln(stdout)
			return nil
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case line == "/exit" || line == "/quit":
			return nil
		case line == "/new":
			th, err := sess.NewThread(ctx, "CLI 会话")
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "新线程 %s\n", th.ID)
			continue
		case strings.HasPrefix(line, "/thread "):
			id := strings.TrimSpace(strings.TrimPrefix(line, "/thread "))
			if err := sess.UseThread(ctx, id); err != nil {
				fmt.Fprintf(stderr, "切换失败: %v\n", err)
				continue
			}
			fmt.Fprintf(stdout, "已切换到 %s\n", id)
			continue
		}
		term, err := sess.Run(ctx, line)
		fmt.Fprintln(stdout)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "错误: %v\n", err)
		case term != domain.TerminalCompleted:
			fmt.Fprintf(stderr, "轮次终态: %s\n", term)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func cmdThreads(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("threads", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "工作区目录")
	dataDir := fs.String("data-dir", "", "数据目录")
	limit := fs.Int("limit", 100, "最多显示多少条")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := cli.ListThreads(ctx, *workspace, *dataDir, *limit)
	if err != nil {
		return err
	}
	for _, th := range items {
		archived := ""
		if th.Archived {
			archived = " [archived]"
		}
		fmt.Fprintf(stdout, "%s  %s  %s%s\n", th.ID, th.UpdatedAt.Format(time.RFC3339), th.Title, archived)
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "（没有线程）")
	}
	return nil
}

func cmdEvents(args []string, stdout io.Writer) error {
	return cmdExportWith(args, stdout, "jsonl")
}

func cmdExport(args []string, stdout io.Writer) error {
	return cmdExportWith(args, stdout, "")
}

func cmdExportWith(args []string, stdout io.Writer, forcedFormat string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "工作区目录")
	dataDir := fs.String("data-dir", "", "数据目录")
	thread := fs.String("thread", "", "线程 id（空=全部）")
	format := fs.String("format", "jsonl", "jsonl | markdown")
	out := fs.String("out", "", "输出文件（空=stdout）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f := *format
	if forcedFormat != "" {
		f = forcedFormat
	}
	w := stdout
	var closer io.Closer
	if *out != "" {
		file, err := os.Create(*out)
		if err != nil {
			return err
		}
		w = file
		closer = file
	}
	if closer != nil {
		defer closer.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	summary, err := cli.ExportThread(ctx, *workspace, *dataDir, *thread, f, w)
	if err != nil {
		return err
	}
	if *out != "" {
		fmt.Fprintf(stdout, "已导出 %d 线程 / %d entry / %d run（%s，快照 %s）\n",
			summary.Threads, summary.Entries, summary.Runs, summary.Format, summary.Snapshot)
	}
	return nil
}

func cmdBackup(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "工作区目录")
	dataDir := fs.String("data-dir", "", "数据目录")
	to := fs.String("to", "", "备份目标文件（必须不存在）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*to) == "" {
		return &usageError{msg: "adag backup 需要 --to <file>"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	info, err := cli.BackupWorkspace(ctx, *workspace, *dataDir, *to)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "备份完成：%s（%d 字节，schema v%d）\n", info.Dest, info.Bytes, info.SchemaVersion)
	return nil
}
