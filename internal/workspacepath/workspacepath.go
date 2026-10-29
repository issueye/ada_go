// Package workspacepath 归一化工作区路径并计算身份键。
//
// 它不依赖存储或传输适配器：SQLite 适配器（目录名、锁文件）与 Gateway 的
// 进程监督（同一工作区只拉起一个 Core）必须用同一套规则，否则会出现
// 「同一个目录被当成两个工作区」的隐性分叉（01 §4.2）。
package workspacepath

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/adag/ada-go/internal/domain"
)

// Key 返回归一化后的真实路径（身份键的原文，供 workspace 列与诊断输出）。
//
// 规则：filepath.Abs → EvalSymlinks（真实落点）→ Clean →
// Windows 折叠盘符与大小写、统一分隔符。E:\x、E:/x/、e:/X 落到同一个键。
func Key(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", domain.NewError(domain.CodeToolInvalidArgs,
			"工作区路径为空：身份键无法计算（01 §4.2）")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("workspacepath: 归一化工作区路径失败 %s: %w", workspace, err)
	}
	// EvalSymlinks 需要路径存在；不存在时退回到 Abs（workspace_missing 由上层判定，
	// 本包只在打开时要求目录存在）。
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(filepath.ToSlash(abs))
	}
	return abs, nil
}

// Hash 返回 01 §4.2 的 12 位工作区哈希（sha256(Key)[:12]）。
//
// 它用于 <dataDir>/workspaces/<hash> 目录名、锁文件名 ws-<hash>.lock 与
// 线程 id 前缀 th_<hash>_<ulid>（04 §5.7）。
func Hash(workspace string) (string, error) {
	key, err := Key(workspace)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12], nil
}
