// Package binpaths 管理「服务程序」目录：工作区 <workspace>/.adago/bin 与用户级
// <ADAGO_HOME|~/.adago>/bin。这两个目录里的可执行文件会被前置到插件子进程的
// PATH（工作区优先），因此 run_command、后台任务与 stdio MCP 可以直接按名字调用，
// 不需要写绝对路径。
//
// 目录不存在是常态：PATH 里保留它们没有副作用，用户把程序放进去后新启动的子进程
// 就能找到；环境变量管理页展示实际存在的程序。
package binpaths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/envstore"
)

// Dir 是一个服务程序目录。
type Dir struct {
	Scope string // "workspace" | "global"
	Path  string
}

// Program 是扫描到的一个可执行文件。
type Program struct {
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`
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

// Path 返回 dirs 的路径列表（与 PATH 顺序一致）。
func Path(dirs []Dir) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if p := filepath.Clean(d.Path); p != "" && p != "." {
			out = append(out, p)
		}
	}
	return out
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

// IsExecutable 判断一个文件是否算「服务程序」：Unix 看可执行位，
// Windows 看常见可执行扩展名（.exe/.cmd/.bat/.com/.ps1）。
func IsExecutable(name string, mode fs.FileMode) bool {
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".exe", ".cmd", ".bat", ".com", ".ps1":
			return true
		}
		return false
	}
	return mode.Perm()&0o111 != 0
}

// List 扫描 dirs 里的可执行文件；目录不存在不算错误，读不了记警告。
// max <= 0 时每批最多返回 200 个。
func List(dirs []Dir, max int) ([]Program, []string) {
	if max <= 0 {
		max = 200
	}
	programs := make([]Program, 0, 16)
	var warnings []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir.Path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, dir.Path+": "+err.Error())
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if !IsExecutable(entry.Name(), info.Mode()) {
				continue
			}
			programs = append(programs, Program{
				Scope:    dir.Scope,
				Name:     entry.Name(),
				Path:     filepath.Join(dir.Path, entry.Name()),
				Size:     info.Size(),
				Modified: info.ModTime().UTC().Format(time.RFC3339),
			})
			if len(programs) >= max {
				warnings = append(warnings, "服务程序数量超过上限，只展示前面的 "+strconv.Itoa(max)+" 个")
				return programs, warnings
			}
		}
	}
	rank := map[string]int{"workspace": 0, "global": 1}
	sort.SliceStable(programs, func(i, j int) bool {
		if programs[i].Scope != programs[j].Scope {
			return rank[programs[i].Scope] < rank[programs[j].Scope]
		}
		return programs[i].Name < programs[j].Name
	})
	return programs, warnings
}
