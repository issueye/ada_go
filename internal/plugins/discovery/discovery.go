// Package discovery 是第三方插件的发现与安装层（M3，设计 6.1、6.9）：
//
//   - 目录布局：工作区 <workspace>/.adago/plugins/<id>/ 与全局 ~/.adago/plugins/<id>/；
//     同 id 冲突时工作区优先并记 warning（MergeByPriority）。
//   - LoadDir/LoadDirDetailed 读取 plugin.json → plugins.LoadManifest → m.Validate(目录名)，
//     并把入口源码（manifest.backend.entry 指向的文件）读成 host.Script。
//     不做 node_modules 解析、不做 TypeScript 编译（05 §6.1、06 §6.10）：入口与随包
//     模块必须是宿主可直接执行的 ESM JavaScript。
//   - 指纹：对插件目录内全部文件按相对路径排序后计算 sha256（FingerprintDir），
//     第三方指纹变化才需要重新装配（6.9）。
//   - InstallZip 从 zip 安装：大小/条目/解压总量上限、拒绝路径穿越、先解压到临时目录
//     校验通过后原子移动到 <workspace>/.adago/plugins/<id>；同名 id 已存在时明确报错
//     （不覆盖）。
//
// 启用/禁用持久化不属于本包：Core 侧由调用方写 plugin_meta（04 §5.7）。
package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

// 来源标记（plugin.list 的 source 字段）。
const (
	// SourceWorkspace 表示插件来自 <workspace>/.adago/plugins。
	SourceWorkspace = "workspace"
	// SourceGlobal 表示插件来自 ~/.adago/plugins。
	SourceGlobal = "global"
)

// Warning 是发现过程中的非致命问题：该插件被跳过，其余插件不受影响（6.5）。
// 重载路径把这些警告当作错误（失败回滚），装配启动路径把它们记入 plugin_meta.last_error。
type Warning struct {
	// PluginID 是插件目录名（清单解析失败前只能用它标识）。
	PluginID string `json:"pluginId,omitempty"`
	// Dir 是插件目录绝对路径。
	Dir string `json:"dir,omitempty"`
	// Message 说明为什么跳过。
	Message string `json:"message"`
}

func (w Warning) String() string {
	id := w.PluginID
	if id == "" {
		id = w.Dir
	}
	if id == "" {
		return w.Message
	}
	return id + ": " + w.Message
}

// Plugin 是发现层产出的一个已加载插件：加载单元、目录指纹与来源。
type Plugin struct {
	// Script 是交给 host.Load 的加载单元。
	Script host.Script
	// Fingerprint 是目录内容指纹（FingerprintDir）。
	Fingerprint string
	// Source 是来源：workspace | global。
	Source string
	// Dir 是插件目录绝对路径。
	Dir string
}

// ID 返回插件 id（清单优先；清单缺失时返回空）。
func (p Plugin) ID() string {
	if p.Script.Manifest != nil {
		return p.Script.Manifest.ID
	}
	return ""
}

// WorkspaceRoot 返回工作区插件目录（<workspace>/.adago/plugins）。
func WorkspaceRoot(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".adago", "plugins")
}

// GlobalRoot 返回全局插件目录（~/.adago/plugins）。
func GlobalRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("discovery: 无法定位用户主目录: %w", err)
	}
	return filepath.Join(home, ".adago", "plugins"), nil
}

// LoadDir 读取一个插件根目录下的全部插件（只返回加载单元）。
// 目录不存在视为空（两个根目录都可能还没创建，不是错误）。
func LoadDir(dir string) ([]host.Script, []Warning, error) {
	loaded, warnings, err := LoadDirDetailed(dir, "")
	if err != nil {
		return nil, warnings, err
	}
	scripts := make([]host.Script, 0, len(loaded))
	for _, p := range loaded {
		scripts = append(scripts, p.Script)
	}
	return scripts, warnings, nil
}

// LoadDirDetailed 与 LoadDir 相同，但额外带出指纹与来源。
// source 为空时只做本地发现（不标记来源）。
//
// 单个插件目录的问题记入 warnings 并跳过（其它插件不受影响，6.5）；
// 只有根目录本身读不了才返回 error。
func LoadDirDetailed(dir, source string) ([]Plugin, []Warning, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("discovery: 读取插件目录 %s 失败: %w", dir, err)
	}
	var (
		out      []Plugin
		warnings []Warning
	)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			// 散落文件与临时目录（安装中间态）不是插件。
			continue
		}
		name := e.Name()
		sub := filepath.Join(dir, name)
		p, warn := loadOne(sub, name, source)
		if warn != nil {
			warnings = append(warnings, *warn)
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, warnings, nil
}

// loadOne 读取单个插件目录；失败返回 warning（不中断其它插件）。
func loadOne(dir, dirName, source string) (Plugin, *Warning) {
	fail := func(format string, args ...any) (Plugin, *Warning) {
		return Plugin{}, &Warning{PluginID: dirName, Dir: dir, Message: fmt.Sprintf(format, args...)}
	}
	manifestPath := filepath.Join(dir, "plugin.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fail("缺少 plugin.json，不是插件目录")
		}
		return fail("读取 plugin.json 失败: %v", err)
	}
	m, err := plugins.LoadManifest(raw)
	if err != nil {
		return fail("%v", err)
	}
	if err := m.Validate(dirName); err != nil {
		return fail("%v", err)
	}
	if m.Backend == nil {
		return fail("纯前端插件没有后端入口，Core 不加载（前端宿主在 Gateway 侧，M4）")
	}
	entryPath := filepath.Join(dir, filepath.FromSlash(m.Backend.Entry))
	src, err := os.ReadFile(entryPath)
	if err != nil {
		return fail("读取入口 %s 失败: %v", m.Backend.Entry, err)
	}
	bundle, assets, err := sideFiles(dir, m.Backend.Entry)
	if err != nil {
		return fail("读取随包文件失败: %v", err)
	}
	fp, err := FingerprintDir(dir)
	if err != nil {
		return fail("计算目录指纹失败: %v", err)
	}
	script := host.Script{
		Manifest:     m,
		EntryName:    host.EntryScheme + "://" + m.ID + "/" + m.Backend.Entry,
		Source:       string(src),
		Bundle:       bundle,
		StaticAssets: assets,
	}
	return Plugin{Script: script, Fingerprint: fp, Source: source, Dir: dir}, nil
}

// sideFiles 收集入口之外的随包模块与静态资源。
//
// 随包模块的键用相对路径（loader 解析相对 import 时会归一到 plugin://<id>/rel，
// 再剥掉前缀回 rel 查表，见 host.moduleLoader.bundleSource）；
// 不解析 node_modules：带 node_modules 的目录直接忽略（05 §6.1）。
func sideFiles(dir, entry string) (map[string]string, map[string][]byte, error) {
	bundle := map[string]string{}
	assets := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == entry || rel == "plugin.json" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".js", ".mjs", ".cjs":
			bundle[rel] = string(content)
			bundle["./"+rel] = string(content)
		default:
			assets[rel] = content
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return bundle, assets, nil
}

// MergeByPriority 合并两个来源的发现结果：primary 优先（工作区覆盖全局），
// 冲突记 warning 不静默覆盖（6.1、6.7 同一原则）。输出按插件 id 排序。
func MergeByPriority(primary, secondary []Plugin) ([]Plugin, []Warning) {
	var warnings []Warning
	seen := map[string]bool{}
	out := make([]Plugin, 0, len(primary)+len(secondary))
	for _, p := range primary {
		id := p.ID()
		if id == "" {
			continue
		}
		seen[id] = true
		out = append(out, p)
	}
	for _, p := range secondary {
		id := p.ID()
		if id == "" || seen[id] {
			if id != "" {
				warnings = append(warnings, Warning{
					PluginID: id, Dir: p.Dir,
					Message: "同 id 插件在多个来源出现，工作区优先（忽略全局/低优先来源的这份）",
				})
			}
			continue
		}
		seen[id] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, warnings
}

// FingerprintDir 对目录内全部文件按相对路径（slash 形式）排序后计算 sha256。
// 同一内容得到同一指纹（与 mtime/平台无关），第三方指纹变化才需要重新装配（6.9）。
func FingerprintDir(dir string) (string, error) {
	var items []fileItem
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		items = append(items, fileItem{rel: filepath.ToSlash(rel), data: data})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("discovery: 扫描 %s 失败: %w", dir, err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })
	h, err := hashItems(items)
	if err != nil {
		return "", err
	}
	return h, nil
}
