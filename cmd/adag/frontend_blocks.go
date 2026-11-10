package main

// Gateway 的第三方前端装配：只加载已安装插件的 Markdown 动态块
// （frontend.blocks），页面/插槽/样式/脚本仍只来自随包官方插件。

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/adag/ada-go/internal/agent_core/plugins/discovery"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/gateway/frontend"
)

// loadThirdPartyBlockScripts 扫描工作区与全局插件根，加载第三方 frontend.blocks。
//
// 工作区优先于全局；与随包官方插件同 id 的磁盘版本被忽略（6.9）。
// 单个插件的块非法只跳过该插件（记日志），不影响其它插件与 Gateway 启动。
func loadThirdPartyBlockScripts(workspaces []string, official []host.Script) []host.Script {
	roots := make([]string, 0, len(workspaces)+1)
	for _, ws := range workspaces {
		roots = append(roots, discovery.WorkspaceRoot(ws))
	}
	if root, err := discovery.GlobalRoot(); err == nil {
		roots = append(roots, root)
	}
	return loadBlockScriptsFromRoots(roots, official)
}

// loadBlockScriptsFromRoots 按 roots 顺序加载，先到先得（工作区优先）。
//
// 这里直接读插件目录、不经过 discovery.LoadDirDetailed：后者要求插件有
// backend（Core 只加载有入口的包），而 block-only 的纯前端包正是本功能要支持的。
// 清单校验与模板/Schema 读取由 frontend.LoadBlockScript 负责。
func loadBlockScriptsFromRoots(roots []string, official []host.Script) []host.Script {
	reserved := map[string]bool{}
	for _, s := range official {
		if s.Manifest != nil {
			reserved[s.Manifest.ID] = true
		}
	}
	seen := map[string]bool{}
	var out []host.Script
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("gateway: 扫描插件目录 %s 失败: %v", root, err)
			}
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			id := e.Name()
			if reserved[id] {
				continue
			}
			if seen[id] {
				log.Printf("gateway: 插件 %s 在 %s 被更高优先级来源覆盖，忽略", id, root)
				continue
			}
			dir := filepath.Join(root, id)
			if _, statErr := os.Stat(filepath.Join(dir, "plugin.json")); statErr != nil {
				continue // 插件根里可能有非插件目录，静默跳过
			}
			sc, err := frontend.LoadBlockScript(dir)
			if errors.Is(err, frontend.ErrNoBlocks) {
				continue
			}
			if err != nil {
				log.Printf("gateway: 跳过 %s 的动态块: %v", id, err)
				continue
			}
			seen[id] = true
			out = append(out, sc)
		}
	}
	return out
}
