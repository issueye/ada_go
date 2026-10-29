package sqlite

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/adag/ada-go/internal/workspacepath"
)

// WorkspaceHash / WorkspaceKey 保留在适配器上，供本包与既有调用方使用；
// 规则本体在 internal/workspacepath，Gateway 的进程监督也直接用它。
// 这样基座的不同部分不会各自实现一套路径归一化（01 §4.2）。
func WorkspaceHash(workspace string) (string, error) { return workspacepath.Hash(workspace) }

func WorkspaceKey(workspace string) (string, error) { return workspacepath.Key(workspace) }

// DefaultDataDir 返回产品级默认数据目录 ~/.adago/workspaces/<hash>（04 §5.7）。
//
// 测试与 CLI 都应显式传 Config.DataDir；本函数只在留空时使用，且绝不写死到某台机器。
func DefaultDataDir(workspace string) (string, error) {
	hash, err := WorkspaceHash(workspace)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sqlite: 无法定位用户主目录（显式传 Config.DataDir 即可绕开）: %w", err)
	}
	return filepath.Join(home, ".adago", "workspaces", hash), nil
}

// StateDBPath 是 <dataDir>/state.db。
func StateDBPath(dataDir string) string { return filepath.Join(dataDir, "state.db") }

// LockDir 是 <dataDir>/locks：插件与工具的 scope 之外的目录（04 §5.7、ports/scope.go
// 的 PathView.ReadOnly 约定由 scope 适配器实施，本包只保证「锁不落在数据库文件里」）。
func LockDir(dataDir string) string { return filepath.Join(dataDir, "locks") }

// LockPath 是 <dataDir>/locks/ws-<hash>.lock（04 §5.7）。
func LockPath(dataDir, hash string) string {
	return filepath.Join(LockDir(dataDir), "ws-"+hash+".lock")
}
