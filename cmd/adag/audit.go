// adag audit：只读查看审计表（M5，09 §10）。
//
// 审计范围：交互请求/响应、工具执行、插件加载/重载/禁用、鉴权失败、配置变更、
// 轮次取消与检查点回滚（storage.restore）。本命令只开只读连接，绝不写库。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/adapters/sqlite"
)

func cmdAudit(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return &usageError{msg: "adag audit 需要 list 子命令（adag audit list --limit N）"}
	}
	fs := flag.NewFlagSet("audit list", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "工作区目录")
	dataDir := fs.String("data-dir", "", "数据目录（默认 ~/.adago/workspaces/<hash>）")
	limit := fs.Int("limit", 50, "返回条数（最新在前）")
	since := fs.String("since", "", "只看该 RFC3339 时刻之后的记录")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *limit <= 0 {
		return &usageError{msg: "adag audit list: --limit 必须大于 0"}
	}
	var sinceAt time.Time
	if strings.TrimSpace(*since) != "" {
		t, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			return &usageError{msg: "adag audit list: --since 必须是 RFC3339 时间"}
		}
		sinceAt = t
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := sqlite.OpenReadOnly(ctx, sqlite.Config{
		Workspace: *workspace, DataDir: *dataDir,
	})
	if err != nil {
		return fmt.Errorf("adag audit: 打开只读连接失败（工作区还没有被 Core/CLI 启动过？）: %w", err)
	}
	defer r.Close()
	rows, err := r.ListAudit(ctx, *limit, sinceAt)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "（audit 表为空）")
		return nil
	}
	for _, rec := range rows {
		detail := ""
		if len(rec.Detail) > 0 {
			detail = string(rec.Detail)
		}
		fmt.Fprintf(stdout, "%s  %-18s thread=%s plugin=%s %s\n",
			rec.At.UTC().Format(time.RFC3339), rec.Kind,
			rec.ThreadID, rec.PluginID, detail)
	}
	fmt.Fprintf(stdout, "共 %d 条（最新在前，limit=%d）\n", len(rows), *limit)
	return nil
}
