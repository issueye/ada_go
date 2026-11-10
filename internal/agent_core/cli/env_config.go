package cli

// 托管环境变量的读取：环境变量管理插件写入 global.config 的 env 命名空间；
// 基座只负责把两层 vars 合并后注入子进程。旧 envstore 文件在配置缺失时兜底读取。

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/adag/ada-go/internal/common/envstore"
	"github.com/adag/ada-go/internal/common/globalconfig"
)

// loadManagedEnv 读取机器级 + 工作区级 env 命名空间，工作区覆盖全局。
// 两层配置都不存在时回退旧 envstore（env.json / .adago/env.json）。
func loadManagedEnv(workspace, providersPath string) (map[string]string, []string) {
	out := map[string]string{}
	var warns []string
	found := false
	machineDir := filepath.Join(filepath.Dir(providersPath), "config")
	if s, err := globalconfig.OpenAt(machineDir, "env"); err != nil {
		warns = append(warns, "全局环境变量配置不可用: "+err.Error())
	} else if vars, ok, err := readEnvVars(s); err != nil {
		warns = append(warns, "全局环境变量配置读取失败: "+err.Error())
	} else if ok {
		found = true
		for k, v := range vars {
			out[k] = v
		}
	}
	if s, err := globalconfig.OpenWorkspace(workspace, "env"); err != nil {
		warns = append(warns, "工作区环境变量配置不可用: "+err.Error())
	} else if vars, ok, err := readEnvVars(s); err != nil {
		warns = append(warns, "工作区环境变量配置读取失败: "+err.Error())
	} else if ok {
		found = true
		for k, v := range vars {
			out[k] = v
		}
	}
	if found {
		return out, warns
	}
	vars, legacyWarns := envstore.LoadForWorkspace(workspace)
	warns = append(warns, legacyWarns...)
	return vars, warns
}

func readEnvVars(s *globalconfig.Store) (map[string]string, bool, error) {
	raw, found, err := s.Get("vars")
	if err != nil || !found {
		return nil, false, err
	}
	var vars map[string]string
	if err := json.Unmarshal(raw, &vars); err != nil {
		return nil, false, fmt.Errorf("vars 不是字符串键值对象: %w", err)
	}
	if vars == nil {
		vars = map[string]string{}
	}
	return vars, true, nil
}
