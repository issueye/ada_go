// Package binpaths 管理「服务程序」目录：工作区 <workspace>/.adago/bin 与用户级
// <ADAGO_HOME|~/.adago>/bin。这两个目录里的可执行文件会被前置到插件子进程的
// PATH（工作区优先），因此 run_command、后台任务与 stdio MCP 可以直接按名字调用，
// 不需要写绝对路径。
//
// 目录不存在是常态：PATH 里保留它们没有副作用，用户把程序放进去后新启动的子进程
// 就能找到。
package binpaths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/adag/ada-go/internal/common/envstore"
)

// Dir 是一个服务程序目录。
type Dir struct {
	Scope string // "workspace" | "global"
	Path  string
}

// Dirs 返回服务程序目录，顺序即 PATH 前缀顺序（工作区优先，其次用户级）。
// 工作区不可用时只返回用户级（反之亦然）；两者都不可用才报错。
func Dirs(workspace string) ([]Dir, error) {
	var dirs []Dir
	if ws := strings.TrimSpace(workspace); ws != "" {
		abs, err := filepath.Abs(ws)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, Dir{Scope: "workspace", Path: filepath.Join(abs, ".adago", "bin")})
	}
	root, err := envstore.GlobalDir()
	if err == nil {
		dirs = append(dirs, Dir{Scope: "global", Path: filepath.Join(root, "bin")})
	}
	if len(dirs) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("binpaths: 没有可用的服务程序目录")
	}
	return dirs, nil
}

// PrependPath 把 dirs 放到 PATH 前面并去重；pathValue 为空时结果就是 dirs。
// 输出保持 dirs 的顺序（工作区在前），已存在的同目录（Windows 大小写不敏感）跳过。
func PrependPath(pathValue string, dirs []Dir) string {
	sep := string(os.PathListSeparator)
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		p := filepath.Clean(d.Path)
		if p == "" || p == "." {
			continue
		}
		key := pathKey(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	for _, p := range strings.Split(pathValue, sep) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key := pathKey(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return strings.Join(out, sep)
}

func pathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}
