package cli

// 托管环境变量的读取：环境变量管理插件与 adag env CLI 都写 global.config 的
// env 命名空间；基座只负责把两层 vars 合并后注入子进程。
// 旧 envstore 文件按层回退：机器级 globalconfig 缺席时读旧 ~/.adago/env.json，
// 工作区级缺席时读旧 <workspace>/.adago/env.json——两层互不影响，避免
// 「一层迁移后另一层旧变量消失」的断崖。

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/adag/ada-go/internal/common/envstore"
	"github.com/adag/ada-go/internal/common/globalconfig"
)

// loadManagedEnv 读取机器级 + 工作区级 env 命名空间，工作区覆盖全局。
// 每层 globalconfig 缺席时各自回退旧 envstore 文件。
func loadManagedEnv(workspace, providersPath string) (map[string]string, []string) {
	out := map[string]string{}
	var warns []string
	merge := func(vars map[string]string) {
		for k, v := range vars {
			out[k] = v
		}
	}

	// 机器级：$ADAGO_HOME/config/env.json（显式 --data-dir 时在数据目录旁），
	// 缺席时回退同目录旁的旧 env.json。
	machineDir := filepath.Join(filepath.Dir(providersPath), "config")
	machineLegacy := filepath.Join(filepath.Dir(providersPath), "env.json")
	if s, err := globalconfig.OpenAt(machineDir, "env"); err != nil {
		warns = append(warns, "全局环境变量配置不可用: "+err.Error())
	} else if vars, ok, err := readEnvVars(s); err != nil {
		warns = append(warns, "全局环境变量配置读取失败: "+err.Error())
	} else if ok {
		merge(vars)
	} else if vars, legacyWarns, err := envstore.LoadFile(machineLegacy); err != nil {
		warns = append(warns, "旧全局 env.json 读取失败: "+err.Error())
	} else {
		warns = append(warns, legacyWarns...)
		merge(vars)
	}

	// 工作区级：<workspace>/.adago/config/env.json，缺席时回退旧
	// <workspace>/.adago/env.json。
	if s, err := globalconfig.OpenWorkspace(workspace, "env"); err != nil {
		warns = append(warns, "工作区环境变量配置不可用: "+err.Error())
	} else if vars, ok, err := readEnvVars(s); err != nil {
		warns = append(warns, "工作区环境变量配置读取失败: "+err.Error())
	} else if ok {
		merge(vars)
	} else if wsPath, wsErr := envstore.WorkspacePath(workspace); wsErr != nil {
		warns = append(warns, "工作区环境变量路径解析失败: "+wsErr.Error())
	} else if vars, legacyWarns, err := envstore.LoadFile(wsPath); err != nil {
		warns = append(warns, "旧工作区 env.json 读取失败: "+err.Error())
	} else {
		warns = append(warns, legacyWarns...)
		merge(vars)
	}
	return out, warns
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
