package cli

// 只读/维护命令：线程列表、导出、备份。都走 sqlite 适配器的既有实现，
// 不在 CLI 里另写一套 SQL（04 §5.7）。

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/ports"
)

// ListThreads 用只读连接列出工作区线程（不需要工作区锁）。
func ListThreads(ctx context.Context, workspace, dataDir string, limit int) ([]ports.Thread, error) {
	abs, dir, err := paths(workspace, dataDir)
	if err != nil {
		return nil, err
	}
	r, err := sqlite.OpenReadOnly(ctx, sqlite.Config{Workspace: abs, DataDir: dir})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if limit <= 0 {
		limit = 100
	}
	page, err := r.ListThreads(ctx, ports.ListThreadsRequest{Workspace: abs, Limit: limit})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// ExportThread 导出单个线程（threadID 为空时导出全部）到 w。
func ExportThread(ctx context.Context, workspace, dataDir, threadID, format string, w io.Writer) (sqlite.ExportSummary, error) {
	abs, dir, err := paths(workspace, dataDir)
	if err != nil {
		return sqlite.ExportSummary{}, err
	}
	r, err := sqlite.OpenReadOnly(ctx, sqlite.Config{Workspace: abs, DataDir: dir})
	if err != nil {
		return sqlite.ExportSummary{}, err
	}
	defer r.Close()
	return r.Export(ctx, w, sqlite.ExportRequest{
		ThreadID: threadID,
		Format:   sqlite.ExportFormat(format),
	})
}

// BackupWorkspace 在线备份：必须持工作区锁（与 Core 一样），VACUUM INTO 目标文件。
func BackupWorkspace(ctx context.Context, workspace, dataDir, dest string) (sqlite.BackupInfo, error) {
	abs, dir, err := paths(workspace, dataDir)
	if err != nil {
		return sqlite.BackupInfo{}, err
	}
	st, err := sqlite.OpenCore(ctx, sqlite.Config{Workspace: abs, DataDir: dir})
	if err != nil {
		return sqlite.BackupInfo{}, err
	}
	defer func() { _ = st.Close(context.Background()) }()
	return st.Backup(ctx, dest)
}

func paths(workspace, dataDir string) (string, string, error) {
	if workspace == "" {
		return "", "", fmt.Errorf("cli: 需要工作区目录")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", err
	}
	dir := dataDir
	if dir == "" {
		dir, err = sqlite.DefaultDataDir(abs)
		if err != nil {
			return "", "", err
		}
	}
	return abs, dir, nil
}
