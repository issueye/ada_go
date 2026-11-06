package canvas

import (
	"fmt"
	"strings"
	"time"
)

// 画布版式：项目作用域的可视边界（宽高 + 比例标签）。
//
// 设计取舍：
//   - 画布是「项目」的属性，不是独立实体 —— 一个项目本来就对应一块画布作用域，
//     再引入一层 Canvas 会让 project / canvas 两套 ID 互相指代，Agent 侧也更难用。
//   - 只存宽高与比例标签，不存缩放/平移：视口状态属于浏览器本地（localStorage），
//     存到服务端会让两个窗口打开同一画布时互相抢镜头。
//   - 比例只是宽高的可读标签，真正生效的是 Width×Height。预设表在 Go 侧单一来源，
//     UI 通过 GET /api/canvas/presets 拉取，避免前后端各写一份而漂移。
//   - 画布边界只作版式参考，不限制节点拖拽范围：无限画布的价值就在于能摆到边界外
//     做草稿区，硬裁剪反而会把稿子弄丢。
const (
	// MinCanvasSide / MaxCanvasSide 是画布宽高的合法区间。
	MinCanvasSide = 320
	MaxCanvasSide = 8192
)

// ProjectCanvas 是项目（画布作用域）的版式配置。
type ProjectCanvas struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Ratio  string `json:"ratio,omitempty"` // 预设 ID（16:9 …）或 custom
}

// Caption 返回「1920×1080 · 16:9」这类人读描述。
func (c ProjectCanvas) Caption() string {
	ratio := c.Ratio
	if ratio == "" {
		ratio = "custom"
	}
	return fmt.Sprintf("%d×%d · %s", c.Width, c.Height, ratio)
}

// CanvasPreset 是画布比例预设。
type CanvasPreset struct {
	ID     string `json:"id"`   // "16:9"
	Name   string `json:"name"` // 「宽屏 16:9」
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// canvasPresets 是唯一一份比例预设表（UI 与 MCP 都从这里取）。
var canvasPresets = []CanvasPreset{
	{ID: "16:9", Name: "宽屏 16:9", Width: 1920, Height: 1080},
	{ID: "16:10", Name: "桌面 16:10", Width: 1920, Height: 1200},
	{ID: "4:3", Name: "标准 4:3", Width: 1440, Height: 1080},
	{ID: "3:2", Name: "相机 3:2", Width: 1800, Height: 1200},
	{ID: "1:1", Name: "方形 1:1", Width: 1440, Height: 1440},
	{ID: "3:4", Name: "竖版 3:4", Width: 1080, Height: 1440},
	{ID: "2:3", Name: "竖版 2:3", Width: 1200, Height: 1800},
	{ID: "9:16", Name: "竖屏 9:16", Width: 1080, Height: 1920},
}

// CanvasPresets 返回预设副本（调用方可安全修改）。
func CanvasPresets() []CanvasPreset {
	out := make([]CanvasPreset, len(canvasPresets))
	copy(out, canvasPresets)
	return out
}

// GetCanvasPreset 按 ID 查预设（大小写不敏感、去空白）。
func GetCanvasPreset(id string) (CanvasPreset, bool) {
	want := strings.TrimSpace(id)
	for _, p := range canvasPresets {
		if strings.EqualFold(p.ID, want) {
			return p, true
		}
	}
	return CanvasPreset{}, false
}

// CanvasPresetIDs 返回全部预设 ID（用于错误提示）。
func CanvasPresetIDs() []string {
	out := make([]string, 0, len(canvasPresets))
	for _, p := range canvasPresets {
		out = append(out, p.ID)
	}
	return out
}

// presetBySize 反查尺寸对应的预设（显式宽高恰好等于预设时，标签自动归一）。
func presetBySize(w, h int) (CanvasPreset, bool) {
	for _, p := range canvasPresets {
		if p.Width == w && p.Height == h {
			return p, true
		}
	}
	return CanvasPreset{}, false
}

// NormalizeCanvas 归一化并校验画布配置。
//
//   - in 为 nil，或宽高与比例都为空 → (nil, nil)：表示「自由画布」。
//   - 只给比例 → 按预设展开宽高。
//   - 只给宽高 → 恰好等于某预设时用该预设 ID，否则记 custom。
//   - 比例与宽高同时给 → 宽高优先，标签按实际尺寸归一。
func NormalizeCanvas(in *ProjectCanvas) (*ProjectCanvas, error) {
	if in == nil {
		return nil, nil
	}
	w, h := in.Width, in.Height
	label := strings.TrimSpace(in.Ratio)

	if label != "" {
		if p, ok := GetCanvasPreset(label); ok {
			if w <= 0 && h <= 0 {
				w, h = p.Width, p.Height
			}
		} else if !strings.EqualFold(label, "custom") {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCanvasRatio, label)
		}
	}
	if w <= 0 && h <= 0 {
		return nil, nil // 自由画布
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: 宽高必须同时给出（%d×%d）", ErrCanvasSizeInvalid, w, h)
	}
	if w < MinCanvasSide || h < MinCanvasSide || w > MaxCanvasSide || h > MaxCanvasSide {
		return nil, fmt.Errorf("%w: %d×%d 超出 %d..%d", ErrCanvasSizeInvalid, w, h, MinCanvasSide, MaxCanvasSide)
	}
	// 标签永远由最终尺寸推导：显式宽高与传入的比例不一致时不能沿用旧标签
	if preset, ok := presetBySize(w, h); ok {
		label = preset.ID
	} else {
		label = "custom"
	}
	return &ProjectCanvas{Width: w, Height: h, Ratio: label}, nil
}

// SetProjectCanvas 设置/清空某项目的画布版式（nil = 自由画布）。
// 与节点写入一样，校验放在 Store：HTTP 与 MCP 两条入口共用。
func (s *Store) SetProjectCanvas(id string, in *ProjectCanvas) (Project, error) {
	cfg, err := NormalizeCanvas(in)
	if err != nil {
		return Project{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	p.Canvas = cfg
	p.UpdatedAt = time.Now()
	s.persistLocked()
	return p.Clone(), nil
}
