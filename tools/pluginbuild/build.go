package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const maxBundleBytes = 512 << 10

// Options 是 pluginbuild 的输入；Dir 可以是相对路径。
type Options struct {
	Dir   string
	Entry string
	Out   string
	ID    string
	Check bool
}

var cspDanger = regexp.MustCompile(`(?m)(\beval\s*\(|new\s+Function\s*\()`)

// Run 构建并写入（或 --check 校验）一个插件的浏览器 bundle。
func Run(opts Options) error {
	code, outPath, err := Build(opts)
	if err != nil {
		return err
	}
	if opts.Check {
		existing, rerr := os.ReadFile(outPath)
		if rerr != nil {
			return fmt.Errorf("缺少产物 %s（先运行 go run ./tools/pluginbuild -dir %s）: %w", outPath, opts.Dir, rerr)
		}
		if string(existing) != string(code) {
			return fmt.Errorf("产物 %s 与源码不同步（运行 go run ./tools/pluginbuild -dir %s）", outPath, opts.Dir)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("创建产物目录失败: %w", err)
	}
	if err := os.WriteFile(outPath, code, 0o644); err != nil {
		return fmt.Errorf("写入产物失败: %w", err)
	}
	fmt.Printf("pluginbuild: %s → %s（%d 字节）\n", filepath.Join(opts.Dir, opts.Entry), outPath, len(code))
	return nil
}

// Build 只构建不写盘；返回带 banner 的 ESM 产物与绝对输出路径。
func Build(opts Options) ([]byte, string, error) {
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, "", err
	}
	entry := opts.Entry
	if entry == "" {
		entry = "client.tsx"
	}
	out := opts.Out
	if out == "" {
		out = filepath.Join("static", "client.js")
	}
	srcPath := filepath.Join(dir, filepath.FromSlash(entry))
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, "", fmt.Errorf("读取入口 %s 失败: %w", srcPath, err)
	}

	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = manifestID(dir)
	}
	if id == "" {
		id = filepath.Base(dir)
	}

	result := api.Build(api.BuildOptions{
		AbsWorkingDir: dir,
		EntryPoints:   []string{entry},
		Bundle:        true,
		Write:         false,
		Format:        api.FormatESModule,
		Platform:      api.PlatformBrowser,
		Target:        api.ES2020,
		JSX:           api.JSXTransform,
		JSXFactory:    "h",
		JSXFragment:   "Fragment",
		Charset:       api.CharsetUTF8,
		LegalComments: api.LegalCommentsNone,
		Sourcemap:     api.SourceMapNone,
		LogLevel:      api.LogLevelSilent,
		Plugins:       []api.Plugin{adaGlobalsPlugin()},
	})
	if len(result.Errors) > 0 {
		first := result.Errors[0]
		if first.Location != nil {
			return nil, "", fmt.Errorf("esbuild: %s（%s:%d）", first.Text, first.Location.File, first.Location.Line)
		}
		return nil, "", fmt.Errorf("esbuild: %s", first.Text)
	}
	if len(result.OutputFiles) != 1 {
		return nil, "", fmt.Errorf("esbuild 产物数量 = %d，期望 1", len(result.OutputFiles))
	}

	sum := sha256.Sum256(src)
	banner := fmt.Sprintf("// generated from %s (sha256:%s) by tools/pluginbuild — do not edit\n",
		filepath.ToSlash(entry), hex.EncodeToString(sum[:])[:16])
	code := append([]byte(banner), result.OutputFiles[0].Contents...)
	if len(code) > maxBundleBytes {
		return nil, "", fmt.Errorf("产物 %d 字节，超过 %d 上限", len(code), maxBundleBytes)
	}
	if loc := cspDanger.FindIndex(code); loc != nil {
		line := 1 + strings.Count(string(code[:loc[0]]), "\n")
		return nil, "", fmt.Errorf("产物包含 CSP 禁止的 eval/new Function（第 %d 行附近）", line)
	}
	// 宿主运行时必须是 shim，不允许把 bare specifier 留给浏览器（ESM 无 import map）。
	if strings.Contains(string(code), `"ada:render"`) || strings.Contains(string(code), `"ada:ui"`) {
		return nil, "", errors.New("产物仍包含 ada:render/ada:ui bare import，shim 未生效")
	}

	fmt.Printf("pluginbuild: %s (%s)\n", id, filepath.ToSlash(entry))
	return code, filepath.Join(dir, filepath.FromSlash(out)), nil
}

func manifestID(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return ""
	}
	var m struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.ID)
}

func adaGlobalsPlugin() api.Plugin {
	return api.Plugin{
		Name: "ada-globals",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `^ada:(render|ui)$`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: args.Path, Namespace: "ada-global"}, nil
			})
			build.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "ada-global"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				src, ok := adaShim(args.Path)
				if !ok {
					return api.OnLoadResult{}, fmt.Errorf("未知宿主模块 %q", args.Path)
				}
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			})
		},
	}
}

func adaShim(path string) (string, bool) {
	switch path {
	case "ada:render":
		return `const R = globalThis.AdaRender;
if (!R) throw new Error("[pluginbuild] 请先加载 ada-render.js");
export const h = R.h;
export const host = R.host;
export const text = R.text;
export const defineComponent = R.defineComponent;
export const createApp = R.createApp;
export const reactive = R.reactive;
export const ref = R.ref;
export const computed = R.computed;
export const watchEffect = R.watchEffect;
export const nextTick = R.nextTick;
export const renderToJSON = R.renderToJSON;
`, true
	case "ada:ui":
		names := []string{
			"Button", "IconButton", "Input", "TextField", "Textarea", "Select",
			"Dropdown", "DropdownButton", "Tabs", "Icon", "Markdown", "Card",
			"Badge", "Slider", "Range", "Progress", "ProgressBar", "Table",
			"DataTable", "Modal", "Message", "Alert", "Notice", "Empty",
			"Spinner", "Switch", "Checkbox", "Radio",
		}
		var b strings.Builder
		b.WriteString("const ISO = globalThis.AdaUIIso;\nconst UI = ISO && ISO.components ? ISO : globalThis.AdaUI;\nif (!UI) throw new Error(\"[pluginbuild] 请先加载 ada-ui-iso.js 或 ada-ui.js\");\nconst C = UI.components || {};\n")
		for _, name := range names {
			fmt.Fprintf(&b, "export const %s = C[%q];\n", name, name)
		}
		return b.String(), true
	default:
		return "", false
	}
}
