package host

// 写插件目录：ctx.fs.writeText 除工作区外，还允许写「已安装插件」目录
// （<home>/.adago/plugins/<id>/…）。这是为了让 agent 能维护工作区外的插件源码
// （block 模板、Schema、入口），由路径判定控制写入范围。
//
// 边界：
//   - 只认已存在的插件目录（目录名 = id、含 plugin.json）；不能在插件根新建插件；
//   - 符号链接必须仍落在该插件目录内（目录本身也不许链到插件根之外）；
//   - 不支持删除/重命名（fs 写面仍只有 writeText）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

// pluginWriteRoots 返回允许写插件文件的两个根：全局安装目录。
// 工作区插件目录（<workspace>/.adago/plugins）本身在工作区内，不需要单独放行。
func pluginWriteRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	return []string{filepath.Join(home, ".adago", "plugins")}
}

// allowPluginWrite 判断 target 是否落在某个已安装插件目录内，返回规范化后的目标路径。
//
// P0 边界（09 §9.1、11 §16）：
//   - 第三方插件只能写自己的插件目录；builtin 系统插件可维护其它插件源码；
//   - 任何插件都不能写 plugin.json（信任/能力声明）与 dist/（可执行产物）；
//   - 符号链接仍必须落在该插件目录内。
func allowPluginWrite(roots []string, target, pluginID string, trust pluginapi.Trust) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if !withinDir(rootAbs, abs) {
			continue
		}
		rel, err := filepath.Rel(rootAbs, abs)
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("不能直接写插件根目录 %s（只能写 <id>/ 下已安装插件的文件）", baseForLog(rootAbs))
		}
		id := parts[0]
		if !pluginapi.ValidID(id) {
			return "", fmt.Errorf("插件目录名 %q 非法", id)
		}
		if id != pluginID && trust != pluginapi.TrustBuiltin {
			return "", fmt.Errorf("第三方插件只能写自己的插件目录 %s，不能写 %s", pluginID, id)
		}
		inside := strings.Join(parts[1:], "/")
		if strings.EqualFold(inside, "plugin.json") {
			return "", fmt.Errorf("插件清单 plugin.json 是信任/能力声明，禁止经 ctx.fs 改写")
		}
		if inside == "dist" || strings.HasPrefix(inside, "dist/") {
			return "", fmt.Errorf("插件可执行产物 dist/ 禁止经 ctx.fs 改写")
		}
		dir := filepath.Join(rootAbs, id)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("插件 %s 未安装（%s 不存在）", id, baseForLog(dir))
		}
		if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
			return "", fmt.Errorf("目录 %s 不是插件（缺 plugin.json）", baseForLog(dir))
		}

		// 规范化插件根与目标：符号链接链出去的一律拒绝。
		canonRoot, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			return "", err
		}
		canonDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return "", err
		}
		if !withinDir(canonRoot, canonDir) {
			return "", fmt.Errorf("插件目录 %s 指向插件根之外", baseForLog(dir))
		}
		ancestor, err := nearestExisting(abs)
		if err != nil {
			return "", err
		}
		canonAncestor, err := filepath.EvalSymlinks(ancestor)
		if err != nil {
			return "", err
		}
		if !withinDir(canonDir, canonAncestor) {
			return "", fmt.Errorf("路径 %s 越出插件目录", baseForLog(target))
		}
		// 目标文件本身是符号链接时，同样要求指向插件目录内部。
		if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			canonTarget, err := filepath.EvalSymlinks(abs)
			if err != nil {
				return "", err
			}
			if !withinDir(canonDir, canonTarget) {
				return "", fmt.Errorf("路径 %s 通过符号链接越出插件目录", baseForLog(target))
			}
		}
		return abs, nil
	}
	return "", errors.New("路径不在工作区，也不在已安装插件目录（~/.adago/plugins/<id>/）内")
}

// nearestExisting 返回 path 上最近的已存在祖先（用于目标文件尚不存在时校验目录）。
func nearestExisting(path string) (string, error) {
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			return cur, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("无法解析路径 %s", path)
		}
		cur = parent
	}
}

func withinDir(dir, target string) bool {
	if target == dir {
		return true
	}
	return strings.HasPrefix(target, dir+string(filepath.Separator))
}
