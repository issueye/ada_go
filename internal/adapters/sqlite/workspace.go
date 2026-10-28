package sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WorkspaceHash 按 01 §4.2 的身份键规则算出工作区的 12 位哈希：
// filepath.Abs → EvalSymlinks（真实落点）→ Windows 折叠盘符与大小写、统一分隔符 →
// sha256[:12]。E:\x、E:/x/、e:/X 必须落到同一个键（01 §4.2）。
//
// 它同时用于 <dataDir>/workspaces/<hash> 目录名、锁文件名 ws-<hash>.lock 与
// 线程 id 前缀 th_<hash>_<ulid>（04 §5.7）。
func WorkspaceHash(workspace string) (string, error) {
	real, err := WorkspaceKey(workspace)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(real))
	return hex.EncodeToString(sum[:])[:12], nil
}

// WorkspaceKey 返回归一化后的真实路径（身份键的原文，供 workspace 列与诊断输出）。
func WorkspaceKey(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", invalidInput("工作区路径为空：身份键无法计算（01 §4.2）")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("sqlite: 归一化工作区路径失败 %s: %w", workspace, err)
	}
	// EvalSymlinks 需要路径存在；不存在时退回到 Abs（01 §4.2 的 workspace_missing
	// 由上层判定，本包只在打开时要求目录存在）。
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(filepath.ToSlash(abs))
	}
	return abs, nil
}

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
