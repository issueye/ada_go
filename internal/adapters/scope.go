package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// Scope 是 ports.Scope 的生产实现：路径解析与越界拒绝的唯一入口（09 §9.2）。
//
// 三条硬规则：
//  1. 先做字符串检查（禁绝对路径、`..` 段、盘符），再 EvalSymlinks 解析真实落点；
//     工作区内指向外面的 symlink/junction 按真实落点拒绝，不按字面路径放行。
//  2. 脚本侧看到的是以工作区为 `/` 的虚拟视图；宿主绝对路径永不进入脚本与错误信息。
//  3. Scope 是唯一入口：宿主 fs/exec 能力内部也走这里，插件无法自行拼路径绕过。
type Scope struct {
	// root 是 EvalSymlinks 之后的真实工作区根（无结尾分隔符）。
	root     string
	readOnly bool
	// lockDirName 是锁目录名：它在 scope 之外，插件删不掉也替换不了（04 §5.7）。
	sepInsens bool
}

var _ ports.Scope = (*Scope)(nil)

// Option 配置 Scope 行为。
type Option func(*Scope)

// WithReadOnly 让整个 scope 只读：写成员在宿主侧被拒（12 §6.12.1）。
func WithReadOnly(readOnly bool) Option {
	return func(s *Scope) { s.readOnly = readOnly }
}

// NewScope 以 workspace 为根创建 Scope。目录不存在或无法解析真实落点时直接失败：
// 为空气目录起进程只会白占资源（01 §4.2）。
func NewScope(workspace string, opts ...Option) (*Scope, error) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, domain.WrapError(domain.NewError(domain.CodeWorkspaceMissing, "无法解析工作区绝对路径"), err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, domain.WrapError(domain.NewError(domain.CodeWorkspaceMissing, "工作区目录不存在或不是目录"), err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return nil, domain.NewError(domain.CodeWorkspaceMissing, "工作区路径不是目录: "+abs)
	}
	s := &Scope{
		root:      filepath.Clean(real),
		sepInsens: runtime.GOOS == "windows" || runtime.GOOS == "darwin",
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Root 返回真实工作区根（宿主路径，只给基座内部使用，不进脚本与日志）。
func (s *Scope) Root() string { return s.root }

// ReadOnly 报告该 scope 是否整体只读。
func (s *Scope) ReadOnly() bool { return s.readOnly }

// Resolve 把插件侧虚拟路径解析为受限视图。
func (s *Scope) Resolve(_ context.Context, virtualPath string) (ports.PathView, error) {
	view := ports.PathView{Virtual: virtualPath, ReadOnly: s.readOnly}

	clean, err := sanitizeVirtual(virtualPath)
	if err != nil {
		return view, err
	}

	// 虚拟 `/` 就是工作区根；把剩余段拼到真实根上。
	rel := strings.TrimPrefix(clean, "/")
	var host string
	if rel == "" {
		host = s.root
	} else {
		host = filepath.Join(s.root, filepath.FromSlash(rel))
	}
	host = filepath.Clean(host)

	// 第一道：字符串层面必须仍在根内（防 join 后的 ../ 逃逸）。
	if !s.withinRoot(host) {
		return view, domain.NewError(domain.CodePluginDenied,
			"路径越界：解析后落在工作区之外（虚拟路径 "+virtualPath+"）")
	}

	// 第二道：真实落点。目标可能存在（含 symlink），也可能还不存在（新建文件），
	// 对不存在的尾部逐段回退到最近的已存在祖先再 EvalSymlinks。
	real, err := resolveExisting(host)
	if err != nil {
		return view, domain.WrapError(domain.NewError(domain.CodePluginDenied,
			"路径真实落点无法解析（虚拟路径 "+virtualPath+"）"), err)
	}
	if !s.withinRoot(real) {
		// symlink/junction 指向工作区外：按真实落点拒绝（09 §9.2）。
		return view, domain.NewError(domain.CodePluginDenied,
			"路径越界：软链接真实落点在工作区之外（虚拟路径 "+virtualPath+"）")
	}

	view.Real = real
	view.WithinWorkspace = true
	view.Virtual = "/" + filepath.ToSlash(rel)
	return view, nil
}

// CheckExec 校验拟执行命令的 cwd 与命令名。
//
// 命令能逃出路径沙箱是明确保留的口子（09 §9.2）：这里只保证 cwd 在范围内、
// 命令名不含路径分隔符（防止 ./evil.sh 这种绕开 PATH 白名单的写法），
// 「是否询问」由 role=tool-policy 的策略插件决定，基座不含规则。
func (s *Scope) CheckExec(ctx context.Context, cwd, command string) (ports.PathView, error) {
	if strings.TrimSpace(command) == "" {
		return ports.PathView{}, domain.NewError(domain.CodeToolInvalidArgs, "命令名为空")
	}
	if strings.ContainsAny(command, `/\`) || filepath.IsAbs(command) {
		return ports.PathView{}, domain.NewError(domain.CodePluginDenied,
			"命令必须是 PATH 上的裸程序名（禁止带路径分隔符），实际收到 "+command)
	}
	if strings.ContainsRune(command, 0) {
		return ports.PathView{}, domain.NewError(domain.CodeToolInvalidArgs, "命令名含 NUL")
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = "/"
	}
	view, err := s.Resolve(ctx, cwd)
	if err != nil {
		return view, err
	}
	return view, nil
}

// sanitizeVirtual 做纯字符串检查：必须是 `/` 开头的虚拟绝对路径，段内不得有 `.`/`..`。
func sanitizeVirtual(virtualPath string) (string, error) {
	if virtualPath == "" {
		return "", domain.NewError(domain.CodeToolInvalidArgs, "路径为空")
	}
	if len(virtualPath) > 4096 {
		return "", domain.NewError(domain.CodeToolInvalidArgs, "路径超过 4096 字节（12 §6.13）")
	}
	if !strings.HasPrefix(virtualPath, "/") {
		return "", domain.NewError(domain.CodePluginDenied,
			"路径必须是工作区内的虚拟绝对路径（以 / 开头），收到 "+virtualPath)
	}
	if strings.ContainsRune(virtualPath, '\\') || hasDrivePrefix(virtualPath) {
		return "", domain.NewError(domain.CodePluginDenied, "路径含宿主分隔符或盘符: "+virtualPath)
	}
	for _, seg := range strings.Split(strings.Trim(virtualPath, "/"), "/") {
		if seg == "." || seg == ".." {
			return "", domain.NewError(domain.CodePluginDenied, "路径不得含 . 或 .. 段: "+virtualPath)
		}
		if strings.ContainsRune(seg, 0) {
			return "", domain.NewError(domain.CodeToolInvalidArgs, "路径含 NUL")
		}
	}
	return filepath.ToSlash(filepath.Clean(virtualPath)), nil
}

func hasDrivePrefix(p string) bool {
	t := strings.TrimPrefix(p, "/")
	return len(t) >= 2 && t[1] == ':' && ((t[0] >= 'a' && t[0] <= 'z') || (t[0] >= 'A' && t[0] <= 'Z'))
}

// withinRoot 报告 path 是否仍在真实根内（大小写不敏感的平台折叠大小写）。
func (s *Scope) withinRoot(path string) bool {
	if path == s.root {
		return true
	}
	prefix := s.root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if !s.sepInsens {
		return strings.HasPrefix(path, prefix)
	}
	return strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix))
}

// resolveExisting 解析真实落点：路径本身存在就直接 EvalSymlinks；
// 不存在时逐段回退到最近的已存在祖先解析，再把剩余段接回去（新建文件的场景）。
func resolveExisting(path string) (string, error) {
	if _, err := os.Lstat(path); err == nil {
		return filepath.EvalSymlinks(path)
	}
	rest := []string{}
	cur := path
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("找不到已存在的祖先目录: %s", path)
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return filepath.Clean(real), nil
		}
	}
}
