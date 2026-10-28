// Package official 是随包发布的官方插件包（设计 1.5.5、6.3）。
//
// 每个插件是一个目录：plugin.json + 入口源码 + （后端插件）dist/index.js 构建产物，
// go:embed 进二进制。运行时它们与第三方插件走同一条入口：同一份清单校验、
// 同一个 moejs 宿主、同一套贡献注册，没有任何 Go 侧注册 API。
//
// M4 起有两类插件（7.5）：
//   - 后端插件（backend 段）：index.ts → dist/index.js（node official/build.mjs），
//     经 Scripts() 进入 host.Load 装配成 Runtime；
//   - 前端插件（只有 frontend 段）：slot.tsx / hello.tsx 模板与 static/ 资源，
//     经 FrontendScripts() 交给 Gateway 的渲染桥（TSX 由 host.Renderer 在渲染时
//     用 esbuild transform + 内容哈希缓存编译，见 internal/plugins/host/render.go）。
//
// 本包只负责「把嵌入的文件变成 host.Script」；选择启用哪些由产品声明决定，
// 组合根在 app/（本包不认识产品，也不认识产品 id）。
package official

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

//go:embed */plugin.json */dist/index.js
var files embed.FS

//go:embed */skills/*/SKILL.md
var skillFiles embed.FS

//go:embed */*.tsx */static/*
var frontendFiles embed.FS

// Scripts 返回后端插件的加载单元（按 id 排序，保证装配顺序确定）。
//
// 前端插件不在其中：它们没有 backend Runtime 可装配（host.Load 会拒绝），
// 由 FrontendScripts() 交给 Gateway 渲染桥。
func Scripts() ([]host.Script, error) {
	return loadAll(func(m *plugins.Manifest) bool { return m.Backend != nil })
}

// FrontendScripts 返回前端插件（frontend-only）的加载单元（按 id 排序）。
// 清单的 frontend 段已通过与第三方完全相同的校验（LoadManifest + Validate）。
func FrontendScripts() ([]host.Script, error) {
	return loadAll(func(m *plugins.Manifest) bool { return m.Backend == nil })
}

// loadAll 按 manifest 谓词挑选插件目录。
func loadAll(keep func(*plugins.Manifest) bool) ([]host.Script, error) {
	paths, err := fs.Glob(files, "*/plugin.json")
	if err != nil {
		return nil, fmt.Errorf("official: 扫描嵌入插件失败: %w", err)
	}
	sort.Strings(paths)
	out := make([]host.Script, 0, len(paths))
	for _, manifestPath := range paths {
		id := strings.SplitN(manifestPath, "/", 2)[0]
		script, err := load(id)
		if err != nil {
			return nil, err
		}
		if script.Manifest != nil && keep(script.Manifest) {
			out = append(out, script)
		}
	}
	return out, nil
}

// Script 按 id 取一个插件（后端或前端形态）；第二个返回值为 false 表示没有这个插件。
func Script(id string) (host.Script, bool, error) {
	if _, err := fs.Stat(files, id+"/plugin.json"); err != nil {
		return host.Script{}, false, nil
	}
	script, err := load(id)
	if err != nil {
		return host.Script{}, false, err
	}
	return script, true, nil
}

// IDs 返回嵌入的官方插件 id（排序后，含前端插件）。
func IDs() ([]string, error) {
	paths, err := fs.Glob(files, "*/plugin.json")
	if err != nil {
		return nil, fmt.Errorf("official: 扫描嵌入插件失败: %w", err)
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, strings.SplitN(p, "/", 2)[0])
	}
	sort.Strings(out)
	return out, nil
}

// load 读一个插件的清单与内容，并做与第三方完全相同的清单校验。
func load(id string) (host.Script, error) {
	raw, err := files.ReadFile(id + "/plugin.json")
	if err != nil {
		return host.Script{}, fmt.Errorf("official: 读取 %s/plugin.json 失败: %w", id, err)
	}
	m, err := plugins.LoadManifest(raw)
	if err != nil {
		return host.Script{}, fmt.Errorf("official: 官方插件 %s 的清单非法: %w", id, err)
	}
	// 目录名必须等于 id：官方插件也不能靠「特权路径」绕过同一条校验。
	if err := m.Validate(id); err != nil {
		return host.Script{}, fmt.Errorf("official: 官方插件 %s 的清单校验失败: %w", id, err)
	}
	if m.Backend == nil {
		// 前端插件：模板源码进 Bundle（键 = 文件名），静态资源进 StaticAssets。
		return loadFrontend(id, m)
	}
	src, err := files.ReadFile(id + "/dist/index.js")
	if err != nil {
		return host.Script{}, fmt.Errorf("official: 缺少 %s/dist/index.js（运行 node official/build.mjs）: %w", id, err)
	}
	if strings.TrimSpace(string(src)) == "" {
		return host.Script{}, fmt.Errorf("official: %s/dist/index.js 为空", id)
	}
	// 随包技能（6.7）：与第三方插件的 discovery.sideFiles 同一形状——
	// StaticAssets 键 = 相对路径 skills/<name>/SKILL.md，宿主装配期扫描。
	assets, err := bundledSkills(id)
	if err != nil {
		return host.Script{}, err
	}
	return host.Script{
		Manifest:     m,
		EntryName:    host.EntryScheme + "://" + id + "/index.js",
		Source:       string(src),
		StaticAssets: assets,
	}, nil
}

// bundledSkills 收集一个官方插件包内 skills/<name>/SKILL.md 的内容。
func bundledSkills(id string) (map[string][]byte, error) {
	paths, err := fs.Glob(skillFiles, id+"/skills/*/SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("official: 扫描 %s 的随包技能失败: %w", id, err)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	out := map[string][]byte{}
	for _, p := range paths {
		data, err := skillFiles.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("official: 读取 %s 失败: %w", p, err)
		}
		out[strings.TrimPrefix(p, id+"/")] = data
	}
	return out, nil
}

// loadFrontend 收集一个前端插件的 TSX 模板与静态资源（7.5）。
func loadFrontend(id string, m *plugins.Manifest) (host.Script, error) {
	bundle := map[string]string{}
	tmpls, err := fs.Glob(frontendFiles, id+"/*.tsx")
	if err != nil {
		return host.Script{}, fmt.Errorf("official: 扫描 %s 的模板失败: %w", id, err)
	}
	for _, p := range tmpls {
		src, err := frontendFiles.ReadFile(p)
		if err != nil {
			return host.Script{}, fmt.Errorf("official: 读取 %s 失败: %w", p, err)
		}
		bundle[path.Base(p)] = string(src)
	}
	assets := map[string][]byte{}
	if _, statErr := fs.Stat(frontendFiles, id+"/static"); statErr == nil {
		err := fs.WalkDir(frontendFiles, id+"/static", func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				return nil
			}
			data, rerr := frontendFiles.ReadFile(p)
			if rerr != nil {
				return fmt.Errorf("official: 读取 %s 失败: %w", p, rerr)
			}
			assets[strings.TrimPrefix(p, id+"/static/")] = data
			return nil
		})
		if err != nil {
			return host.Script{}, fmt.Errorf("official: 收集 %s 的静态资源失败: %w", id, err)
		}
	}
	script := host.Script{
		Manifest:     m,
		Bundle:       bundle,
		StaticAssets: assets,
	}
	// 清单引用的模板必须真实存在：缺件是打包错误而不是运行期才发现。
	if m.Frontend != nil {
		for _, sc := range m.Frontend.Slots {
			if _, ok := bundle[strings.TrimSpace(sc.Template)]; !ok {
				return host.Script{}, fmt.Errorf("official: 插件 %s 的插槽 %s 引用模板 %q，但包里没有它", id, sc.Slot, sc.Template)
			}
		}
		for _, pc := range m.Frontend.Pages {
			if _, ok := bundle[strings.TrimSpace(pc.Template)]; !ok {
				return host.Script{}, fmt.Errorf("official: 插件 %s 的页面 %s 引用模板 %q，但包里没有它", id, pc.Slug, pc.Template)
			}
		}
	}
	return script, nil
}

// RawManifest 返回某插件的清单原文（诊断与 verify 用）。
func RawManifest(id string) (json.RawMessage, bool) {
	raw, err := files.ReadFile(id + "/plugin.json")
	if err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}
