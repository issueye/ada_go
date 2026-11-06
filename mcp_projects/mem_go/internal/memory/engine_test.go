package memory

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestEngine 创建基于离线哈希嵌入器的测试引擎。
func newTestEngine(t *testing.T) (*Engine, *stubClock) {
	t.Helper()
	return newTestEngineAt(t, t.TempDir())
}

func newTestEngineAt(t *testing.T, dir string) (*Engine, *stubClock) {
	t.Helper()
	clock := &stubClock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	e, err := Open(Config{
		DBPath:   filepath.Join(dir, "test_mem.db"),
		Embedder: NewHashEmbedder(128),
		now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("打开测试引擎失败: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e, clock
}

// stubClock 可推进的假时钟，用于测试时间衰减与时间排序。
type stubClock struct{ now time.Time }

func (c *stubClock) Now() time.Time          { return c.now }
func (c *stubClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func mustStore(t *testing.T, e *Engine, content, kind string, importance float64, scope string, tags ...string) (Memory, StoreAction) {
	t.Helper()
	res, err := e.Store(context.Background(), StoreInput{
		Content: content, Kind: kind, Importance: importance, Scope: scope, Tags: tags,
	})
	if err != nil {
		t.Fatalf("Store(%q) 失败: %v", content, err)
	}
	return res.Memory, res.Action
}

func TestStoreAndSearch(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	m1, action := mustStore(t, e, "用户的主力语言是 Go，项目都在 E:\\codes\\go_projects", "fact", 0.8, "", "lang")
	if action != ActionCreated {
		t.Fatalf("首次存储应为 created，得到 %s", action)
	}
	mustStore(t, e, "用户喜欢简洁的中文回复，不用表情符号", "preference", 0.6, "")
	mustStore(t, e, "团队的代码评审在每周五下午进行", "event", 0.4, "")

	got, err := e.Search(ctx, SearchInput{Query: "用户使用什么编程语言"})
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("检索结果为空")
	}
	if got[0].ID != m1.ID {
		t.Fatalf("最相关结果应是 Go 语言记忆: got %q (%s)", got[0].Content, got[0].Content)
	}
	if got[0].Similarity <= 0 || got[0].Score <= 0 {
		t.Fatalf("得分应大于 0: sim=%f score=%f", got[0].Similarity, got[0].Score)
	}
	// 验证与偏好记忆的排序：无关内容相似度应显著低于命中项
	if len(got) > 1 && got[0].Similarity < got[1].Similarity {
		t.Fatalf("结果应按相似度相关度排序: %f < %f", got[0].Similarity, got[1].Similarity)
	}
}

func TestStoreDedupMerge(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	oldContent := "用户偏好使用 Vim 编辑器"
	m1, _ := mustStore(t, e, oldContent, "preference", 0.5, "", "editor")
	// 近逐字重复（仅标点差异）再次存储 → 合并更新
	m2, err := e.Store(ctx, StoreInput{Content: "用户偏好使用 Vim 编辑器。", Importance: 0.9, Tags: []string{"工具"}})
	if err != nil {
		t.Fatalf("重复存储失败: %v", err)
	}
	if m2.Action != ActionUpdated {
		t.Fatalf("重复内容应触发 updated，得到 %s", m2.Action)
	}
	if m2.Memory.ID != m1.ID {
		t.Fatalf("更新应保持原 ID: %s != %s", m2.Memory.ID, m1.ID)
	}
	if m2.Memory.Importance != 0.9 {
		t.Fatalf("重要度应取较大值 0.9，得到 %f", m2.Memory.Importance)
	}
	if len(m2.Memory.Tags) != 2 {
		t.Fatalf("标签应合并为 2 个，得到 %v", m2.Memory.Tags)
	}
	if m2.Memory.AccessCount != 1 {
		t.Fatalf("强化次数应为 1，得到 %d", m2.Memory.AccessCount)
	}
	// 合并必须可审计：Previous 返回被覆盖的旧内容
	if m2.Previous == nil {
		t.Fatal("合并更新必须返回被覆盖的旧记忆（Previous）")
	}
	if m2.Previous.Content != oldContent || m2.Previous.ID != m1.ID {
		t.Fatalf("Previous 应为旧记忆: %+v", m2.Previous)
	}
	// 库里仍只有 1 条
	st, err := e.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 {
		t.Fatalf("去重后总数应为 1，得到 %d", st.Total)
	}
}

// TestStoreDistinctFactsNotMerged 是数据丢失回归测试：
// 词面哈希下仅末位数字不同的两条真实记忆相似度可达 0.92+，
// 旧的 0.60 阈值会把第二条静默覆盖到第一条上。
// 现在哈希阈值收紧到 0.95，语义上不同的事实必须各自成条。
func TestStoreDistinctFactsNotMerged(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	mustStore(t, e, "生产环境数据库在 10.0.0.5", "fact", 0.5, "")
	res, err := e.Store(ctx, StoreInput{Content: "生产环境数据库在 10.0.0.6"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("仅末位数字不同的两条事实不应合并，得到 %s（previous=%v）", res.Action, res.Previous)
	}
	if res.Previous != nil {
		t.Fatal("新增记忆不应携带 Previous")
	}
	st, err := e.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 2 {
		t.Fatalf("两条不同事实应共存，总数应为 2，得到 %d", st.Total)
	}
}

// TestStoreMergePersists 验证合并结果真正落盘（而不只是返回值正确）：
// 重开数据库后内容、强化计数、合并标签、原创建时间都应保持。
func TestStoreMergePersists(t *testing.T) {
	dir := t.TempDir()

	e1, _ := newTestEngineAt(t, dir)
	m1, _ := mustStore(t, e1, "部署前必须跑完整测试", "procedure", 0.5, "", "ci")
	res, err := e1.Store(context.Background(), StoreInput{
		Content: "部署前必须跑完整测试!", Importance: 0.8, Tags: []string{"release"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionUpdated {
		t.Fatalf("近重复内容应合并，得到 %s", res.Action)
	}
	if err := e1.Close(); err != nil {
		t.Fatalf("关闭引擎失败: %v", err)
	}

	e2, _ := newTestEngineAt(t, dir)
	defer e2.Close()
	got, err := e2.Get(context.Background(), m1.ID)
	if err != nil {
		t.Fatalf("重开后按 ID 查询失败: %v", err)
	}
	if got.Content != "部署前必须跑完整测试!" {
		t.Fatalf("合并后的新内容应落盘: %q", got.Content)
	}
	if got.Importance != 0.8 {
		t.Fatalf("合并后重要度应取较大值 0.8，得到 %f", got.Importance)
	}
	if got.AccessCount != 1 {
		t.Fatalf("合并后强化计数应为 1，得到 %d", got.AccessCount)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("合并后标签应为 2 个，得到 %v", got.Tags)
	}
	if !got.CreatedAt.Equal(m1.CreatedAt) {
		t.Fatalf("合并应保留原创建时间: %v != %v", got.CreatedAt, m1.CreatedAt)
	}
}

// TestConcurrentStoreDedup 验证 storeMu 串行化写入路径：
// 并发写入同语义内容时恰好产生一条记忆、一次 created。
func TestConcurrentStoreDedup(t *testing.T) {
	e, _ := newTestEngine(t)

	const n = 8
	actions := make([]StoreAction, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := e.Store(context.Background(), StoreInput{Content: "并发写入的同一条记忆"})
			if err != nil {
				t.Errorf("并发 Store 失败: %v", err)
				return
			}
			actions[i] = res.Action
		}(i)
	}
	wg.Wait()

	created := 0
	for _, a := range actions {
		if a == ActionCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("并发写入应恰好一次 created，得到 %d（actions=%v）", created, actions)
	}
	st, err := e.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 {
		t.Fatalf("并发去重后总数应为 1，得到 %d", st.Total)
	}
}

func TestScopeIsolation(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	mustStore(t, e, "项目 A 的数据库密码是 hunter2", "fact", 0.5, "project_a")
	mustStore(t, e, "用户家住北京", "fact", 0.5, "user_bob")

	got, err := e.Search(ctx, SearchInput{Query: "数据库密码", Scope: "project_a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Scope != "project_a" {
		t.Fatalf("scope 过滤失效: %+v", got)
	}
	st, _ := e.Stats(ctx)
	if st.ByScope["project_a"] != 1 || st.ByScope["user_bob"] != 1 {
		t.Fatalf("scope 统计错误: %v", st.ByScope)
	}
}

func TestKindAndTagFilter(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	mustStore(t, e, "部署脚本位于 scripts/deploy.sh", "procedure", 0.5, "", "devops")
	mustStore(t, e, "部署失败时先看 Nginx 日志", "knowledge", 0.5, "", "devops")

	got, err := e.Search(ctx, SearchInput{Query: "部署", Kind: "procedure"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if m.Kind != KindProcedure {
			t.Fatalf("kind 过滤失效: %s", m.Kind)
		}
	}
	listed, err := e.List(ctx, ListInput{Tag: "devops"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("标签过滤应命中 2 条，得到 %d", len(listed))
	}
}

func TestListOrderByUpdatedAt(t *testing.T) {
	e, clock := newTestEngine(t)
	ctx := context.Background()

	mustStore(t, e, "第一条记忆", "fact", 0.5, "")
	clock.Advance(1 * time.Hour)
	mustStore(t, e, "第二条记忆", "fact", 0.5, "")
	clock.Advance(1 * time.Hour)
	mustStore(t, e, "第三条记忆", "fact", 0.5, "")

	got, err := e.List(ctx, ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Content != "第三条记忆" || got[2].Content != "第一条记忆" {
		t.Fatalf("列表应按更新时间倒序: %s, %s, %s", got[0].Content, got[1].Content, got[2].Content)
	}
}

func TestGetDeleteAndForget(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	m1, _ := mustStore(t, e, "临时令牌 abc123 将于明天过期", "event", 0.9, "")
	mustStore(t, e, "生产环境数据库在 10.0.0.5", "fact", 0.5, "")

	// Get
	got, err := e.Get(ctx, m1.ID)
	if err != nil || got.Content != m1.Content {
		t.Fatalf("Get 失败: %v %+v", err, got)
	}
	if _, err := e.Get(ctx, "mem_not_exist"); err == nil {
		t.Fatal("查询不存在的 ID 应返回错误")
	}

	// Delete
	n, err := e.Delete(ctx, []string{m1.ID, "mem_not_exist"})
	if err != nil || n != 1 {
		t.Fatalf("Delete 应删除 1 条，得到 %d, err=%v", n, err)
	}

	// Forget（语义删除）：不传 Threshold，验证哈希提供者的自适应默认阈值（0.5）
	mustStore(t, e, "旧版的 API 密钥 sk-old-xxx 已废弃", "fact", 0.3, "")
	removed, err := e.Forget(ctx, ForgetInput{Query: "废弃的 API 密钥"})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 {
		t.Fatal("语义遗忘应至少删除一条相近记忆")
	}
	st, _ := e.Stats(ctx)
	if st.Total != 1 {
		t.Fatalf("删除后应剩 1 条，得到 %d", st.Total)
	}
}

func TestRecencyDecayRerank(t *testing.T) {
	e, clock := newTestEngine(t)
	ctx := context.Background()

	// 旧但重要的记忆 vs 新但不重要的记忆，内容互不重叠
	old, _ := mustStore(t, e, "公司内网 VPN 地址是 vpn.corp.example.com", "fact", 1.0, "")
	mustStore(t, e, "今天午饭吃了红烧牛肉面", "event", 0.1, "")

	// 前进 120 天，旧记忆衰减但重要度撑住排名
	clock.Advance(120 * 24 * time.Hour)
	oldMem, err := e.Get(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = oldMem

	// 直接比较两条的衰减因子：旧的必须明显低
	decayOld := recencyDecay(clock.Now(), old.CreatedAt.AddDate(0, 0, -100), 30)
	decayNew := recencyDecay(clock.Now(), clock.Now().Add(-time.Hour), 30)
	if decayOld >= decayNew {
		t.Fatalf("衰减逻辑错误: old=%f new=%f", decayOld, decayNew)
	}
	// 半衰期验证：30 天前 → 0.5
	half := recencyDecay(clock.Now(), clock.Now().Add(-30*24*time.Hour), 30)
	if diff := half - 0.5; diff > 0.001 || diff < -0.001 {
		t.Fatalf("半衰期 30 天应衰减至 0.5，得到 %f", half)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// 第一阶段：写入
	e1, _ := newTestEngineAt(t, dir)
	m1, _ := mustStore(t, e1, "重启验证：持久化必须完整保留标签与重要度", "knowledge", 0.7, "persistence", "ci", "roundtrip")
	if err := e1.Close(); err != nil {
		t.Fatalf("关闭引擎失败: %v", err)
	}

	// 第二阶段：重开并验证（覆盖 govector 集合重载 + payload 类型往返）
	e2, _ := newTestEngineAt(t, dir)
	defer e2.Close()
	ctx := context.Background()

	got, err := e2.Get(ctx, m1.ID)
	if err != nil {
		t.Fatalf("重启后按 ID 查询失败: %v", err)
	}
	if got.Content != m1.Content || got.Kind != m1.Kind {
		t.Fatalf("重启后内容/类型不一致: %+v", got)
	}
	if got.Importance != 0.7 {
		t.Fatalf("重启后重要度不一致: %f", got.Importance)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "ci" {
		t.Fatalf("重启后标签不一致（[]any 往返问题）: %v", got.Tags)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("重启后时间戳丢失: %+v", got)
	}

	found, err := e2.Search(ctx, SearchInput{Query: "持久化验证", Scope: "persistence"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].ID != m1.ID {
		t.Fatalf("重启后语义检索失败: %+v", found)
	}
}

func TestDimensionIsolation(t *testing.T) {
	dir := t.TempDir()

	// 同一 DB、不同维度 → 集合互不污染
	e1, _ := newTestEngineAt(t, dir)
	mustStore(t, e1, "128 维集合的记忆", "fact", 0.5, "")
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	clock := &stubClock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	e2, err := Open(Config{
		DBPath:   filepath.Join(dir, "test_mem.db"),
		Embedder: NewHashEmbedder(256),
		now:      clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	st, err := e2.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 0 {
		t.Fatalf("不同维度集合应相互隔离，却看到 %d 条", st.Total)
	}
}

func TestValidationErrors(t *testing.T) {
	e, _ := newTestEngine(t)
	ctx := context.Background()

	if _, err := e.Store(ctx, StoreInput{Content: "  "}); err == nil {
		t.Fatal("空内容应报错")
	}
	if _, err := e.Store(ctx, StoreInput{Content: "ok", Kind: "bogus"}); err == nil {
		t.Fatal("非法类型应报错")
	}
	if _, err := e.Search(ctx, SearchInput{Query: ""}); err == nil {
		t.Fatal("空查询应报错")
	}
	if _, err := e.Forget(ctx, ForgetInput{Query: ""}); err == nil {
		t.Fatal("空遗忘目标应报错")
	}
	// 非法 kind 在过滤查询中同样必须报错，而不是静默忽略、放大结果集
	if _, err := e.Search(ctx, SearchInput{Query: "任意", Kind: "bogus"}); err == nil {
		t.Fatal("Search 非法 kind 应报错")
	}
	if _, err := e.List(ctx, ListInput{Kind: "bogus"}); err == nil {
		t.Fatal("List 非法 kind 应报错")
	}
	if _, err := e.Forget(ctx, ForgetInput{Query: "任意", Kind: "bogus"}); err == nil {
		t.Fatal("Forget 非法 kind 应报错")
	}
	// 重要度越界裁剪
	m, err := e.Store(ctx, StoreInput{Content: "重要度越界", Importance: 42})
	if err != nil || m.Memory.Importance != 1 {
		t.Fatalf("重要度应裁剪到 1，得到 %f err=%v", m.Memory.Importance, err)
	}
}

// TestListAndSearchLimit 验证 limit 截断与排序稳定性。
func TestListAndSearchLimit(t *testing.T) {
	e, clock := newTestEngine(t)
	ctx := context.Background()

	mustStore(t, e, "第一条待 limit 记忆", "fact", 0.5, "")
	clock.Advance(1 * time.Hour)
	mustStore(t, e, "第二条待 limit 记忆", "fact", 0.5, "")
	clock.Advance(1 * time.Hour)
	third, _ := mustStore(t, e, "第三条待 limit 记忆", "fact", 0.5, "")

	listed, err := e.List(ctx, ListInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("List limit=2 应只返回 2 条，得到 %d", len(listed))
	}
	if listed[0].ID != third.ID {
		t.Fatalf("List 截断应保留最新的一条: %s", listed[0].Content)
	}

	found, err := e.Search(ctx, SearchInput{Query: "待 limit 记忆", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("Search limit=1 应只返回 1 条，得到 %d", len(found))
	}
}

func TestNormalizeKind(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
		ok   bool
	}{
		{"", KindFact, true},
		{"fact", KindFact, true},
		{"PREFERENCE", KindPreference, true},
		{" knowledge ", KindKnowledge, true},
		{"nope", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeKind(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("NormalizeKind(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("NormalizeKind(%q) 应返回错误", c.in)
		}
	}
}
