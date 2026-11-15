// adag env —— 环境变量模块的命令行入口。存储与运行时注入（cli.loadManagedEnv）、
// env-manager 管理页同源：global.config 的 env 命名空间——机器级
// $ADAGO_HOME/config/env.json、工作区级 <workspace>/.adago/config/env.json
// （工作区覆盖全局），变量放在 "vars" 键下。
//
//	adag env list  [--workspace <dir>] [--json]   列出变量（带 --workspace 时合并工作区）
//	adag env set   KEY=VALUE... [--workspace <dir>] 写入变量（缺省写机器级）
//	adag env unset KEY... [--workspace <dir>]       删除变量（缺省改机器级）
//
// 旧 envstore 文件（~/.adago/env.json 与 <workspace>/.adago/env.json）在对应层
// globalconfig 尚无 vars 时回退读取（与运行时注入同口径）；首次写入会把旧值
// 并入新文件——否则第一次 set 会把旧变量从注入面里挤掉。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/common/envstore"
	"github.com/adag/ada-go/internal/common/globalconfig"
)

// envNamespace 是 env 命名空间（globalconfig 的 <ns>.json 文件名）。
const envNamespace = "env"

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

// envStore 打开目标层的 env 命名空间：--workspace 给了就是工作区层，否则机器级。
// 工作区层先做旧 .adag/env.json → .adago/env.json 的物化，避免迁移播种时丢变量。
func envStore(workspace string) (*globalconfig.Store, string, error) {
	if ws := strings.TrimSpace(workspace); ws != "" {
		if err := envstore.MigrateWorkspace(ws); err != nil {
			return nil, "", err
		}
		s, err := globalconfig.OpenWorkspace(ws, envNamespace)
		return s, "工作区", err
	}
	s, err := globalconfig.Open(envNamespace)
	return s, "全局", err
}

// envLegacyPath 返回目标层的旧 envstore 文件路径（读不到主目录时为空）。
func envLegacyPath(workspace string) string {
	if ws := strings.TrimSpace(workspace); ws != "" {
		path, err := envstore.WorkspacePath(ws)
		if err != nil {
			return ""
		}
		return path
	}
	path, err := envstore.GlobalPath()
	if err != nil {
		return ""
	}
	return path
}

// readLayerVars 读取一层的 vars；该层 globalconfig 尚无 vars 时回退旧 envstore
//（legacy=true），与运行时注入的双层回退口径一致。
func readLayerVars(s *globalconfig.Store, legacyPath string) (map[string]string, bool, []string, error) {
	raw, found, err := s.Get("vars")
	if err != nil {
		return nil, false, nil, err
	}
	if found {
		var vars map[string]string
		if err := json.Unmarshal(raw, &vars); err != nil {
			return nil, false, nil, fmt.Errorf("vars 不是字符串键值对象: %w", err)
		}
		if vars == nil {
			vars = map[string]string{}
		}
		return vars, false, nil, nil
	}
	if legacyPath == "" {
		return map[string]string{}, true, nil, nil
	}
	vars, warnings, err := envstore.LoadFile(legacyPath)
	if err != nil {
		return nil, false, nil, err
	}
	if vars == nil {
		vars = map[string]string{}
	}
	return vars, true, warnings, nil
}

// writeLayerVars 把一层的 vars 写回 globalconfig（"vars" 键，与 env-manager 同形状）。
func writeLayerVars(s *globalconfig.Store, vars map[string]string) error {
	raw, err := json.Marshal(vars)
	if err != nil {
		return fmt.Errorf("环境变量无法序列化: %w", err)
	}
	return s.Set("vars", raw)
}

func envList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("env list", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "同时读取该工作区的 .adago/config/env.json（工作区覆盖全局）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	vars := map[string]string{}
	var warnings []string
	merge := func(layer map[string]string) {
		for k, v := range layer {
			vars[k] = v
		}
	}

	// 机器级。
	if s, err := globalconfig.Open(envNamespace); err != nil {
		warnings = append(warnings, "全局环境变量配置不可用: "+err.Error())
	} else {
		layer, legacy, warns, err := readLayerVars(s, envLegacyPath(""))
		warnings = append(warnings, warns...)
		if err != nil {
			warnings = append(warnings, "全局环境变量读取失败: "+err.Error())
		} else {
			if legacy && len(layer) > 0 {
				warnings = append(warnings, "全局变量来自旧 env.json（下次 set/unset 自动迁移）")
			}
			merge(layer)
		}
	}
	// 工作区级覆盖。
	if ws := strings.TrimSpace(*workspace); ws != "" {
		_ = envstore.MigrateWorkspace(ws)
		if s, err := globalconfig.OpenWorkspace(ws, envNamespace); err != nil {
			warnings = append(warnings, "工作区环境变量配置不可用: "+err.Error())
		} else {
			layer, legacy, warns, err := readLayerVars(s, envLegacyPath(ws))
			warnings = append(warnings, warns...)
			if err != nil {
				warnings = append(warnings, "工作区环境变量读取失败: "+err.Error())
			} else {
				if legacy && len(layer) > 0 {
					warnings = append(warnings, "工作区变量来自旧 env.json（下次 set/unset 自动迁移）")
				}
				merge(layer)
			}
		}
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
	workspace := fs.String("workspace", "", "写入该工作区的 .adago/config/env.json（缺省写机器级）")
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

	s, scope, err := envStore(*workspace)
	if err != nil {
		return err
	}
	vars, legacy, warnings, err := readLayerVars(s, envLegacyPath(*workspace))
	for _, w := range warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	if err != nil {
		return err
	}
	if legacy && len(vars) > 0 {
		fmt.Fprintf(stdout, "已从旧 %s 迁移 %d 个变量\n", envLegacyPath(*workspace), len(vars))
	}
	for key, value := range assignments {
		vars[key] = value
	}
	if err := writeLayerVars(s, vars); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已写入%s环境变量 %s（%s）\n", scope, strings.Join(order, ", "), s.Path())
	return nil
}

func envUnset(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("env unset", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "修改该工作区的 .adago/config/env.json（缺省改机器级）")
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
	s, scope, err := envStore(*workspace)
	if err != nil {
		return err
	}
	vars, _, warnings, err := readLayerVars(s, envLegacyPath(*workspace))
	for _, w := range warnings {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}
	if err != nil {
		return err
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
		if err := writeLayerVars(s, vars); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "已移除%s环境变量 %s（%s）\n", scope, strings.Join(removed, ", "), s.Path())
	}
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "未设置：%s\n", strings.Join(missing, ", "))
	}
	return nil
}
