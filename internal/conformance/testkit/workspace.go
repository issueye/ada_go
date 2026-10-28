package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ScopeSample 是一条 scope 边界样本：虚拟路径 + 期望结论。
//
// 守的不变量/规则：09 §9.2（先字符串检查、再按真实落点判定；工作区内指向外面的
// symlink/junction 按真实落点拒绝）、INV-4（拿不准就往拒绝倒）。
type ScopeSample struct {
	// Virtual 是插件侧看到的受限虚拟路径（工作区挂在 /）。
	Virtual string
	// Real 是磁盘上的真实落点（可能为空：纯字符串越界样本没有落点）。
	Real string
	// WantWithin 报告期望的解析结论：true=应解析到工作区内，false=应被拒。
	WantWithin bool
	// WantDenied 报告期望 Scope.Resolve 是否返回错误。
	WantDenied bool
	// Note 说明这条样本守的是哪条规则。
	Note string
	// Symlink 标记该样本依赖符号链接（平台不支持时为 nil 处理）。
	Symlink bool
}

// Workspace 是 TempWorkspace 的返回值。
type Workspace struct {
	// Root 是工作区根（真实落点）。
	Root string
	// Outside 是工作区之外的样本目录（含 secret.txt）。
	Outside string
	// EscapeLink 是指向 Outside/secret.txt 的工作区内符号链接；
	// SymlinkOK 为 false 时为空。
	EscapeLink string
	// SymlinkOK 报告平台是否真的建成了符号链接。
	SymlinkOK bool
	// SymlinkErr 是建链失败原因（Windows 无特权/未开开发者模式时非空）。
	SymlinkErr error
	// Files 是工作区内的常规样本文件（相对 Root）。
	Files []string
}

// DataDir 返回工作区的数据目录（<root>/.adago，04 §5.7 的 state.db/locks 落点）。
func (w *Workspace) DataDir() string { return filepath.Join(w.Root, ".adago") }

// Rel 把绝对路径换算成虚拟路径（/ 前缀、正斜杠），供 Scope 断言使用。
func (w *Workspace) Rel(abs string) string {
	rp, err := filepath.Rel(w.Root, abs)
	if err != nil {
		return abs
	}
	return "/" + filepath.ToSlash(rp)
}

// Samples 返回可迭代的 scope 边界样本集（M1 的 scope 测试直接 range 它）。
func (w *Workspace) Samples() []ScopeSample {
	out := []ScopeSample{
		{Virtual: "/src/main.go", Real: filepath.Join(w.Root, "src", "main.go"),
			WantWithin: true, WantDenied: false, Note: "工作区内常规文件：可读"},
		{Virtual: "/../outside/secret.txt", WantWithin: false, WantDenied: true,
			Note: "字符串层越界（..）：先拒再解析（09 §9.2）"},
		{Virtual: "/../../etc/passwd", WantWithin: false, WantDenied: true,
			Note: "多层 .. 逃逸：字符串层拒绝"},
		{Virtual: w.Rel(filepath.Join(w.Outside, "secret.txt")), WantWithin: false, WantDenied: true,
			Note: "绝对路径落在工作区外：按真实落点拒绝"},
	}
	if w.EscapeLink != "" {
		out = append(out, ScopeSample{
			Virtual: w.Rel(w.EscapeLink), Real: filepath.Join(w.Outside, "secret.txt"),
			WantWithin: false, WantDenied: true, Symlink: true,
			Note: "工作区内 symlink 指向工作区外：按真实落点拒绝（09 §9.2）",
		})
	}
	if w.DataDir() != "" {
		out = append(out, ScopeSample{
			Virtual: "/.adago/locks", Real: filepath.Join(w.DataDir(), "locks"),
			WantWithin: true, WantDenied: false,
			Note: "锁目录在工作区内但永远只读（ports/scope.go：ReadOnly）",
		})
	}
	return out
}

// TempWorkspace 创建一个测试独占的工作区，附带 scope 边界样本。
//
// 清理注册在 t.Cleanup 上（10 §11.2：测试必须可并行，所以每个测试一份独占目录）。
// Windows 上建符号链接需要特权或开发者模式：失败不判错，只在 SymlinkOK/SymlinkErr 里
// 报告，让依赖它的样本自行跳过 —— 这是「平台差异显式化」，不是放水。
func TempWorkspace(t TB) *Workspace {
	t.Helper()
	root := t.TempDir()
	ws := &Workspace{Root: root}

	write := func(path, content string) {
		t.Helper()
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("testkit: 建目录 %s: %v", dir, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("testkit: 写文件 %s: %v", path, err)
		}
		ws.Files = append(ws.Files, filepath.Base(path))
	}

	// 工作区内样本。
	write(filepath.Join(root, "src", "main.go"), "package main\n")
	write(filepath.Join(root, "README.md"), "# ada-go 测试工作区\n")
	write(filepath.Join(root, ".adago", "config.toml"), "# 空配置\n")
	if err := os.MkdirAll(filepath.Join(root, ".adago", "locks"), 0o755); err != nil {
		t.Fatalf("testkit: 建锁目录: %v", err)
	}

	// 工作区外的样本（同一临时父目录下，避免跨盘）。
	outside := filepath.Join(root, "..", "outside-"+filepath.Base(root))
	abs, err := filepath.Abs(outside)
	if err != nil {
		t.Fatalf("testkit: 归一化外部目录: %v", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("testkit: 建外部目录 %s: %v", abs, err)
	}
	secretPath := filepath.Join(abs, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("不该被读到的密钥\n"), 0o600); err != nil {
		t.Fatalf("testkit: 写外部密钥: %v", err)
	}
	// 归一化 Root，保证 Rel/Abs 比较时两边同为绝对路径。
	rootAbs, err := filepath.Abs(root)
	if err == nil {
		ws.Root = rootAbs
	}
	ws.Outside = abs

	// 工作区内 symlink → 工作区外（09 §9.2 的真实落点判定样本）。
	link := filepath.Join(ws.Root, "escape-link")
	if lerr := os.Symlink(secretPath, link); lerr == nil {
		ws.EscapeLink = link
		ws.SymlinkOK = true
	} else {
		ws.SymlinkErr = lerr
		ws.EscapeLink = ""
	}

	t.Cleanup(func() { cleanTree(ws.Root); cleanTree(abs) })
	return ws
}

// SymlinkUnsupported 报告当前平台/权限是否建不了符号链接。
func (w *Workspace) SymlinkUnsupported() bool { return !w.SymlinkOK }

// SymlinkSkipReason 给出跳过 symlink 样本的人话原因。
func (w *Workspace) SymlinkSkipReason() string {
	if w.SymlinkErr == nil {
		return ""
	}
	return fmt.Sprintf("平台 %s 建符号链接失败（需要特权或开发者模式）：%v", runtime.GOOS, w.SymlinkErr)
}

// cleanTree 先删链接/只读项再删目录：Windows 上 RemoveAll 对指向外部的
// reparse point 可能失败，先逐个 unlink 更稳。
func cleanTree(dir string) {
	if dir == "" {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			_ = os.Remove(path)
			return nil
		}
		_ = os.Chmod(path, 0o755)
		return nil
	})
	_ = os.RemoveAll(dir)
}

// BaseName 是路径尾名（测试里造可预测的 workspaceHash 用）。
func BaseName(p string) string {
	return filepath.Base(filepath.Clean(p))
}

// WorkspaceHash 从工作区路径造一个稳定的短哈希（th_<hash>_<ulid> 的前半段）。
// 只做测试用的确定性归一，不是生产身份键（生产的规则见 01 §4.2：折叠盘符与大小写）。
func WorkspaceHash(path string) string {
	slanes := filepath.ToSlash(strings.ToLower(filepath.Clean(path)))
	slanes = strings.ReplaceAll(slanes, ":", "")
	var h uint64 = 1469598103934665603 // FNV-1a 64
	for i := 0; i < len(slanes); i++ {
		h ^= uint64(slanes[i])
		h *= 1099511628211
	}
	const digits = "0123456789abcdefghjkmnpqrstvwxyz"
	out := make([]byte, 0, 12)
	for h != 0 {
		out = append([]byte{digits[h%uint64(len(digits))]}, out...)
		h /= uint64(len(digits))
	}
	if len(out) == 0 {
		return "0"
	}
	return string(out)
}
