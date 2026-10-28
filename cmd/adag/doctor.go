package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/engine"
	"github.com/adag/ada-go/internal/plugins"
)

// repoRoot 从当前目录向上找 go.mod，定位仓库根。
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("找不到仓库根（go.mod）；请在 ada-go 仓库内运行")
		}
		dir = parent
	}
}

// checkRepoRoot 确认目录骨架与预期一致（设计 13 的布局）。
func checkRepoRoot(root string) error {
	required := []string{"spec/proto.json", "products/coding.agent.json", "products/skeleton.agent.json", "docs/design.md"}
	for _, r := range required {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(r))); err != nil {
			return fmt.Errorf("缺少 %s: %w", r, err)
		}
	}
	return nil
}

// checkSpecFiles 确认三份契约源都在且可解析。
func checkSpecFiles(root string) error {
	for _, name := range []string{"proto.json", "manifest.schema.json", "vnode.schema.json", "product.schema.json"} {
		p := filepath.Join(root, "spec", name)
		raw, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("缺少契约源 %s: %w", name, err)
		}
		if !json.Valid(raw) {
			return fmt.Errorf("契约源 %s 不是合法 JSON", name)
		}
	}
	return nil
}

// checkProductsBoot 用每份产品声明跑一次装配。
func checkProductsBoot(root string) error {
	files, err := coreapp.DiscoverProducts(filepath.Join(root, "products"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("products/ 里没有 *.agent.json")
	}
	var problems []string
	for _, f := range files {
		p, err := coreapp.LoadProduct(f)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		rt, err := coreapp.Assemble(p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", p.ID, err))
			continue
		}
		if len(p.Plugins) == 0 && !rt.IsEmptyBase() {
			problems = append(problems, fmt.Sprintf("%s: 声明为空但装配结果不为空（INV-11）", p.ID))
		}
	}
	// 钩子与插槽注册表的自洽性也在这里报出：doctor 是排障入口，不该只报文件问题。
	if err := plugins.CheckCapabilitySites(); err != nil {
		problems = append(problems, err.Error())
	}
	if len(engine.HookSites()) != len(plugins.HookPoints()) {
		problems = append(problems, fmt.Sprintf("钩子点位 %d 个但调用方 %d 个", len(plugins.HookPoints()), len(engine.HookSites())))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// checkDataDir 确认数据目录可创建且可写；密钥文件权限提示（09 §9.4）。
func checkDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("数据目录不可用 %s: %w", dir, err)
	}
	probe := filepath.Join(dir, ".adag-doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("数据目录不可写 %s: %w", dir, err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("数据目录无法清理探针: %w", err)
	}
	secrets := filepath.Join(dir, "secrets.toml")
	if info, err := os.Stat(secrets); err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s 权限 %o 过宽，密钥文件必须是 0600（09 §9.4）", secrets, info.Mode().Perm())
		}
	}
	return nil
}

// checkFakeProvider 说明 fake 路径的覆盖边界。
func checkFakeProvider() string {
	return "fake 模型在 internal/devfake（CLI --fake）；provider 桥由官方 Chat 插件的真实 SSE 测试覆盖"
}

// checkSQLite 用只读连接检查完整性、schema 版本与 WAL 参数（09 §10 doctor 项）。
func checkSQLite(dataDir string) error {
	if err := sqlite.EnsureLocalDataDir(dataDir); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := sqlite.OpenReadOnly(ctx, sqlite.Config{DataDir: dataDir})
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.CheckIntegrity(ctx); err != nil {
		return err
	}
	v, err := r.Version(ctx)
	if err != nil {
		return err
	}
	if v != sqlite.SchemaVersion() {
		return fmt.Errorf("schema 版本 v%d，本二进制期望 v%d", v, sqlite.SchemaVersion())
	}
	p, err := r.Pragmas(ctx)
	if err != nil {
		return err
	}
	if !strings.EqualFold(p.JournalMode, "wal") {
		return fmt.Errorf("journal_mode = %s，期望 wal（04 §5.7）", p.JournalMode)
	}
	return nil
}

// checkLock 检查工作区锁：空闲则取一次再释放；被运行中的 Core 持有也算正常。
func checkLock(workspace, dataDir string) error {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	hash, err := sqlite.WorkspaceHash(abs)
	if err != nil {
		return err
	}
	lock, err := sqlite.AcquireWorkspaceLock(dataDir, hash)
	if err == nil {
		return lock.Release()
	}
	var le *sqlite.LockedError
	if errors.As(err, &le) {
		return nil // 另一个 Core 正在运行；doctor 只报告，不抢占。
	}
	return err
}
