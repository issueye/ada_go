package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// pluginSummaryDTO 对应 Core plugin.list 的一行（只声明页面用到的字段）。
type pluginSummaryDTO struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	APIVersion    int             `json:"apiVersion"`
	Trust         string          `json:"trust"`
	Source        string          `json:"source"`
	Path          string          `json:"path"`
	Enabled       bool            `json:"enabled"`
	Status        string          `json:"status"`
	LastError     string          `json:"lastError"`
	Readme        string          `json:"readme"`
	Files         []pluginFileDTO `json:"files"`
	Contributions map[string]any  `json:"contributions"`
}

type pluginListResult struct {
	Plugins []pluginSummaryDTO `json:"plugins"`
}

// ---- 插件 ----------------------------------------------------------------

// extensionsState 合并插件列表与 CSRF，供 /partials/extensions.json 输出。
func (s *Server) extensionsState(r *http.Request) extensionsState {
	plugins := s.collectPlugins(r, "")
	return extensionsState{
		Plugins: plugins,
		Count:   len(plugins),
		CSRF:    s.csrf(r),
	}
}

// handleExtensionsJSON 是插件列表岛的刷新接口（只读）。
// ?detail=<id> 时按详情口径取那一个插件（清单文件、README 等只有详情请求
// 才投影），供插件详情页复用同一端点。
func (s *Server) handleExtensionsJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	state := s.extensionsState(r)
	if detail := strings.TrimSpace(r.URL.Query().Get("detail")); detail != "" {
		if p, ok := s.pluginByID(r, detail); ok {
			for i := range state.Plugins {
				if state.Plugins[i].ID == detail {
					state.Plugins[i] = p
					break
				}
			}
		}
	}
	_ = json.NewEncoder(w).Encode(state)
}

// handlePluginFile 返回插件包内单个文件的文本预览。
// 只允许详情请求里出现过的文件（相对路径），防止把插件目录之外的任意文件读给浏览器。
func (s *Server) handlePluginFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := s.pluginByID(r, id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "没有这个插件")
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少文件路径")
		return
	}
	view, err := s.readPluginFile(p, rel)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// pluginByID 用 plugin.list 的详情请求定位单个插件。
func (s *Server) pluginByID(r *http.Request, id string) (pluginRow, bool) {
	for _, p := range s.collectPlugins(r, id) {
		if p.ID == id {
			return p, true
		}
	}
	return pluginRow{}, false
}

const maxPluginFilePreview = 512 << 10

func pluginFileMeta(p pluginRow, filePath string) pluginFileView {
	for _, f := range p.Files {
		if f.Path == filePath {
			return pluginFileView{Path: f.Path, Size: f.Size, SizeLabel: f.SizeLabel}
		}
	}
	return pluginFileView{Path: filePath}
}

// readPluginFile 读取插件包内文件：只接受清单里的相对路径，大小有上限，
// 二进制文件返回标记而不把字节塞进 JSON。
func (s *Server) readPluginFile(p pluginRow, rel string) (pluginFileView, error) {
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "./")
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "" || rel == "." || path.IsAbs(rel) || strings.HasPrefix(rel, "../") {
		return pluginFileView{}, fmt.Errorf("文件路径不合法")
	}
	meta := pluginFileMeta(p, rel)
	found := false
	for _, f := range p.Files {
		if f.Path == rel && !f.Dir {
			meta = pluginFileView{Path: f.Path, Size: f.Size, SizeLabel: f.SizeLabel}
			found = true
			break
		}
	}
	if !found {
		return pluginFileView{}, fmt.Errorf("文件不在插件目录中")
	}
	if strings.TrimSpace(p.Path) == "" {
		return meta, fmt.Errorf("插件文件未落盘，无法预览")
	}
	root, err := realPath(p.Path)
	if err != nil {
		return meta, fmt.Errorf("插件目录不可读")
	}
	target, err := realPath(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return meta, fmt.Errorf("文件不存在或不可读")
	}
	if !withinRoot(root, target) {
		return meta, fmt.Errorf("文件路径越界")
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return meta, fmt.Errorf("文件不存在或不是普通文件")
	}
	f, err := os.Open(target)
	if err != nil {
		return meta, fmt.Errorf("无法打开文件")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxPluginFilePreview+1))
	if err != nil {
		return meta, fmt.Errorf("读取文件失败")
	}
	truncated := len(data) > maxPluginFilePreview
	if truncated {
		data = data[:maxPluginFilePreview]
	}
	meta.Size = info.Size()
	meta.SizeLabel = humanBytes(info.Size())
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		meta.Binary = true
		return meta, nil
	}
	meta.Content = string(data)
	// Markdown 文件只标记能力：预览渲染由前端做（源码高亮同理，服务端不产出 HTML）。
	if isMarkdownFile(meta.Path) {
		meta.Markdown = true
	}
	meta.Truncated = truncated
	return meta, nil
}

// isMarkdownFile 判断预览文件是否按 Markdown 渲染（只看扩展名，不看内容嗅探：
// 扩展名是插件作者的选择，嗅探会把 README 之外的 .txt 也画成富文本）。
func isMarkdownFile(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	default:
		return false
	}
}

// collectPlugins 合并前端插件（渲染桥）与后端插件（Core plugin.list）。
// 同一个 id 同时具备两段能力时合并成一条；前端/后端标记只在详情展示。
func (s *Server) collectPlugins(r *http.Request, detailID string) []pluginRow {
	byID := make(map[string]*pluginRow)
	order := make([]string, 0, 8)
	upsert := func(id string) *pluginRow {
		if p, ok := byID[id]; ok {
			return p
		}
		p := &pluginRow{ID: id, Enabled: true, Status: "ready"}
		byID[id] = p
		order = append(order, id)
		return p
	}
	if lister, ok := s.cfg.PluginUI.(PluginLister); ok {
		for _, p := range lister.List(r) {
			row := upsert(p.ID)
			row.Frontend = true
			if p.Name != "" {
				row.Name = p.Name
			}
			if p.Version != "" {
				row.Version = p.Version
			}
			if p.Description != "" {
				row.Description = p.Description
			}
			if p.Status != "" {
				row.Status = p.Status
			}
		}
	}
	listParams := map[string]any{}
	if detailID != "" {
		listParams["detailId"] = detailID
	}
	if raw, err := s.call(r, "plugin.list", listParams); err == nil {
		var res pluginListResult
		if json.Unmarshal(raw, &res) == nil {
			for _, p := range res.Plugins {
				row := upsert(p.ID)
				row.Frontend = row.Frontend || contributionBool(p.Contributions["frontend"])
				// 旧 Core 不返回段标记时按后端处理（历史上 plugin.list 只有后端行）。
				row.Backend = contributionBool(p.Contributions["backend"]) || !row.Frontend
				if row.Name == "" {
					row.Name = p.Name
				}
				if row.Version == "" {
					row.Version = p.Version
				}
				row.Source = pluginScope(p.Source, p.Trust)
				row.Path = p.Path
				row.Trust = p.Trust
				row.APIVersion = p.APIVersion
				row.Status = p.Status
				row.Enabled = p.Enabled
				row.Error = p.LastError
				row.Readme = p.Readme
				row.Files = make([]pluginFileDTO, 0, len(p.Files))
				for _, f := range p.Files {
					row.Files = append(row.Files, pluginFileDTO{
						Path: f.Path, Size: f.Size, Dir: f.Dir, Depth: f.Depth,
						SizeLabel:  humanBytes(f.Size),
						DepthClass: pluginDepthClass(f.Depth),
					})
				}
				row.Tools = contributionCount(p.Contributions["tools"])
				row.Hooks = contributionCount(p.Contributions["hooks"])
				row.Skills = contributionCount(p.Contributions["skills"])
			}
		}
	}
	rows := make([]pluginRow, 0, len(order))
	for _, id := range order {
		rows = append(rows, *byID[id])
	}
	for i := range rows {
		rows[i].StatusLabel, rows[i].StatusTone = pluginStatusView(rows[i].Status, rows[i].Enabled)
	}
	sort.Slice(rows, func(i, j int) bool {
		ai, aj := rows[i].Name, rows[j].Name
		if ai == "" {
			ai = rows[i].ID
		}
		if aj == "" {
			aj = rows[j].ID
		}
		if ai != aj {
			return ai < aj
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// handleExtensionsReload 重载全部 agent 插件（前端插件在装配期固定，不在此处重载）。
func (s *Server) handleExtensionsReload(w http.ResponseWriter, r *http.Request) {
	path := actionReturnPath(r)
	if _, err := s.call(r, "plugin.reload", map[string]any{}); err != nil {
		redirect(w, flashRedirect(path, "error", cleanRPCError(err)))
		return
	}
	redirect(w, flashRedirect(path, "notice", "插件已重载"))
}

// handleAgentPluginsReload 与 handleExtensionsReload 同义（a_da 的两个按钮）。
func (s *Server) handleAgentPluginsReload(w http.ResponseWriter, r *http.Request) {
	s.handleExtensionsReload(w, r)
}

func (s *Server) handlePluginEnable(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, true)
}

func (s *Server) handlePluginDisable(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, false)
}

func (s *Server) setPluginEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	path := actionReturnPath(r)
	if id == "" {
		redirect(w, flashRedirect(path, "error", "缺少插件 id"))
		return
	}
	if _, err := s.call(r, "plugin.setEnabled", map[string]any{"pluginId": id, "enabled": enabled}); err != nil {
		// 业务拒绝（例如插件与管理操作冲突）回到列表用提示条说明，不渲染 502。
		redirect(w, flashRedirect(path, "error", cleanRPCError(err)))
		return
	}
	// 前端插件是否渲染由渲染桥按 plugin.list 实时判断；让它的短缓存立刻失效。
	if inval, ok := s.cfg.PluginUI.(interface{ InvalidateEnabled(string) }); ok {
		ws, _ := s.resolveWorkspace(r)
		inval.InvalidateEnabled(ws)
	}
	action := "已启用"
	if !enabled {
		action = "已停用"
	}
	redirect(w, flashRedirect(path, "notice", action+"插件 "+id))
}

// actionReturnPath 决定表单动作后的回跳地址。
//
// 页面统一发 Referrer-Policy: no-referrer，浏览器不会带 Referer，单靠
// safeReturnPath 会退回首页——插件启停因此会“跳回会话界面”。表单显式
// 带 next 时优先用它（只接受站内绝对路径）。
func actionReturnPath(r *http.Request) string {
	if next := strings.TrimSpace(r.PostFormValue("next")); next != "" {
		if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, `\`) {
			if u, err := url.Parse(next); err == nil && u.Host == "" {
				return next
			}
		}
	}
	return safeReturnPath(r)
}

// flashRedirect 给回跳地址加上提示参数。
func flashRedirect(path, kind, msg string) string {
	if path == "" {
		path = "/"
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + kind + "=" + url.QueryEscape(msg)
}

// cleanRPCError 去掉 relay 传输前缀，只留 Core 的人话错误。
func cleanRPCError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	msg = strings.TrimPrefix(msg, "relay: ")
	if i := strings.Index(msg, "RPC 错误 "); i >= 0 {
		rest := msg[i+len("RPC 错误 "):]
		if j := strings.Index(rest, ": "); j >= 0 {
			msg = rest[j+2:]
		}
	}
	return clip(msg, 600)
}

func pluginScope(source, trust string) string {
	if strings.TrimSpace(source) == "" {
		return "bundled"
	}
	return source
}

func contributionCount(value any) int {
	switch v := value.(type) {
	case []any:
		return len(v)
	case []string:
		return len(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func contributionBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

// pluginStatusView 把 Core 的状态映射成列表/详情的中文标签与色调。
// unused = 随包但当前产品未声明，不是故障。
func pluginStatusView(status string, enabled bool) (label, tone string) {
	switch status {
	case "ready":
		return "启用", "success"
	case "disabled":
		return "停用", "neutral"
	case "unused":
		return "未启用", "neutral"
	case "failed":
		return "失败", "danger"
	case "degraded":
		return "降级", "warn"
	default:
		if enabled {
			return "启用", "success"
		}
		return "停用", "neutral"
	}
}

// humanBytes 给文件清单显示一个紧凑大小（插件详情页）。
func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "—"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// pluginDepthClass 把目录深度映射到固定的缩进 class（CSP 不允许内联 style）。
func pluginDepthClass(depth int) string {
	if depth < 0 {
		depth = 0
	}
	if depth > 4 {
		depth = 4
	}
	return fmt.Sprintf("ada-plugin-file--d%d", depth)
}
