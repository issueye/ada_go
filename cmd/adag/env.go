// adag env —— 环境变量模块的命令行入口（设计：全局 ~/.adago/env.json +
// <workspace>/.adag/env.json，工作区覆盖全局）。
//
//	adag env list  [--workspace <dir>] [--json]   列出变量（带 --workspace 时合并工作区）
//	adag env set   KEY=VALUE... [--workspace <dir>] 写入变量（缺省写全局）
//	adag env unset KEY... [--workspace <dir>]       删除变量（缺省改全局）
//
// 写入的文件会被宿主注入插件启动的子进程（ctx.exec / 后台任务 / process.start）；
// 修改后通过 plugin.reload 重建装配即可生效，不需要重启进程。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/envstore"
)

func cmdEnv(args []string, stdout io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		return envList(args[1:], stdout)
	case "set":
		return envSet(args[1:], stdout)
	case "unset":
		return envUnset(args[1:], stdout)
	case "":
		return &usageError{msg: "adag env 需要子命令：list | set | unset"}
	default:
		return &usageError{msg: "未知子命令 adag env " + sub + "（可用：list | set | unset）"}
	}
}

// envTarget 解析 --workspace：给了就写工作区文件，否则写全局文件。
func envTarget(workspace string) (string, string, error) {
	if ws := strings.TrimSpace(workspace); ws != "" {
		path, err := envstore.WorkspacePath(ws)
		if err != nil {
			return "", "", err
		}
		return path, "工作区", nil
	}
	path, err := envstore.GlobalPath()
	if err != nil {
		return "", "", err
	}
	return path, "全局", nil
}

func envList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("env list", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "同时读取该工作区的 .adag/env.json（工作区覆盖全局）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	vars := map[string]string{}
	var warnings []string
	if strings.TrimSpace(*workspace) != "" {
		vars, warnings = envstore.LoadForWorkspace(*workspace)
	} else {
		path, err := envstore.GlobalPath()
		if err != nil {
			return err
		}
		var loadWarns []string
		vars, loadWarns, err = envstore.LoadFile(path)
		if err != nil {
			return err
		}
		warnings = loadWarns
	}
	for _, w := range warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"vars": vars})
	}
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		fmt.Fprintln(stdout, "（没有环境变量）")
		return nil
	}
	for _, key := range keys {
		fmt.Fprintf(stdout, "%s=%s\n", key, vars[key])
	}
	return nil
}

func envSet(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "写入该工作区的 .adag/env.json（缺省写全局）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return &usageError{msg: "adag env set 需要 KEY=VALUE（可一次写多个）"}
	}
	assignments := make(map[string]string, len(rest))
	order := make([]string, 0, len(rest))
	for _, arg := range rest {
		key, value, err := envstore.ParseAssignment(arg)
		if err != nil {
			return err
		}
		if _, dup := assignments[key]; !dup {
			order = append(order, key)
		}
		assignments[key] = value
	}

	path, scope, err := envTarget(*workspace)
	if err != nil {
		return err
	}
	vars, warnings, err := envstore.LoadFile(path)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	for key, value := range assignments {
		vars[key] = value
	}
	if err := envstore.SaveFile(path, vars); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已写入%s环境变量 %s（%s）\n", scope, strings.Join(order, ", "), path)
	return nil
}

func envUnset(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("env unset", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "修改该工作区的 .adag/env.json（缺省改全局）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keys := fs.Args()
	if len(keys) == 0 {
		return &usageError{msg: "adag env unset 需要变量名（可一次删多个）"}
	}
	for _, key := range keys {
		if err := envstore.ValidateKey(key); err != nil {
			return err
		}
	}
	path, scope, err := envTarget(*workspace)
	if err != nil {
		return err
	}
	vars, warnings, err := envstore.LoadFile(path)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	removed := make([]string, 0, len(keys))
	missing := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := vars[key]; !ok {
			missing = append(missing, key)
			continue
		}
		delete(vars, key)
		removed = append(removed, key)
	}
	if len(removed) > 0 {
		if err := envstore.SaveFile(path, vars); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "已移除%s环境变量 %s（%s）\n", scope, strings.Join(removed, ", "), path)
	}
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "未设置：%s\n", strings.Join(missing, ", "))
	}
	return nil
}
