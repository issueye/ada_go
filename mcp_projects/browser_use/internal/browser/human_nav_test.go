package browser

import (
	"context"
	"errors"
	"testing"

	"browseruse/internal/config"
)

// TestHumanNavigateDoesNotTakeLease 钉住 UI 导航的边界：地址栏/前进/刷新不能在
// 页面上留下 agent 租约（否则遮罩会盖住页面并锁输入 120s）。
//
// Tab.Page 故意置为 nil：如果实现又走回 begin()，ensureRuntime 会对 nil Page
// 解引用并 panic，测试会直接暴露回归；Human 路径应先返回 tab_closed。
func TestHumanNavigateDoesNotTakeLease(t *testing.T) {
	cfg := config.Default()
	m := &Manager{
		cfg:    cfg,
		tabs:   map[string]*Tab{"t1": {ID: "t1", Owner: OwnerUser}},
		order:  []string{"t1"},
		active: "t1",
		stop:   make(chan struct{}),
	}

	_, err := m.Navigate(context.Background(), "t1", "ui", "stop", "", NavigateOptions{
		WaitUntil: "none",
		Human:     true,
	})
	if err == nil {
		t.Fatal("expected tab_closed for a tab without a page")
	}
	var be *Error
	if !errors.As(err, &be) || be.Code != CodeTabClosed {
		t.Fatalf("err = %v, want %s", err, CodeTabClosed)
	}
	if tab := m.Tab("t1"); tab == nil || tab.HasLease() {
		t.Fatalf("human navigation must not acquire a lease: %+v", tab)
	}
	if st := m.State(); st.State != "idle" || st.Masked {
		t.Fatalf("control state = %+v, want idle/unmasked", st)
	}
}

// TestHumanNavigateWithoutTab 验证无活动标签页时，人类路径不会隐式创建标签页，
// 而是明确返回 no_tab（创建标签页是 NewTab 的职责，不应藏在 back/forward 里）。
func TestHumanNavigateWithoutTab(t *testing.T) {
	cfg := config.Default()
	m := &Manager{cfg: cfg, tabs: map[string]*Tab{}, stop: make(chan struct{})}

	_, err := m.Navigate(context.Background(), "", "ui", "back", "", NavigateOptions{
		WaitUntil: "none",
		Human:     true,
	})
	var be *Error
	if !errors.As(err, &be) || be.Code != CodeNoTab {
		t.Fatalf("err = %v, want %s", err, CodeNoTab)
	}
}

// TestPauseState 钉住 Pause/Resume 的控制状态：Pause 不把浏览器标成
// user-controlled，但 State 必须是 paused 且不显示遮罩；Resume 回到 idle。
func TestPauseState(t *testing.T) {
	cfg := config.Default()
	m := &Manager{
		cfg:    cfg,
		tabs:   map[string]*Tab{"t1": {ID: "t1", Owner: OwnerUser}},
		order:  []string{"t1"},
		active: "t1",
		stop:   make(chan struct{}),
	}
	if st := m.Pause(); st.State != "paused" || st.Masked || !st.Paused {
		t.Fatalf("pause state = %+v", st)
	}
	if m.Masked("t1") {
		t.Fatal("paused manager must not show the mask")
	}
	if st := m.Resume(); st.State != "idle" || st.Paused {
		t.Fatalf("resume state = %+v", st)
	}
}
