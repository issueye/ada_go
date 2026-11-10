package frontend

// 全局/工作区安装的第三方插件只贡献 Markdown 动态块（frontend.blocks）：
// 页面/插槽/样式/脚本仍然只装配随包官方插件（SPEC §8 边界）。
//
// 这里从插件目录读取 plugin.json、block 模板与数据 Schema，产出 pluginapi.Script。
// 清单其余前端贡献被剥掉，避免第三方插件借 blocks 之外的面进入宿主 UI。

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pluginapi "github.com/adag/ada-go/internal/common/pluginapi"
)

// ErrNoBlocks 表示插件没有声明任何 frontend.blocks（调用方跳过即可）。
var ErrNoBlocks = errors.New("frontend: 插件没有 frontend.blocks")

// LoadBlockScript 加载一个插件目录的 Markdown 动态块。
//
// 只接受 display/未知 profile 的声明（interactive 在运行期同样 fail-closed）；
// Schema 在加载期编译，非法即拒绝整个脚本，让调用方跳过该插件而不是拖垮 Gateway。
func LoadBlockScript(dir string) (pluginapi.Script, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return pluginapi.Script{}, fmt.Errorf("frontend: 读取 %s/plugin.json 失败: %w", dir, err)
	}
	m, err := pluginapi.LoadManifest(raw)
	if err != nil {
		return pluginapi.Script{}, fmt.Errorf("frontend: 插件 %s 的清单非法: %w", dir, err)
	}
	if err := m.Validate(filepath.Base(dir)); err != nil {
		return pluginapi.Script{}, err
	}
	if m.Frontend == nil || len(m.Frontend.Blocks) == 0 {
		return pluginapi.Script{}, ErrNoBlocks
	}

	bundle := map[string]string{}
	schemas := map[string][]byte{}
	for _, b := range m.Frontend.Blocks {
		template := strings.TrimSpace(b.Template)
		src, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(template)))
		if rerr != nil {
			return pluginapi.Script{}, fmt.Errorf("frontend: 读取 block %s 的模板 %s 失败: %w", b.Name, b.Template, rerr)
		}
		if strings.TrimSpace(string(src)) == "" {
			return pluginapi.Script{}, fmt.Errorf("frontend: block %s 的模板 %s 为空", b.Name, b.Template)
		}
		bundle[template] = string(src)

		schemaPath := strings.TrimSpace(b.Schema)
		schema, serr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(schemaPath)))
		if serr != nil {
			return pluginapi.Script{}, fmt.Errorf("frontend: 读取 block %s 的 Schema %s 失败: %w", b.Name, b.Schema, serr)
		}
		if _, cerr := CompileDataSchema(schema); cerr != nil {
			return pluginapi.Script{}, fmt.Errorf("frontend: block %s 的 Schema 非法: %w", b.Name, cerr)
		}
		schemas[schemaPath] = schema
	}

	// 只保留 blocks：第三方插件的页面/插槽/样式/脚本不进入宿主 UI。
	clone := *m
	fe := *m.Frontend
	fe.Slots, fe.Pages, fe.Nav, fe.Dashboard = nil, nil, nil, nil
	fe.Actions, fe.Styles, fe.Scripts, fe.Assets = nil, nil, nil, ""
	clone.Frontend = &fe

	return pluginapi.Script{
		Manifest: &clone,
		Bundle:   bundle,
		Schemas:  schemas,
		Root:     absPath(dir),
		Readme:   readOptional(filepath.Join(dir, "README.md")),
	}, nil
}

func absPath(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

func readOptional(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || bytes.TrimSpace(data) == nil {
		return ""
	}
	return string(data)
}
