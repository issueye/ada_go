package canvas

import (
	"errors"
	"net/http"
	"testing"
)

// 画布版式（比例/尺寸）：归一化、校验、持久化与 HTTP 契约。

func TestCanvasPresetsValid(t *testing.T) {
	presets := CanvasPresets()
	if len(presets) < 6 {
		t.Fatalf("比例预设过少: %d", len(presets))
	}
	seen := map[string]bool{}
	for _, p := range presets {
		if p.ID == "" || p.Name == "" {
			t.Errorf("预设缺少 id/name: %+v", p)
		}
		if seen[p.ID] {
			t.Errorf("预设 ID 重复: %s", p.ID)
		}
		seen[p.ID] = true
		if p.Width < MinCanvasSide || p.Height < MinCanvasSide ||
			p.Width > MaxCanvasSide || p.Height > MaxCanvasSide {
			t.Errorf("预设 %s 尺寸越界: %dx%d", p.ID, p.Width, p.Height)
		}
	}
	// 返回副本，改它不能污染全局表
	presets[0].Width = 1
	if got, _ := GetCanvasPreset(presets[0].ID); got.Width == 1 {
		t.Fatal("CanvasPresets 应返回副本")
	}
	if _, ok := GetCanvasPreset(" 16:9 "); !ok {
		t.Fatal("GetCanvasPreset 应忽略空白")
	}
}

func TestNormalizeCanvas(t *testing.T) {
	cases := []struct {
		name    string
		in      *ProjectCanvas
		wantW   int
		wantH   int
		wantRat string
		wantNil bool
		wantErr error
	}{
		{name: "nil 即自由画布", in: nil, wantNil: true},
		{name: "空配置即自由画布", in: &ProjectCanvas{}, wantNil: true},
		{name: "按预设展开", in: &ProjectCanvas{Ratio: "16:9"}, wantW: 1920, wantH: 1080, wantRat: "16:9"},
		{name: "竖屏预设", in: &ProjectCanvas{Ratio: "9:16"}, wantW: 1080, wantH: 1920, wantRat: "9:16"},
		{name: "自定义尺寸", in: &ProjectCanvas{Width: 1000, Height: 700}, wantW: 1000, wantH: 700, wantRat: "custom"},
		{name: "尺寸命中预设自动归一", in: &ProjectCanvas{Width: 1920, Height: 1080}, wantW: 1920, wantH: 1080, wantRat: "16:9"},
		{name: "比例与尺寸冲突时以尺寸为准", in: &ProjectCanvas{Ratio: "16:9", Width: 800, Height: 600}, wantW: 800, wantH: 600, wantRat: "custom"},
		{name: "未知比例", in: &ProjectCanvas{Ratio: "21:9"}, wantErr: ErrUnknownCanvasRatio},
		{name: "只给宽", in: &ProjectCanvas{Width: 800}, wantErr: ErrCanvasSizeInvalid},
		{name: "只给高", in: &ProjectCanvas{Height: 600}, wantErr: ErrCanvasSizeInvalid},
		{name: "过小", in: &ProjectCanvas{Width: 100, Height: 100}, wantErr: ErrCanvasSizeInvalid},
		{name: "过大", in: &ProjectCanvas{Width: 9000, Height: 600}, wantErr: ErrCanvasSizeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeCanvas(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, 期望 %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("期望 nil（自由画布），得到 %+v", got)
				}
				return
			}
			if got == nil || got.Width != tc.wantW || got.Height != tc.wantH || got.Ratio != tc.wantRat {
				t.Fatalf("got %+v, 期望 %dx%d %s", got, tc.wantW, tc.wantH, tc.wantRat)
			}
		})
	}
}

func TestSetProjectCanvasPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AddProject("移动端改版", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Canvas != nil {
		t.Fatal("新项目默认应为自由画布")
	}

	updated, err := s.SetProjectCanvas(p.ID, &ProjectCanvas{Ratio: "9:16"})
	if err != nil {
		t.Fatalf("SetProjectCanvas: %v", err)
	}
	if updated.Canvas == nil || updated.Canvas.Width != 1080 || updated.Canvas.Ratio != "9:16" {
		t.Fatalf("画布配置异常: %+v", updated.Canvas)
	}
	if updated.UpdatedAt.Before(p.UpdatedAt) {
		t.Fatal("设置画布应刷新 updated_at")
	}

	// 重开验证持久化
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.GetProject(p.ID)
	if err != nil || got.Canvas == nil || got.Canvas.Height != 1920 {
		t.Fatalf("重开后画布配置丢失: %+v (%v)", got.Canvas, err)
	}

	// Clone 必须深拷贝：改副本不能影响存储
	got.Canvas.Width = 1
	if stored, _ := s2.GetProject(p.ID); stored.Canvas.Width == 1 {
		t.Fatal("Clone 应深拷贝 Canvas")
	}

	// 清空回自由画布
	cleared, err := s2.SetProjectCanvas(p.ID, nil)
	if err != nil || cleared.Canvas != nil {
		t.Fatalf("清空画布失败: %+v (%v)", cleared.Canvas, err)
	}

	// 错误路径
	if _, err := s2.SetProjectCanvas("p-nope", &ProjectCanvas{Ratio: "16:9"}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("未知项目应 ErrProjectNotFound，得到 %v", err)
	}
	if _, err := s2.SetProjectCanvas(p.ID, &ProjectCanvas{Ratio: "21:9"}); !errors.Is(err, ErrUnknownCanvasRatio) {
		t.Fatalf("未知比例应 ErrUnknownCanvasRatio，得到 %v", err)
	}
}

func TestCanvasPresetsAPI(t *testing.T) {
	_, ts := newTestServer(t)
	data := res0(t, ts, "GET", "/api/canvas/presets", nil)
	presets, ok := data["presets"].([]any)
	if !ok || len(presets) == 0 {
		t.Fatalf("presets 响应异常: %v", data)
	}
	if data["min_side"].(float64) != MinCanvasSide || data["max_side"].(float64) != MaxCanvasSide {
		t.Fatalf("尺寸区间异常: %v", data)
	}
}

func TestSetProjectCanvasAPI(t *testing.T) {
	_, ts := newTestServer(t)
	pid := newHTTPProject(t, ts)

	// 按比例设置
	res, data := doJSON(t, "PUT", ts.URL+"/api/projects/"+pid+"/canvas", map[string]any{"ratio": "16:9"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("设置比例 status = %d: %v", res.StatusCode, data)
	}
	proj := data["project"].(map[string]any)
	cv := proj["canvas"].(map[string]any)
	if cv["width"] != float64(1920) || cv["height"] != float64(1080) || cv["ratio"] != "16:9" {
		t.Fatalf("画布回包异常: %v", cv)
	}

	// 自定义尺寸
	res, data = doJSON(t, "PUT", ts.URL+"/api/projects/"+pid+"/canvas", map[string]any{"width": 1280, "height": 800})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("自定义尺寸 status = %d: %v", res.StatusCode, data)
	}
	cv = data["project"].(map[string]any)["canvas"].(map[string]any)
	if cv["width"] != float64(1280) || cv["ratio"] != "custom" {
		t.Fatalf("自定义画布异常: %v", cv)
	}

	// 项目列表里也能读到（画布随项目走）
	projects := res0(t, ts, "GET", "/api/projects", nil)["projects"].([]any)
	first := projects[0].(map[string]any)
	if first["canvas"].(map[string]any)["height"] != float64(800) {
		t.Fatalf("项目列表画布异常: %v", first["canvas"])
	}

	// free 清除边界
	res, data = doJSON(t, "PUT", ts.URL+"/api/projects/"+pid+"/canvas", map[string]any{"free": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("free status = %d: %v", res.StatusCode, data)
	}
	if _, has := data["project"].(map[string]any)["canvas"]; has {
		t.Fatalf("free 后不应再有 canvas: %v", data)
	}

	// 未知比例
	res, data = doJSON(t, "PUT", ts.URL+"/api/projects/"+pid+"/canvas", map[string]any{"ratio": "21:9"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("未知比例应 400, got %d", res.StatusCode)
	}
	if errObj := data["error"].(map[string]any); errObj["code"] != "unknown_ratio" {
		t.Fatalf("错误码 = %v", errObj["code"])
	}

	// 非法尺寸
	res, _ = doJSON(t, "PUT", ts.URL+"/api/projects/"+pid+"/canvas", map[string]any{"width": 10, "height": 10})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法尺寸应 400, got %d", res.StatusCode)
	}

	// 未知项目
	res, _ = doJSON(t, "PUT", ts.URL+"/api/projects/p-nope/canvas", map[string]any{"ratio": "16:9"})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("未知项目应 404, got %d", res.StatusCode)
	}
}
