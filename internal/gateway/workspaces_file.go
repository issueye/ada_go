package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workspacesFileData 是用户级工作区列表的落盘形状。
type workspacesFileData struct {
	Workspaces []string `json:"workspaces"`
}

// loadWorkspacesFile 读取用户上次打开过的工作区；不存在的目录直接跳过，
// 避免移动/删除项目后启动列表里留下永远打不开的条目。
func loadWorkspacesFile(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var data workspacesFileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	out := make([]string, 0, len(data.Workspaces))
	for _, candidate := range data.Workspaces {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, abs)
	}
	return out, nil
}

// saveWorkspacesFile 原子写入工作区列表（0600，同目录临时文件 + rename）。
func saveWorkspacesFile(path string, workspaces []string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(workspacesFileData{
		Workspaces: append([]string(nil), workspaces...),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("工作区列表无法序列化: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建工作区状态目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".workspaces-*.json")
	if err != nil {
		return fmt.Errorf("创建工作区状态临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置工作区状态权限失败: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入工作区列表失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭工作区状态文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换工作区列表失败: %w", err)
	}
	return nil
}
