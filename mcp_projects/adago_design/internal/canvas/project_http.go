package canvas

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// 项目的 HTTP 面 + 稳定的项目类错误码映射。
//
// 错误码是契约的一部分（MCP 工具会原样转给模型）：
//   project_required   —— 没带项目就新增设计稿
//   unknown_project    —— 项目 ID 不存在
//   project_duplicate  —— 同名项目已存在
//   project_not_empty  —— 项目下还有设计稿，拒绝删除
//   edge_cross_project —— 连线两端不在同一项目

// projectErr 把 Store 的哨兵错误翻译成带上稳定 code 的 API 错误。
func projectErr(err error, id string) *Error {
	switch {
	case errors.Is(err, ErrProjectRequired):
		return APIError("project_required", "新增设计稿必须指定项目",
			"先用 project_create 建项目（或 project_list 选一个已有项目），再添加设计稿")
	case errors.Is(err, ErrUnknownProject):
		return APIError("unknown_project", "未知项目 "+id, "用 project_list 查看已有项目")
	case errors.Is(err, ErrProjectNotFound):
		return APIError("unknown_project", "项目 "+id+" 不存在", "用 project_list 查看已有项目")
	case errors.Is(err, ErrProjectNameEmpty):
		return APIError("bad_request", "项目名不能为空", "给项目一个可读的名字，如「官网改版」")
	case errors.Is(err, ErrProjectDuplicate):
		return APIError("project_duplicate", "同名项目已存在", "换一个名称，或直接使用已有项目")
	case errors.Is(err, ErrProjectNotEmpty):
		return APIError("project_not_empty", "项目下仍有设计稿，拒绝删除",
			"先 canvas_clear 清空该项目（会保留一份内存快照），再删除项目")
	case errors.Is(err, ErrEdgeCrossProject):
		return APIError("edge_cross_project", "连线两端必须属于同一项目",
			"跨项目表达流向请改用分组框或便签")
	case errors.Is(err, ErrUnknownCanvasRatio):
		return APIError("unknown_ratio", "未知画布比例预设", "可用比例："+strings.Join(CanvasPresetIDs(), " / "))
	case errors.Is(err, ErrCanvasSizeInvalid):
		return APIError("bad_request", "画布尺寸非法",
			fmt.Sprintf("宽高需同时给出，且在 %d..%d 之间", MinCanvasSide, MaxCanvasSide))
	case errors.Is(err, ErrArtboardResolver):
		return APIError("artboard_unavailable", "当前进程没有画板解析器",
			"这是装配层未注入导致的环境问题（正常 serve/mcp 启动不会出现）")
	}
	return APIError("internal", err.Error(), "")
}

// statusForProjectErr 给出对应的 HTTP 状态码。
func statusForProjectErr(err error) int {
	switch {
	case errors.Is(err, ErrProjectRequired),
		errors.Is(err, ErrProjectNameEmpty),
		errors.Is(err, ErrEdgeCrossProject),
		errors.Is(err, ErrUnknownCanvasRatio),
		errors.Is(err, ErrCanvasSizeInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrUnknownProject), errors.Is(err, ErrProjectNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrProjectDuplicate), errors.Is(err, ErrProjectNotEmpty):
		return http.StatusConflict
	case errors.Is(err, ErrArtboardResolver):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func (s *Server) handleListProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"projects": s.store.ListProjects()})
}

type projectIn struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

func (s *Server) handleAddProject(w http.ResponseWriter, r *http.Request) {
	var in projectIn
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := s.store.AddProject(in.Name, in.Note)
	if err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, ""))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": p})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, p := range s.store.ListProjects() {
		if p.ID == id {
			writeJSON(w, http.StatusOK, map[string]any{"project": p})
			return
		}
	}
	writeErr(w, http.StatusNotFound, projectErr(ErrProjectNotFound, id))
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in projectIn
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := s.store.UpdateProject(id, in.Name, in.Note)
	if err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": p})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.store.RemoveProject(id); err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": id})
}

// ---------------- 画布版式（比例 / 尺寸） ----------------

// handleCanvasPresets 返回比例预设表与合法尺寸区间（UI 直接渲染成菜单）。
func (s *Server) handleCanvasPresets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"presets":  CanvasPresets(),
		"min_side": MinCanvasSide,
		"max_side": MaxCanvasSide,
	})
}

type canvasConfigIn struct {
	Ratio  string `json:"ratio"`  // 预设 ID；与 width/height 二选一
	Width  int    `json:"width"`  // 自定义宽（px）
	Height int    `json:"height"` // 自定义高（px）
	Free   bool   `json:"free"`   // true = 恢复自由画布（无边界）
}

// handleSetProjectCanvas 设置当前画布的比例/尺寸；free=true 清除边界。
func (s *Server) handleSetProjectCanvas(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var in canvasConfigIn
	if !decodeBody(w, r, &in) {
		return
	}
	var cfg *ProjectCanvas
	if !in.Free {
		cfg = &ProjectCanvas{Ratio: in.Ratio, Width: in.Width, Height: in.Height}
	}
	p, err := s.store.SetProjectCanvas(id, cfg)
	if err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": p, "canvas": p.Canvas})
}

// ---------------- 画板尺寸显式重同步 ----------------

type resyncArtboardsIn struct {
	Project string `json:"project,omitempty"` // 空 = 全部项目
}

// handleResyncArtboards 按节点自身的 artboard 重算宽高（显式触发，可撤销）。
// 画板/比例切换本身不会自动迁移尺寸，见 artboard_resync.go 的设计说明。
func (s *Server) handleResyncArtboards(w http.ResponseWriter, r *http.Request) {
	var in resyncArtboardsIn
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := s.store.ResyncArtboards(in.Project)
	if err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, in.Project))
		return
	}
	writeJSON(w, http.StatusOK, res)
}
