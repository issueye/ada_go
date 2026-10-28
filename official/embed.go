// Package official 是随包发布的官方插件包（设计 1.5.5、6.3）。
//
// 每个插件是一个目录：plugin.json + index.ts（源码）+ dist/index.js（构建产物，
// go:embed 进二进制）。运行时它们与第三方插件走同一条入口：同一份清单校验、
// 同一个 moejs 宿主、同一套贡献注册，没有任何 Go 侧注册 API。
//
// 本包只负责「把嵌入的文件变成 host.Script」；选择启用哪些由产品声明决定，
// 组合根在 app/（本包不认识产品，也不认识产品 id）。
package official

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

//go:embed */plugin.json */dist/index.js
var files embed.FS

// Scripts 返回全部官方插件的加载单元（按 id 排序，保证装配顺序确定）。
func Scripts() ([]host.Script, error) {
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
		out = append(out, script)
	}
	return out, nil
}

// Script 按 id 取一个官方插件；第二个返回值为 false 表示没有这个插件。
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

// IDs 返回嵌入的官方插件 id（排序后）。
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

// load 读一个插件的清单与产物，并做与第三方完全相同的清单校验。
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
	src, err := files.ReadFile(id + "/dist/index.js")
	if err != nil {
		return host.Script{}, fmt.Errorf("official: 缺少 %s/dist/index.js（运行 node official/build.mjs）: %w", id, err)
	}
	if strings.TrimSpace(string(src)) == "" {
		return host.Script{}, fmt.Errorf("official: %s/dist/index.js 为空", id)
	}
	return host.Script{
		Manifest:  m,
		EntryName: host.EntryScheme + "://" + id + "/index.js",
		Source:    string(src),
	}, nil
}

// RawManifest 返回某插件的清单原文（诊断与 verify 用）。
func RawManifest(id string) (json.RawMessage, bool) {
	raw, err := files.ReadFile(id + "/plugin.json")
	if err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}
