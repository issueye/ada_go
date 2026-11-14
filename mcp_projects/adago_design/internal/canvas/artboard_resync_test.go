package canvas

import (
	"net/http"
	"testing"
)

// stubArtboards 是测试用的画板尺寸表（canvas 包不引用 design）。
func stubArtboards() func(string, string) (int, int, bool) {
	sizes := map[string][2]int{
		"phone": {393, 852},
		"web":   {1440, 900},
	}
	return func(id, kind string) (int, int, bool) {
		s, ok := sizes[id]
		if !ok {
			return 0, 0, false
		}
		if kind == KindScreen {
			return s[0], s[1] - 100, true // 模拟 BodyHeight < Height
		}
		return s[0], s[1], true
	}
}

func TestResyncArtboards(t *testing.T) {
	s, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.ArtboardSize = stubArtboards()
	pid := newTestProject(t, s)

	comp := addNode(t, s, pid, Node{
		Title: "组件", Kind: KindComponent, Artboard: "phone",
		Width: 420, Height: 320, HTML: "<html><body>x</body></html>",
	})
	screen := addNode(t, s, pid, Node{
		Title: "整页", Kind: KindScreen, Artboard: "web",
		Width: 420, Height: 320, HTML: "<html><body>y</body></html>",
	})
	note := addNode(t, s, pid, newTestNode("便签"))

	res, err := s.ResyncArtboards(pid)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if res.Total != 3 || res.Changed != 2 || res.Skipped != 1 {
		t.Fatalf("统计异常: %+v", res)
	}

	// 组件只纠正宽度（高度由客户端实测）
	gotComp, _ := s.Get(comp.ID)
	if gotComp.Width != 393 || gotComp.Height != 320 {
		t.Fatalf("组件尺寸 = %dx%d，期望 393x320", gotComp.Width, gotComp.Height)
	}
	// 整页同时纠正宽高，高度取 BodyHeight（stub: 900-100）
	gotScreen, _ := s.Get(screen.ID)
	if gotScreen.Width != 1440 || gotScreen.Height != 800 {
		t.Fatalf("整页尺寸 = %dx%d，期望 1440x800", gotScreen.Width, gotScreen.Height)
	}
	// 无画板节点不动
	gotNote, _ := s.Get(note.ID)
	if gotNote.Width != 420 || gotNote.Height != 320 {
		t.Fatalf("便签被改动: %+v", gotNote)
	}

	// 再跑一次应无变化（幂等）
	res2, err := s.ResyncArtboards(pid)
	if err != nil || res2.Changed != 0 {
		t.Fatalf("第二次 resync 应无变化: %+v (%v)", res2, err)
	}
}

func TestResyncArtboardsUndoRedo(t *testing.T) {
	s, _ := OpenStore("")
	s.ArtboardSize = stubArtboards()
	pid := newTestProject(t, s)
	n := addNode(t, s, pid, Node{
		Title: "组件", Kind: KindComponent, Artboard: "phone",
		Width: 420, Height: 320, HTML: "<html><body>x</body></html>",
	})

	if _, err := s.ResyncArtboards(pid); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if got, _ := s.Get(n.ID); got.Width != 393 {
		t.Fatalf("resync 未生效: %d", got.Width)
	}

	desc, ok := s.Undo()
	if !ok {
		t.Fatal("应有可撤销记录")
	}
	if got, _ := s.Get(n.ID); got.Width != 420 {
		t.Fatalf("撤销后宽度 = %d，期望 420（desc=%s）", got.Width, desc)
	}
	if _, ok := s.Redo(); !ok {
		t.Fatal("应有可重做记录")
	}
	if got, _ := s.Get(n.ID); got.Width != 393 {
		t.Fatalf("重做后宽度 = %d，期望 393", got.Width)
	}
}

func TestResyncArtboardsScopeAndUnknown(t *testing.T) {
	s, _ := OpenStore("")
	s.ArtboardSize = stubArtboards()
	p1 := newTestProject(t, s)
	p2, err := s.AddProject("另一个项目", "")
	if err != nil {
		t.Fatal(err)
	}
	a := addNode(t, s, p1, Node{Title: "a", Kind: KindComponent, Artboard: "phone", Width: 420, Height: 320, HTML: "<p>a</p>"})
	b := addNode(t, s, p2.ID, Node{Title: "b", Kind: KindComponent, Artboard: "phone", Width: 420, Height: 320, HTML: "<p>b</p>"})
	unknown := addNode(t, s, p1, Node{Title: "c", Kind: KindComponent, Artboard: "ghost", Width: 300, Height: 200, HTML: "<p>c</p>"})

	res, err := s.ResyncArtboards(p1)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if res.Changed != 1 || len(res.UnknownArtboards) != 1 || res.UnknownArtboards[0] != "ghost" {
		t.Fatalf("结果异常: %+v", res)
	}
	if got, _ := s.Get(a.ID); got.Width != 393 {
		t.Fatalf("项目内节点未调整: %d", got.Width)
	}
	if got, _ := s.Get(b.ID); got.Width != 420 {
		t.Fatalf("其它项目节点被误改: %d", got.Width)
	}
	if got, _ := s.Get(unknown.ID); got.Width != 300 {
		t.Fatalf("未知画板节点被误改: %d", got.Width)
	}
}

func TestResyncArtboardsRequiresResolver(t *testing.T) {
	s, _ := OpenStore("")
	if _, err := s.ResyncArtboards(""); err != ErrArtboardResolver {
		t.Fatalf("缺少解析器时应返回 ErrArtboardResolver，got %v", err)
	}
}

func TestResyncArtboardsAPI(t *testing.T) {
	srv, ts := newTestServer(t)
	srv.store.ArtboardSize = stubArtboards()
	pid := newTestProject(t, srv.store)
	addNode(t, srv.store, pid, Node{
		Title: "组件", Kind: KindComponent, Artboard: "phone",
		Width: 420, Height: 320, HTML: "<p>x</p>",
	})

	res, data := doJSON(t, "POST", ts.URL+"/api/artboards/resync", map[string]any{"project": pid})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", res.StatusCode, data)
	}
	if data["changed"].(float64) != 1 || data["total"].(float64) != 1 {
		t.Fatalf("回包异常: %v", data)
	}

	// 未知项目 -> 404 + unknown_project
	res, data = doJSON(t, "POST", ts.URL+"/api/artboards/resync", map[string]any{"project": "p-nope"})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("未知项目 status = %d", res.StatusCode)
	}
	if errObj, _ := data["error"].(map[string]any); errObj["code"] != "unknown_project" {
		t.Fatalf("错误码 = %v", data["error"])
	}
}
