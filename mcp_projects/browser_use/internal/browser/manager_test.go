package browser

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"browseruse/internal/config"
)

// TestReserveSlotClosesMaxTabsRace 钉住 T1.1:并发 NewTab 曾在「检查后放锁再建
// WebView」的窗口里全部通过容量检查,最终超出 MaxTabs。槽位必须锁内预留,
// 并发预留的成功数恰好等于上限。
func TestReserveSlotClosesMaxTabsRace(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.MaxTabs = 2
	m := &Manager{cfg: cfg, tabs: map[string]*Tab{}, stop: make(chan struct{})}

	const calls = 16
	var ok atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if m.reserveSlot() == nil {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := ok.Load(); got != int32(cfg.Agent.MaxTabs) {
		t.Fatalf("预留成功的槽位 = %d, 期望恰好 %d(MaxTabs)", got, cfg.Agent.MaxTabs)
	}
	// 每个成功预留都对应一次归还:全部归还后计数归零。
	for i := 0; i < int(ok.Load()); i++ {
		m.releaseSlot()
	}
	if m.pending != 0 {
		t.Fatalf("全部归还后 pending = %d, 期望 0", m.pending)
	}
}

// TestExpireSkipsBusyTab 钉住 T1.2 的 expireLoop 一侧:动作占用槽位期间
// expire 不得清租约(否则遮罩消失、输入解锁,而动作还在跑);空闲过期
// 照常清除。
func TestExpireSkipsBusyTab(t *testing.T) {
	tab := &Tab{ID: "t1"}
	tab.acquire("agent-1", -time.Second) // 已经过期
	tab.actionBusy.Store(true)
	if tab.expire(time.Now()) {
		t.Fatal("动作执行中 expireLoop 不得清租约")
	}
	if tab.leaseUntil.IsZero() {
		t.Fatal("busy 期间租约字段应保持,等心跳续期")
	}
	tab.actionBusy.Store(false)
	if !tab.expire(time.Now()) {
		t.Fatal("空闲且过期的租约应被正常清除")
	}
	if !tab.leaseUntil.IsZero() || tab.leaseClient != "" {
		t.Fatalf("过期租约应被清空: until=%v client=%q", tab.leaseUntil, tab.leaseClient)
	}
}

// TestLeaseHeartbeatKeepsLeaseDuringAction 钉住 T1.2 的心跳一侧:长动作期间
// 租约被持续续期不失效;人类接管(releaseLease)后心跳退出,不得复活租约。
func TestLeaseHeartbeatKeepsLeaseDuringAction(t *testing.T) {
	tab := &Tab{ID: "t1"}
	const d = 120 * time.Millisecond
	tab.acquire("agent-1", d)
	stop := make(chan struct{})
	defer close(stop)
	go tab.leaseHeartbeat("agent-1", 20*time.Millisecond, d, stop)

	time.Sleep(5 * d) // 没有心跳的话,租约早该过期了
	if !tab.HasLease() {
		t.Fatal("动作执行中租约被过期释放")
	}

	tab.releaseLease("")
	time.Sleep(4 * d)
	if tab.HasLease() {
		t.Fatal("接管清租约后,心跳不得把租约续回来")
	}
}

// TestHeartbeatInterval 钉住续租频率:默认 120s 空闲窗 → 30s 心跳(计划 T1.2);
// 极短空闲窗至少 1s,超长窗封顶 30s。注意 Manager 持有 cfg 的值拷贝,改配置要
// 改 m.cfg 这份。
func TestHeartbeatInterval(t *testing.T) {
	m := &Manager{cfg: config.Default()}
	if got := m.heartbeatInterval(); got != 30*time.Second {
		t.Fatalf("默认心跳间隔 = %v, 期望 30s", got)
	}
	m.cfg.Control.IdleReleaseMS = 2000
	if got := m.heartbeatInterval(); got != time.Second {
		t.Fatalf("2s 空闲窗的心跳间隔 = %v, 期望下限 1s", got)
	}
	m.cfg.Control.IdleReleaseMS = 100
	if got := m.heartbeatInterval(); got != time.Second {
		t.Fatalf("100ms 空闲窗的心跳间隔 = %v, 期望下限 1s", got)
	}
	m.cfg.Control.IdleReleaseMS = 10 * 60 * 1000
	if got := m.heartbeatInterval(); got != 30*time.Second {
		t.Fatalf("10min 空闲窗的心跳间隔 = %v, 期望上限 30s", got)
	}
}

// TestAllowScheme 钉住收敛后的两份导航策略:agent(goto)比页面(链接/重定向)
// 更严,页面自己产生的 blob: 导航合法,agent 不得 goto。两份名单由一个函数
// 产生,不再各自演化(A4)。
func TestAllowScheme(t *testing.T) {
	cases := []struct {
		scheme    string
		fromAgent bool
		want      bool
	}{
		{"http", true, true}, {"https", true, true},
		{"HTTP", false, true},
		{"about", true, true}, {"about", false, true},
		{"blob", false, true},
		{"blob", true, false},
		{"javascript", true, false}, {"javascript", false, false},
		{"data", true, false}, {"data", false, false},
		{"file", true, false}, {"file", false, false},
		{"vbscript", false, false},
		{"chrome-extension", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		if got := allowScheme(c.scheme, c.fromAgent); got != c.want {
			t.Errorf("allowScheme(%q, agent=%v) = %v, want %v", c.scheme, c.fromAgent, got, c.want)
		}
	}
}

// TestInvokeOnClosingTab 钉住 T1.3:关闭中的 tab 返回语义化 tab_closed,而不是
// destroy 后 eval 失败的 internal(无法归因,agent 无法自纠)。
func TestInvokeOnClosingTab(t *testing.T) {
	tab := &Tab{ID: "t1"}
	tab.closing.Store(true)
	_, err := tab.invoke(context.Background(), "snapshot", map[string]any{})
	var be *Error
	if !errors.As(err, &be) || be.Code != CodeTabClosed {
		t.Fatalf("err = %v, want %s", err, CodeTabClosed)
	}
}

// TestDecodeWireWarning 钉住 T1.7:动作回执解包失败不再静默丢成零值(旧实现
// _ = json.Unmarshal 让点击回执看起来像成功空操作),而是挂 warning;
// 正常回执不产生 warning。注意 encoding/json 对「字符串进指针字段」会先分配
// 指针再报错,所以畸形回执可能留下零值节点 —— warning 是判断依据。
func TestDecodeWireWarning(t *testing.T) {
	r := &Receipt{}
	var w clickWire
	decodeWire(r, json.RawMessage(`{"clicked":"not-an-object"}`), &w)
	if len(r.Warnings) == 0 {
		t.Fatal("解包失败应产生 warning")
	}

	r2 := &Receipt{}
	decodeWire(r2, json.RawMessage(`{"clicked":{"ref":"e1","role":"button","name":"OK"}}`), &w)
	if len(r2.Warnings) != 0 {
		t.Fatalf("正常解包不应有 warning: %v", r2.Warnings)
	}
	if w.Clicked == nil || w.Clicked.Ref != "e1" {
		t.Fatalf("clicked 解包错误: %+v", w.Clicked)
	}
}
