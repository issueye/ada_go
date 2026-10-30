package official

// 官方插件只从磁盘加载（桌面打包把 plugins/official 放进 Electron resources）。
//
// 查找顺序：
//   1. ADAG_OFFICIAL_PLUGINS_DIR（显式指定；无效即报错）
//   2. <exe>/../plugins/official（resources/bin → resources/plugins/official）
//   3. <exe>/../../plugins/official（desktop/bin、bin/release → 仓库根）
//   4. <exe>/plugins/official
//   5. 从 cwd 向上找 plugins/official（源码检出/测试）
//
// 找不到目录直接报错；二进制不再内嵌插件副本。

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

// Scripts 加载后端官方插件（目录由 pluginsRoot 解析）。
func Scripts() ([]host.Script, error) {
	root, err := pluginsRoot()
	if err != nil {
		return nil, err
	}
	return ScriptsFromDir(root)
}

// FrontendScripts 加载前端官方插件（含统一插件包）。
func FrontendScripts() ([]host.Script, error) {
	root, err := pluginsRoot()
	if err != nil {
		return nil, err
	}
	return FrontendScriptsFromDir(root)
}

// ScriptsFromDir 从磁盘根目录加载后端官方插件（目录名 = 插件 id）。
func ScriptsFromDir(root string) ([]host.Script, error) {
	return loadAllFromDir(root, false)
}

// FrontendScriptsFromDir 从磁盘根目录加载前端官方插件（含统一插件包）。
func FrontendScriptsFromDir(root string) ([]host.Script, error) {
	return loadAllFromDir(root, true)
}

// Root 返回解析到的官方插件目录（与 Scripts/FrontendScripts 共用同一份解析）。
func Root() (string, error) {
	return pluginsRoot()
}

// pluginsRoot 解析官方插件目录；不存在时返回可操作的错误。
func pluginsRoot() (string, error) {
	if v := strings.TrimSpace(os.Getenv("ADAG_OFFICIAL_PLUGINS_DIR")); v != "" {
		if !isOfficialRoot(v) {
			return "", fmt.Errorf("official: ADAG_OFFICIAL_PLUGINS_DIR=%q 不是有效的官方插件目录（需要包含 <id>/plugin.json）", v)
		}
		return absDir(v), nil
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "..", "plugins", "official"),
			filepath.Join(exeDir, "..", "..", "plugins", "official"),
			filepath.Join(exeDir, "plugins", "official"),
		)
	}
	if src := sourceOfficialRoot(); src != "" {
		candidates = append(candidates, src)
	}
	for _, root := range candidates {
		if isOfficialRoot(root) {
			return absDir(root), nil
		}
	}
	return "", fmt.Errorf("official: 找不到官方插件目录（需要 <exe>/../plugins/official 或 <exe>/../../plugins/official，或设置 ADAG_OFFICIAL_PLUGINS_DIR）")
}

// sourceOfficialRoot 只服务源码检出（go test / go run）：按编译期文件路径
// 找到仓库里的 plugins/official。-trimpath 发布构建返回模块路径，不会命中。
func sourceOfficialRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(file)
	if filepath.Base(dir) == "official" && filepath.Base(filepath.Dir(dir)) == "plugins" {
		return dir
	}
	return ""
}

func isOfficialRoot(root string) bool {
	if root == "" {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "plugin.json")); err == nil {
			return true
		}
	}
	return false
}

func absDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

func loadAllFromDir(root string, frontend bool) ([]host.Script, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("official: 读取插件目录 %s 失败: %w", root, err)
	}
	out := make([]host.Script, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		id := e.Name()
		dir := filepath.Join(root, id)
		raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil {
			return nil, fmt.Errorf("official: 读取 %s/plugin.json 失败: %w", id, err)
		}
		m, err := plugins.LoadManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("official: 官方插件 %s 的清单非法: %w", id, err)
		}
		if err := m.Validate(id); err != nil {
			return nil, fmt.Errorf("official: 官方插件 %s 的清单校验失败: %w", id, err)
		}
		if frontend {
			if m.Frontend == nil {
				continue
			}
			sc, err := loadFrontendDir(dir, m)
			if err != nil {
				return nil, err
			}
			out = append(out, sc)
			continue
		}
		if m.Backend == nil {
			continue // 纯前端插件没有后端入口
		}
		sc, err := loadBackendDir(dir, m)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out, nil
}

func loadBackendDir(dir string, m *plugins.Manifest) (host.Script, error) {
	src, err := os.ReadFile(filepath.Join(dir, "dist", "index.js"))
	if err != nil {
		return host.Script{}, fmt.Errorf("official: 缺少 %s/dist/index.js（先运行 node plugins/official/build.mjs）: %w", dir, err)
	}
	if strings.TrimSpace(string(src)) == "" {
		return host.Script{}, fmt.Errorf("official: %s/dist/index.js 为空", dir)
	}
	assets, err := dirSkills(dir)
	if err != nil {
		return host.Script{}, err
	}
	return host.Script{
		Manifest:     m,
		EntryName:    host.EntryScheme + "://" + m.ID + "/index.js",
		Source:       string(src),
		StaticAssets: assets,
		Readme:       readOptionalText(filepath.Join(dir, "README.md")),
	}, nil
}

func loadFrontendDir(dir string, m *plugins.Manifest) (host.Script, error) {
	bundle := map[string]string{}
	tmpls, err := filepath.Glob(filepath.Join(dir, "*.tsx"))
	if err != nil {
		return host.Script{}, fmt.Errorf("official: 扫描 %s 的模板失败: %w", dir, err)
	}
	for _, p := range tmpls {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return host.Script{}, fmt.Errorf("official: 读取 %s 失败: %w", p, rerr)
		}
		bundle[filepath.Base(p)] = string(data)
	}
	assets := map[string][]byte{}
	staticDir := filepath.Join(dir, "static")
	if st, statErr := os.Stat(staticDir); statErr == nil && st.IsDir() {
		err := filepath.WalkDir(staticDir, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return fmt.Errorf("official: 读取 %s 失败: %w", p, rerr)
			}
			rel, relErr := filepath.Rel(staticDir, p)
			if relErr != nil {
				return relErr
			}
			assets[filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			return host.Script{}, fmt.Errorf("official: 收集 %s 的静态资源失败: %w", dir, err)
		}
	}
	script := host.Script{
		Manifest:     m,
		Bundle:       bundle,
		StaticAssets: assets,
		Readme:       readOptionalText(filepath.Join(dir, "README.md")),
	}
	// 清单引用的模板必须真实存在：缺件是打包错误而不是运行期才发现。
	for _, sc := range m.Frontend.Slots {
		if _, ok := bundle[strings.TrimSpace(sc.Template)]; !ok {
			return host.Script{}, fmt.Errorf("official: 插件 %s 的插槽 %s 引用模板 %q，但包里没有它", m.ID, sc.Slot, sc.Template)
		}
	}
	for _, pc := range m.Frontend.Pages {
		if _, ok := bundle[strings.TrimSpace(pc.Template)]; !ok {
			return host.Script{}, fmt.Errorf("official: 插件 %s 的页面 %s 引用模板 %q，但包里没有它", m.ID, pc.Slug, pc.Template)
		}
	}
	return script, nil
}

func dirSkills(dir string) (map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "skills", "*", "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("official: 扫描 %s 的随包技能失败: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	out := map[string][]byte{}
	for _, p := range paths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, fmt.Errorf("official: 读取 %s 失败: %w", p, rerr)
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return nil, relErr
		}
		out[filepath.ToSlash(rel)] = data
	}
	return out, nil
}

func readOptionalText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
