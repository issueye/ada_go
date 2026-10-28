package adapters_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/adapters"
	"github.com/adag/ada-go/internal/domain"
)

// newWorkspace 建一个临时工作区，返回真实路径（macOS 上 /var → /private/var 由 EvalSymlinks 处理）。
func newWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Skipf("无法解析临时目录真实路径: %v", err)
	}
	return real
}

// TestScopeResolvesVirtualRoot 验证脚本视图里工作区挂到 `/`（12 §6.12.1）。
func TestScopeResolvesVirtualRoot(t *testing.T) {
	t.Parallel()
	root := newWorkspace(t)
	s, err := adapters.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Resolve(context.Background(), "/src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !v.WithinWorkspace || v.Virtual != "/src/main.go" {
		t.Fatalf("解析结果不对：%+v", v)
	}
	want := filepath.Join(root, "src", "main.go")
	if v.Real != want {
		t.Fatalf("真实落点 = %q，期望 %q", v.Real, want)
	}
	// 宿主绝对路径不得出现在脚本视图里。
	if strings.Contains(v.Virtual, strings.TrimSuffix(root, string(filepath.Separator))) {
		t.Fatal("虚拟路径泄露了宿主绝对路径")
	}
}

// TestScopeRejectsEscapes 覆盖字符串层面与真实落点两层拒绝（09 §9.2）。
func TestScopeRejectsEscapes(t *testing.T) {
	t.Parallel()
	root := newWorkspace(t)
	s, err := adapters.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for _, bad := range []string{"", "relative/path", "/..", "/../etc/passwd", "/a/../../b", `C:\Windows`} {
		if _, err := s.Resolve(ctx, bad); err == nil {
			t.Errorf("非法虚拟路径 %q 必须被拒", bad)
		}
	}
	if _, err := s.Resolve(ctx, "/ok/../other"); err == nil {
		t.Error("含 .. 段必须被拒（即使折叠后仍在工作区内）")
	}
}

// TestScopeRejectsSymlinkEscape 是关键用例：工作区内的链接指向外面，按真实落点拒绝。
func TestScopeRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建 symlink 需要特权或开发者模式；CI 的 linux/mac 腿覆盖这条")
	}
	t.Parallel()
	root := newWorkspace(t)
	outside := t.TempDir()
	outside, _ = filepath.EvalSymlinks(outside)
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("无法创建 symlink: %v", err)
	}
	s, err := adapters.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(context.Background(), "/escape/secret.txt"); err == nil {
		t.Fatal("工作区内指向外部的 symlink 必须按真实落点拒绝")
	}
	// 工作区内的普通链接不受影响。
	inside := filepath.Join(root, "inner")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	innerLink := filepath.Join(root, "link-inner")
	if err := os.Symlink(inside, innerLink); err != nil {
		t.Skipf("无法创建 symlink: %v", err)
	}
	if _, err := s.Resolve(context.Background(), "/link-inner/a.txt"); err != nil {
		t.Fatalf("工作区内链接应可用: %v", err)
	}
}

// TestScopeReadOnlyFlag 验证只读视图会传到宿主（12 §6.12.1 成员裁剪的依据）。
func TestScopeReadOnlyFlag(t *testing.T) {
	t.Parallel()
	root := newWorkspace(t)
	s, err := adapters.NewScope(root, adapters.WithReadOnly(true))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Resolve(context.Background(), "/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !v.ReadOnly || !s.ReadOnly() {
		t.Fatalf("只读标记没传下去：%+v", v)
	}
}

// TestScopeMissingWorkspaceFailsClosed 验证「工作区不存在」直接拒绝而不是创建（01 §4.2）。
func TestScopeMissingWorkspaceFailsClosed(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := adapters.NewScope(missing)
	if err == nil {
		t.Fatal("工作区不存在必须拒绝")
	}
	de, ok := domain.AsError(err)
	if !ok || de.Code != domain.CodeWorkspaceMissing {
		t.Fatalf("错误必须是 workspace_missing，实得 %v", err)
	}
}

// TestCheckExecRules 验证命令必须是裸程序名、cwd 在范围内（09 §9.2）。
func TestCheckExecRules(t *testing.T) {
	t.Parallel()
	root := newWorkspace(t)
	s, err := adapters.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.CheckExec(ctx, "/", "go"); err != nil {
		t.Fatalf("裸程序名应可用: %v", err)
	}
	for _, bad := range []string{"./evil.sh", "/bin/sh", "C:\\tools\\evil.exe", `sub\cmd`} {
		if _, err := s.CheckExec(ctx, "/", bad); err == nil {
			t.Errorf("命令 %q 必须被拒（带路径分隔符即逃逸 PATH 白名单）", bad)
		}
	}
	if _, err := s.CheckExec(ctx, "/..", "go"); err == nil {
		t.Error("越界 cwd 必须被拒")
	}
	if _, err := s.CheckExec(ctx, "/", ""); err == nil {
		t.Error("空命令必须被拒")
	}
}

// ---------- 密钥 ----------

// TestSecretsLookupOrder 验证环境变量优先于文件（附录 B 的优先级口径）。
func TestSecretsLookupOrder(t *testing.T) {
	// t.Setenv 禁止与 t.Parallel 同时使用（Go 的测试框架约束），这条只能是串行用例。
	if runtime.GOOS == "windows" {
		// 写文件后的权限位不可控（ACL 平台），LoadFile 的 0600 判定在 Windows 上跳过，
		// 这里仍验证读取与优先级逻辑本身。
		_ = 0o600
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.toml")
	if err := os.WriteFile(path, []byte("openai_api_key = \"from-file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := adapters.NewSecrets()
	if err := s.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got, found, err := s.Lookup(ctx, "openai_api_key")
	if err != nil || !found || got != "from-file" {
		t.Fatalf("文件读取不对：%q found=%v err=%v", got, found, err)
	}

	t.Setenv(adapters.SecretEnvPrefix+"OPENAI_API_KEY", "from-env")
	got, _, _ = s.Lookup(ctx, "openai_api_key")
	if got != "from-env" {
		t.Fatalf("环境变量应优先，实得 %q", got)
	}
	if _, found, _ := s.Lookup(ctx, "not_configured"); found {
		t.Fatal("未配置的密钥不应存在")
	}
}

// TestSecretsRejectsLoosePermissions 验证 0600 要求（09 §9.4）。
//
// Windows 上权限位由 NTFS ACL 决定，os.WriteFile 的 mode 不生效，因此只在 unix 腿断言。
func TestSecretsRejectsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 ACL 语义不同，权限检查在 unix 腿覆盖")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "secrets.toml")
	if err := os.WriteFile(path, []byte("k = \"v\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := adapters.NewSecrets()
	err := s.LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("宽权限密钥文件必须拒绝加载：%v", err)
	}
}

// TestSecretsDeclaredAndRedact 验证按名授权与日志脱敏（12 §6.13）。
func TestSecretsDeclaredAndRedact(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "secrets.toml")
	if err := os.WriteFile(path, []byte("tok = \"super-secret-value\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := adapters.NewSecrets()
	if err := s.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	s.SetDeclared("provider-x", []string{"tok"})
	ctx := context.Background()
	if !s.Declared(ctx, "provider-x", "tok") {
		t.Fatal("已声明的密钥名必须可判定")
	}
	if s.Declared(ctx, "provider-y", "tok") {
		t.Fatal("未声明的插件不能借用别人的密钥")
	}
	if s.Declared(ctx, "provider-x", "other") {
		t.Fatal("越名申请必须为 false")
	}
	got := s.Redact(ctx, "报错里带了 super-secret-value 一起打出去了")
	if strings.Contains(got, "super-secret-value") {
		t.Fatalf("脱敏失败：%s", got)
	}
	if len(s.Names()) != 1 || s.Names()[0] != "tok" {
		t.Fatalf("Names 只给名字不给值：%v", s.Names())
	}
}

// TestSecretsMissingFileIsNotError 保证离线首启动可用（附录 B：密钥可以完全没有）。
func TestSecretsMissingFileIsNotError(t *testing.T) {
	t.Parallel()
	s := adapters.NewSecrets()
	if err := s.LoadFile(filepath.Join(t.TempDir(), "absent.toml")); err != nil {
		t.Fatalf("缺失密钥文件不应报错: %v", err)
	}
}

// ---------- 时钟与 id ----------

func TestULIDGenShapes(t *testing.T) {
	t.Parallel()
	g := adapters.NewULIDGen()
	ctx := context.Background()
	th, err := g.NewThreadID(ctx, "0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(th, "th_0123456789ab_") || len(th) != len("th_")+12+1+len("01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Fatalf("线程 id 形状不对: %q", th)
	}
	if _, err := g.NewThreadID(ctx, ""); err == nil {
		t.Error("缺工作区哈希时必须拒绝")
	}
	for _, c := range []struct {
		gen    func() (string, error)
		prefix string
	}{
		{func() (string, error) { return g.NewCallID(ctx) }, "call_"},
		{func() (string, error) { return g.NewCheckpointID(ctx) }, "chk_"},
		{func() (string, error) { return g.NewULID(ctx) }, ""},
	} {
		id, err := c.gen()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, c.prefix) {
			t.Errorf("id %q 缺少前缀 %q", id, c.prefix)
		}
	}
	// 单调性：同一毫秒内也必须严格递增。
	prev, _ := g.NewULID(ctx)
	for i := 0; i < 200; i++ {
		next, _ := g.NewULID(ctx)
		if next <= prev {
			t.Fatalf("ULID 非单调：%s >= %s", prev, next)
		}
		prev = next
	}
}
