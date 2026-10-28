// adag plugin —— M3 插件平台命令（设计 6.1、6.9）：
//
//	adag plugin list    [--workspace dir] [--global]   发现层列表（随包 + 工作区 + 全局）
//	adag plugin install --zip <file> [--workspace dir]  安装 zip（不覆盖同名 id）
//	adag plugin reload  [--workspace dir] [--plugin id]发现校验；热重载要经运行中的 Core
//
// list 与 install 是离线操作：只碰发现目录与 zip，不打扰运行中的 Core（04 §5.7：
// 安装只写插件目录，启用状态由 Core 落 plugin_meta）。reload 在 CLI 里没有可热替换
// 的运行代（会话随进程结束关闭），因此只做发现校验，并提示通过 plugin.reload RPC
// 让 Gateway → Core 执行真正的双代热重载。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/plugins/discovery"
)

func cmdPlugin(args []string, stdout io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		return pluginList(args[1:], stdout)
	case "install":
		return pluginInstall(args[1:], stdout)
	case "reload":
		return pluginReload(args[1:], stdout)
	case "":
		return &usageError{msg: "adag plugin 需要子命令：list | install | reload"}
	default:
		return &usageError{msg: "未知子命令 adag plugin " + sub + "（可用：list | install | reload）"}
	}
}

// pluginRow 是 CLI 表格的一行（与 cli.PluginView 同口径的离线版）。
type pluginRow struct {
	id          string
	version     string
	trust       string
	enabled     bool
	knownEnable bool
	source      string
	fingerprint string
	note        string
}

func pluginList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "工作区目录")
	global := fs.Bool("global", true, "包含全局插件目录（~/.adago/plugins）")
	dataDir := fs.String("data-dir", "", "数据目录（读 plugin_meta 的启用状态；默认按工作区哈希推）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}

	rows, warnings := collectPluginRows(abs, *global)
	// 启用状态是 Core 落库的用户决定（04 §5.7）：只读连接能读到就带上。
	meta, err := cli.ListPluginMetaOffline(context.Background(), abs, *dataDir)
	if err != nil {
		for i := range rows {
			rows[i].knownEnable = false
		}
		warnings = append(warnings, "读取启用状态失败（"+err.Error()+"），enabled 列显示 -")
	} else {
		for i := range rows {
			if rec, ok := meta[rows[i].id]; ok {
				rows[i].enabled = rec.Enabled
				rows[i].knownEnable = true
				rows[i].note = rec.LastError
			}
		}
	}

	for _, w := range warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	fmt.Fprintf(stdout, "%-24s  %-10s  %-12s  %-8s  %-10s  %s\n",
		"ID", "VERSION", "TRUST", "ENABLED", "SOURCE", "FINGERPRINT")
	for _, r := range rows {
		enabled := "-"
		if r.knownEnable {
			enabled = fmt.Sprintf("%v", r.enabled)
		}
		note := ""
		if r.note != "" {
			note = "  (" + r.note + ")"
		}
		fmt.Fprintf(stdout, "%-24s  %-10s  %-12s  %-8s  %-10s  %s%s\n",
			r.id, r.version, r.trust, enabled, r.source, r.fingerprint, note)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "（没有发现任何插件）")
	}
	return nil
}

// collectPluginRows 汇总随包 + 工作区 + 全局三路发现结果（工作区优先，6.1）。
func collectPluginRows(workspace string, includeGlobal bool) ([]pluginRow, []string) {
	var warnings []string
	var rows []pluginRow

	base, err := app.OfficialScripts()
	if err != nil {
		warnings = append(warnings, "读取随包插件失败: "+err.Error())
	}
	for _, sc := range base {
		if sc.Manifest == nil {
			continue
		}
		rows = append(rows, pluginRow{
			id: sc.Manifest.ID, version: sc.Manifest.Version,
			trust: string(sc.Manifest.Trust), enabled: true, knownEnable: true,
			source: "builtin",
		})
	}
	builtinIDs := map[string]bool{}
	for _, r := range rows {
		builtinIDs[r.id] = true
	}

	ws, warns, err := discovery.LoadDirDetailed(discovery.WorkspaceRoot(workspace), discovery.SourceWorkspace)
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	merged := ws
	if includeGlobal {
		if root, gerr := discovery.GlobalRoot(); gerr == nil {
			gl, gw, rerr := discovery.LoadDirDetailed(root, discovery.SourceGlobal)
			if rerr != nil {
				warnings = append(warnings, rerr.Error())
			}
			m, mw := discovery.MergeByPriority(merged, gl)
			merged = m
			warnings = append(warnings, warnStrings(gw)...)
			warnings = append(warnings, warnStrings(mw)...)
		} else {
			warnings = append(warnings, gerr.Error())
		}
	}
	warnings = append(warnings, warnStrings(warns)...)
	for _, p := range merged {
		if p.Script.Manifest == nil || builtinIDs[p.Script.Manifest.ID] {
			continue
		}
		m := p.Script.Manifest
		rows = append(rows, pluginRow{
			id: m.ID, version: m.Version, trust: string(m.Trust),
			enabled: true, knownEnable: true,
			source: p.Source, fingerprint: p.Fingerprint,
		})
	}
	return rows, warnings
}

func pluginInstall(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	zipPath := fs.String("zip", "", "插件 zip 文件")
	workspace := fs.String("workspace", ".", "安装目标工作区")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*zipPath) == "" {
		return &usageError{msg: "adag plugin install 需要 --zip <file>"}
	}
	abs, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	id, err := discovery.InstallZip(*zipPath, abs)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已安装插件 %s 到 %s\n", id, discovery.WorkspaceRoot(abs))
	fmt.Fprintln(stdout, "下一步：")
	fmt.Fprintln(stdout, "  - 重启 Core（adag core --workspace <dir>）；或")
	fmt.Fprintln(stdout, "  - 对运行中的 Core 发 plugin.reload RPC（经 Gateway），随后用 plugin.setEnabled 控制启停。")
	return nil
}

func pluginReload(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin reload", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "工作区目录")
	pluginID := fs.String("plugin", "", "只校验指定插件（缺省校验全部）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	ws, warns, err := discovery.LoadDirDetailed(discovery.WorkspaceRoot(abs), discovery.SourceWorkspace)
	if err != nil {
		return err
	}
	for _, w := range warns {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	count := 0
	for _, p := range ws {
		if *pluginID != "" && p.ID() != *pluginID {
			continue
		}
		count++
		fmt.Fprintf(stdout, "OK  %-24s %s（fingerprint %s）\n", p.ID(), p.Script.Manifest.Version, p.Fingerprint)
	}
	if *pluginID != "" && count == 0 {
		return fmt.Errorf("adag plugin reload: 工作区插件目录里没有发现 %q", *pluginID)
	}
	fmt.Fprintf(stdout, "发现校验通过（%d 个插件）。运行中 Core 的热重载是双代切换，需要经 Gateway/plugin.reload RPC 触发；", count)
	fmt.Fprintln(stdout, "CLI 会话随进程结束关闭，没有可热替换的运行代。")
	return nil
}

func warnStrings(warnings []discovery.Warning) []string {
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, w.String())
	}
	return out
}
