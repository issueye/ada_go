package web

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---- 文件系统 API（FilePicker） -------------------------------------------

// fsListRoot 是一个可选的工作区根。
type fsListRoot struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// fsListEntry 是目录下的一项（只列目录）。
type fsListEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Selectable bool   `json:"selectable"`
}

// fsListResponse 是 FilePicker 的目录快照。
type fsListResponse struct {
	Path       string        `json:"path"`
	Parent     string        `json:"parent"`
	Roots      []fsListRoot  `json:"roots"`      // Windows 盘符 / Unix 根与主目录
	Workspaces []fsListRoot  `json:"workspaces"` // 已打开/已配置的工作区，快捷入口
	Entries    []fsListEntry `json:"entries"`
	Selectable bool          `json:"selectable"`
	Truncated  bool          `json:"truncated"`
	Omitted    int           `json:"omitted"`
}

// handleFSList 是 FilePicker 的只读目录 API。
//
// Windows 上不带 path 时返回盘符列表（A:–Z:），让用户先选盘；Unix 返回
// 根目录与主目录。带 path 时浏览该目录。只列目录，不列文件，也不返回
// 文件内容。路径统一 EvalSymlinks，页面拿到的是真实落点。
func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimSpace(r.URL.Query().Get("path"))
	current := ""
	entries := []fsListEntry{}
	truncated := false
	omitted := 0
	if requested != "" {
		real, err := realPath(requested)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "目录不存在或无法访问")
			return
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			writeJSONError(w, http.StatusNotFound, "这个路径不是目录")
			return
		}
		current = real
		var listed []workspaceEntry
		listed, truncated, omitted = listDirectories(current, 500)
		entries = make([]fsListEntry, 0, len(listed))
		for _, entry := range listed {
			child, err := realPath(entry.Path)
			if err != nil {
				continue
			}
			entries = append(entries, fsListEntry{Name: entry.Name, Path: child, Selectable: true})
		}
	}

	parent := ""
	if current != "" {
		if dir := filepath.Dir(current); !samePath(dir, current) {
			parent = dir
		}
	}
	writeJSON(w, http.StatusOK, fsListResponse{
		Path:       current,
		Parent:     parent,
		Roots:      fsSystemRoots(),
		Workspaces: fsWorkspaceRoots(s.workspaceList()),
		Entries:    entries,
		Selectable: current != "",
		Truncated:  truncated,
		Omitted:    omitted,
	})
}

// fsSystemRoots 返回文件系统入口：Windows 是盘符，其他平台是 / 与主目录。
func fsSystemRoots() []fsListRoot {
	if os.PathSeparator == '\\' {
		out := make([]fsListRoot, 0, 8)
		// 从 C: 开始：A:/B: 是软驱字母，Stat 可能触发系统插入磁盘提示。
		for c := 'C'; c <= 'Z'; c++ {
			root := string(c) + ":\\"
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				out = append(out, fsListRoot{Label: string(c) + ":", Path: root})
			}
		}
		return out
	}
	out := []fsListRoot{{Label: "/", Path: "/"}}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, fsListRoot{Label: "主目录", Path: home})
	}
	return out
}

// fsWorkspaceRoots 把工作区列表转成 FilePicker 的快捷入口。
func fsWorkspaceRoots(workspaces []string) []fsListRoot {
	out := make([]fsListRoot, 0, len(workspaces))
	for _, ws := range workspaces {
		real, err := realPath(ws)
		if err != nil {
			continue
		}
		name := filepath.Base(real)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = real
		}
		out = append(out, fsListRoot{Label: name, Path: real})
	}
	return out
}

// realPath 把路径规范成真实落点（EvalSymlinks）；不存在时返回错误。
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// samePath 比较两个已规范化的路径；Windows 下大小写不敏感。
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// handleWorkspaceMkdir 在工作区子树内新建目录（目录选择器的“新建文件夹”）。
func (s *Server) handleWorkspaceMkdir(w http.ResponseWriter, r *http.Request) {
	root, _ := s.resolveWorkspace(r)
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单。", err.Error(), "/workspaces", "返回")
		return
	}
	parent := strings.TrimSpace(r.PostFormValue("parent"))
	if parent == "" || !withinRoot(root, parent) {
		parent = root
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		s.renderError(w, r, http.StatusBadRequest, "目录名不合法",
			"目录名不能为空，也不能包含路径分隔符。", "name="+name, "/workspaces?path="+urlQueryEscape(parent), "返回")
		return
	}
	target := filepath.Join(parent, name)
	if !withinRoot(root, target) {
		s.renderError(w, r, http.StatusBadRequest, "目录越界",
			"新建目录必须位于当前工作区内。", "", "/workspaces", "返回")
		return
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "无法新建目录",
			"服务端创建目录失败。请检查权限后重试。", err.Error(),
			"/workspaces?path="+urlQueryEscape(parent), "返回")
		return
	}
	redirect(w, "/workspaces?path="+urlQueryEscape(parent))
}

// listDirectories 列出目录下的子目录（只列目录；最多 limit 项）。
func listDirectories(dir string, limit int) ([]workspaceEntry, bool, int) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, 0
	}
	dirs := make([]fs.DirEntry, 0, len(items))
	for _, item := range items {
		if item.IsDir() {
			dirs = append(dirs, item)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
	omitted := 0
	if len(dirs) > limit {
		omitted = len(dirs) - limit
		dirs = dirs[:limit]
	}
	entries := make([]workspaceEntry, 0, len(dirs))
	for _, item := range dirs {
		full := filepath.Join(dir, item.Name())
		entries = append(entries, workspaceEntry{
			Name: item.Name(),
			Path: full,
			Href: "/workspaces?path=" + urlQueryEscape(full),
		})
	}
	return entries, omitted > 0, omitted
}
