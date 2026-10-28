package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MoeForge/moejs/node"
)

// tree 是探针用的工作区夹具。
type tree struct {
	Root     string // 宿主绝对路径，node:fs 的 Root
	Outside  string // Root 之外的宿主目录（越界取样点）
	HostFile string // Outside 里的一个文件
}

// makeTree 建出一棵最小工作区：hello.txt / dir/inner.txt；bigBytes>0 时再加
// big.bin（20MB 级）、many/（10500 个条目，测目录上限）与越界符号链接。
func makeTree(tag string, bigBytes int) (*tree, error) {
	base, err := os.MkdirTemp("", "moejspike-"+tag+"-")
	if err != nil {
		return nil, err
	}
	root := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(root, "dir")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			panic(fmt.Sprintf("spike: 写夹具失败 %s: %v", p, err))
		}
	}
	write(filepath.Join(root, "hello.txt"), "hello moejs")
	write(filepath.Join(root, "dir", "inner.txt"), "inner")
	write(filepath.Join(outside, "secret.txt"), "outside secret")
	write(filepath.Join(root, ".env"), "SECRET=hunter2")
	if bigBytes > 0 {
		f, err := os.Create(filepath.Join(root, "big.bin"))
		if err != nil {
			return nil, err
		}
		chunk := strings.Repeat("A", 1<<20)
		for i := 0; i < bigBytes/(1<<20); i++ {
			if _, err := f.WriteString(chunk); err != nil {
				f.Close()
				return nil, err
			}
		}
		f.Close()
		many := filepath.Join(root, "many")
		if err := os.MkdirAll(many, 0o755); err != nil {
			return nil, err
		}
		for i := 0; i < 10500; i++ {
			if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("f%05d.txt", i)), []byte("x"), 0o644); err != nil {
				return nil, err
			}
		}
		// 越界与根内链接（best effort：Windows 无权限时跳过，探针自己标注未验证）
		_ = os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "out.link"))
		_ = os.Symlink(filepath.Join(root, "hello.txt"), filepath.Join(root, "inner.link"))
	}
	return &tree{Root: root, Outside: outside, HostFile: filepath.Join(outside, "secret.txt")}, nil
}

// nodeFS 返回只读（write=false）或可写的 node 安装配置；Root 用真实宿主目录，
// Mount 用设计口径的虚拟根。
func nodeFS(t *tree, mount string, write bool) *node.Options {
	return &node.Options{
		FS: &node.FSOptions{
			Root:  t.Root,
			Mount: mount,
			Write: write,
		},
		Buffer: true,
	}
}

// withAllow 返回带 Allow 钩子的安装配置。
func withAllow(o *node.Options, allow func(op, path string) error) *node.Options {
	o.FS.Allow = allow
	return o
}

// hostOSCommand 是跨平台的“可执行文件 + 参数”取样点（无 shell）。
func hostOSCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", "echo", "spike"}
	}
	return "/bin/echo", []string{"spike"}
}

// canSymlink 报告当前平台/权限能否创建符号链接（Windows 非开发者模式会失败）。
func canSymlink(dir string) bool {
	target := filepath.Join(dir, "lk-probe-target.txt")
	link := filepath.Join(dir, "lk-probe.txt")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		return false
	}
	err := os.Symlink(target, link)
	if err == nil {
		_ = os.Remove(link)
	}
	_ = os.Remove(target)
	return err == nil
}
