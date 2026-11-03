package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/govector/core"
)

// Config 是记忆引擎的配置项。
type Config struct {
	// DBPath 是 govector BoltDB 数据库文件路径。
	DBPath string
	// Embedder 是文本向量化提供者（必填）。
	Embedder Embedder
	// DedupThreshold 是语义去重阈值：同 scope 下相似度达到该值时，
	// 视为重复记忆并执行"合并更新"而非新增。默认 0.93。
	DedupThreshold float64
	// RecencyHalfLifeDays 是检索重排时新近度衰减的半衰期（天）。默认 30。
	RecencyHalfLifeDays float64

	// now 可注入时钟，供测试使用。
	now func() time.Time
}

// Engine 是长期记忆引擎：负责记忆的存储、语义检索、管理与统计。
type Engine struct {
	cfg   Config
	store *core.Storage
	col   *core.Collection

	storeMu sync.Mutex // 串行化写入路径，避免并发去重竞态
}

// 默认集合名带维度后缀：切换嵌入提供者/维度时互不干扰。
func collectionName(dim int) string { return fmt.Sprintf("memories_%d", dim) }

// Open 打开（或创建）记忆库，并加载既有数据到内存索引。
func Open(cfg Config) (*Engine, error) {
	if cfg.Embedder == nil {
		return nil, fmt.Errorf("必须提供 Embedder")
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return nil, fmt.Errorf("必须提供 DBPath")
	}
	if cfg.DedupThreshold <= 0 {
		// 默认阈值由嵌入提供者声明：不同嵌入器的相似度分布差异巨大
		if aware, ok := cfg.Embedder.(DedupThresholdAware); ok {
			cfg.DedupThreshold = aware.DefaultDedupThreshold()
		} else {
			cfg.DedupThreshold = 0.93
		}
	}
	if cfg.RecencyHalfLifeDays <= 0 {
		cfg.RecencyHalfLifeDays = 30
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}

	store, err := core.NewStorage(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s 失败: %w", cfg.DBPath, err)
	}

	name := collectionName(cfg.Embedder.Dim())
	var col *core.Collection
	if meta, metaErr := store.LoadCollectionMeta(name); metaErr != nil {
		// 首次创建：启用 HNSW 索引与余弦相似度
		col, err = core.NewCollectionWithParams(name, cfg.Embedder.Dim(), core.Cosine, store, true, core.DefaultHNSWParams())
	} else {
		col, err = core.NewCollectionWithParams(meta.Name, meta.VectorLen, meta.Metric, store, meta.UseHNSW, meta.HNSWParams)
	}
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("初始化记忆集合 %s 失败: %w", name, err)
	}
	return &Engine{cfg: cfg, store: store, col: col}, nil
}

// Close 刷盘并关闭底层存储。
func (e *Engine) Close() error {
	if err := e.col.Flush(); err != nil {
		e.store.Close()
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := e.col.Close(); err != nil {
		e.store.Close()
		return err
	}
	return e.store.Close()
}

// StoreInput 是写入记忆的参数。
type StoreInput struct {
	Content    string   // 记忆正文（必填）
	Kind       string   // 记忆类型，空为 fact
	Tags       []string // 自由标签
	Importance float64  // 重要度 [0,1]，0 表示使用默认 0.5
	Scope      string   // 命名空间，空为 "default"
}

// StoreAction 描述本次写入的行为。
type StoreAction string

const (
	ActionCreated StoreAction = "created" // 新增记忆
	ActionUpdated StoreAction = "updated" // 命中重复记忆，合并更新
)

// Store 写入一条记忆。同 scope 下语义重复时自动合并：
// 以新内容为准、标签取并集、重要度取较大值，并递增强化次数。
func (e *Engine) Store(ctx context.Context, in StoreInput) (Memory, StoreAction, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return Memory{}, "", fmt.Errorf("记忆内容不能为空")
	}
	kind, err := NormalizeKind(in.Kind)
	if err != nil {
		return Memory{}, "", err
	}
	scope := normalizeScope(in.Scope)
	importance := ClampImportance(in.Importance)
	now := e.cfg.now().UTC().Truncate(time.Second)

	e.storeMu.Lock()
	defer e.storeMu.Unlock()

	vecs, err := e.cfg.Embedder.Embed(ctx, []string{content})
	if err != nil {
		return Memory{}, "", fmt.Errorf("向量化失败: %w", err)
	}

	// 同 scope 内做语义去重检查
	scopeFilter := &core.Filter{Must: []core.Condition{
		{Key: payloadScope, Match: core.MatchValue{Value: scope}},
	}}
	hits, err := e.col.Search(vecs[0], scopeFilter, 5)
	if err != nil {
		return Memory{}, "", fmt.Errorf("去重检索失败: %w", err)
	}

	if len(hits) > 0 && float64(hits[0].Score) >= e.cfg.DedupThreshold {
		// 命中重复：以新内容覆盖，合并标签与重要度
		old := payloadToMemory(hits[0].ID, hits[0].Payload)
		updated := Memory{
			ID:          old.ID,
			Content:     content,
			Kind:        kind,
			Tags:        mergeTags(old.Tags, in.Tags),
			Importance:  math.Max(importance, old.Importance),
			Scope:       scope,
			CreatedAt:   old.CreatedAt,
			UpdatedAt:   now,
			AccessCount: old.AccessCount + 1,
		}
		if err := e.upsertMemory(updated, vecs[0]); err != nil {
			return Memory{}, "", err
		}
		return updated, ActionUpdated, nil
	}

	mem := Memory{
		ID:         newMemoryID(now),
		Content:    content,
		Kind:       kind,
		Tags:       dedupTags(in.Tags),
		Importance: importance,
		Scope:      scope,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := e.upsertMemory(mem, vecs[0]); err != nil {
		return Memory{}, "", err
	}
	return mem, ActionCreated, nil
}

// SearchInput 是语义检索的参数。
type SearchInput struct {
	Query string // 检索文本（必填）
	Limit int    // 返回条数，默认 5
	Kind  string // 可选：按类型过滤
	Tag   string // 可选：按标签过滤
	Scope string // 可选：按命名空间过滤
}

// Search 语义检索记忆，按"相似度 × 重要度 × 新近度"综合得分排序。
func (e *Engine) Search(ctx context.Context, in SearchInput) ([]Memory, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, fmt.Errorf("检索内容不能为空")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}

	vecs, err := e.cfg.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("向量化失败: %w", err)
	}
	// 多取候选再做重排，避免 HNSW 截断丢结果
	hits, err := e.col.Search(vecs[0], buildFilter(in.Scope, in.Kind, in.Tag), max(20, limit*4))
	if err != nil {
		return nil, fmt.Errorf("检索失败: %w", err)
	}

	now := e.cfg.now()
	ranked := make([]Memory, 0, len(hits))
	for _, h := range hits {
		m := payloadToMemory(h.ID, h.Payload)
		m.Similarity = float64(h.Score)
		m.Score = m.Similarity * importanceWeight(m.Importance) * recencyDecay(now, m.UpdatedAt, e.cfg.RecencyHalfLifeDays)
		ranked = append(ranked, m)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}

// ListInput 是列表查询的参数。
type ListInput struct {
	Scope string // 可选：按命名空间过滤
	Kind  string // 可选：按类型过滤
	Tag   string // 可选：按标签过滤
	Limit int    // 返回条数，默认 20
}

// List 按最近更新时间倒序列出记忆（不走向量检索）。
func (e *Engine) List(_ context.Context, in ListInput) ([]Memory, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	points, err := e.col.GetPointsByFilter(buildFilter(in.Scope, in.Kind, in.Tag))
	if err != nil {
		return nil, fmt.Errorf("列出记忆失败: %w", err)
	}
	mems := make([]Memory, 0, len(points))
	for _, p := range points {
		mems = append(mems, payloadToMemory(p.ID, p.Payload))
	}
	sort.SliceStable(mems, func(i, j int) bool { return mems[i].UpdatedAt.After(mems[j].UpdatedAt) })
	if len(mems) > limit {
		mems = mems[:limit]
	}
	return mems, nil
}

// Get 按 ID 获取单条记忆；不存在时返回错误。
func (e *Engine) Get(_ context.Context, id string) (Memory, error) {
	points, err := e.col.GetPointsByFilter(&core.Filter{Must: []core.Condition{
		{Key: payloadID, Match: core.MatchValue{Value: id}},
	}})
	if err != nil {
		return Memory{}, fmt.Errorf("查询记忆失败: %w", err)
	}
	if len(points) == 0 {
		return Memory{}, fmt.Errorf("记忆 %s 不存在", id)
	}
	return payloadToMemory(points[0].ID, points[0].Payload), nil
}

// Delete 按 ID 删除记忆，返回实际删除条数。
func (e *Engine) Delete(_ context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return 0, nil
	}
	n, err := e.col.Delete(clean, nil)
	if err != nil {
		return 0, fmt.Errorf("删除记忆失败: %w", err)
	}
	return n, nil
}

// ForgetInput 是语义遗忘的参数。
type ForgetInput struct {
	Query     string  // 遗忘目标描述（必填）
	Threshold float64 // 相似度阈值，默认 0.8；越高越保守
	Limit     int     // 最多删除条数，默认 5
	Kind      string  // 可选：按类型过滤
	Tag       string  // 可选：按标签过滤
	Scope     string  // 可选：按命名空间过滤
}

// Forget 语义遗忘：删除与 Query 相似度达到阈值的所有记忆，
// 返回被删除的记忆列表（阈值基于原始向量相似度，不含重排加权）。
func (e *Engine) Forget(ctx context.Context, in ForgetInput) ([]Memory, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, fmt.Errorf("遗忘目标不能为空")
	}
	threshold := in.Threshold
	if threshold <= 0 {
		threshold = 0.8
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}

	vecs, err := e.cfg.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("向量化失败: %w", err)
	}
	hits, err := e.col.Search(vecs[0], buildFilter(in.Scope, in.Kind, in.Tag), max(20, limit*2))
	if err != nil {
		return nil, fmt.Errorf("检索待遗忘记忆失败: %w", err)
	}

	var doomed []Memory
	var ids []string
	for _, h := range hits {
		if float64(h.Score) < threshold || len(ids) >= limit {
			break // hits 已按相似度降序
		}
		doomed = append(doomed, payloadToMemory(h.ID, h.Payload))
		ids = append(ids, h.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := e.col.Delete(ids, nil); err != nil {
		return nil, fmt.Errorf("删除记忆失败: %w", err)
	}
	return doomed, nil
}

// Stats 汇总记忆库统计信息。
type Stats struct {
	Total      int            `json:"total"`      // 记忆总数
	ByKind     map[string]int `json:"by_kind"`    // 按类型分布
	ByScope    map[string]int `json:"by_scope"`   // 按命名空间分布
	DBPath     string         `json:"db_path"`    // 数据库文件
	Embedder   string         `json:"embedder"`   // 嵌入提供者
	Dimensions int            `json:"dimensions"` // 向量维度
	Collection string         `json:"collection"` // 底层集合名
}

// Stats 返回记忆库统计信息。
func (e *Engine) Stats(_ context.Context) (Stats, error) {
	points, err := e.col.GetPointsByFilter(&core.Filter{})
	if err != nil {
		return Stats{}, fmt.Errorf("统计失败: %w", err)
	}
	st := Stats{
		Total:      len(points),
		ByKind:     map[string]int{},
		ByScope:    map[string]int{},
		DBPath:     e.cfg.DBPath,
		Embedder:   e.cfg.Embedder.Name(),
		Dimensions: e.cfg.Embedder.Dim(),
		Collection: e.col.Name,
	}
	for _, p := range points {
		m := payloadToMemory(p.ID, p.Payload)
		st.ByKind[string(m.Kind)]++
		st.ByScope[m.Scope]++
	}
	return st, nil
}

// ---------------- 内部工具函数 ----------------

// upsertMemory 将记忆写入向量库并立即刷盘（记忆数据量小，安全优先）。
func (e *Engine) upsertMemory(m Memory, vec []float32) error {
	err := e.col.Upsert([]core.PointStruct{{
		ID:      m.ID,
		Version: uint64(m.UpdatedAt.UnixMilli()),
		Vector:  vec,
		Payload: memoryToPayload(m),
	}})
	if err != nil {
		return fmt.Errorf("写入记忆失败: %w", err)
	}
	if err := e.col.Flush(); err != nil {
		return fmt.Errorf("刷盘失败: %w", err)
	}
	return nil
}

// buildFilter 将 scope/kind/tag 组装为 govector payload 过滤器。
// 无条件时返回空过滤器（govector 的 GetPointsByFilter 不接受 nil）。
func buildFilter(scope, kind, tag string) *core.Filter {
	var must []core.Condition
	if scope = normalizeScope(scope); scope != "" {
		must = append(must, core.Condition{Key: payloadScope, Match: core.MatchValue{Value: scope}})
	}
	if k, err := NormalizeKind(kind); err == nil && kind != "" {
		must = append(must, core.Condition{Key: payloadKind, Match: core.MatchValue{Value: string(k)}})
	}
	if tag = strings.TrimSpace(tag); tag != "" {
		must = append(must, core.Condition{Key: payloadTags, Type: core.MatchTypeContains, Match: core.MatchValue{Value: tag}})
	}
	return &core.Filter{Must: must}
}

func normalizeScope(s string) string { return strings.TrimSpace(s) }

// newMemoryID 生成 "mem_" 前缀、时间有序的唯一 ID。
func newMemoryID(now time.Time) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("mem_%d", now.UnixNano())
	}
	return fmt.Sprintf("mem_%x_%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

// memoryToPayload 将记忆序列化为 govector payload。
// 时间统一存 RFC3339 字符串，避免 protobuf 数值往返的精度/类型问题。
func memoryToPayload(m Memory) core.Payload {
	p := core.Payload{
		payloadID:         m.ID,
		payloadContent:    m.Content,
		payloadKind:       string(m.Kind),
		payloadImportance: m.Importance,
		payloadScope:      m.Scope,
		payloadCreatedAt:  m.CreatedAt.Format(time.RFC3339),
		payloadUpdatedAt:  m.UpdatedAt.Format(time.RFC3339),
		payloadAccessCnt:  int64(m.AccessCount),
	}
	if len(m.Tags) > 0 {
		p[payloadTags] = m.Tags
	}
	return p
}

// payloadToMemory 从 govector payload 还原记忆。
// 注意持久化往返的类型变化：[]string → []any，int → int64。
func payloadToMemory(id string, p core.Payload) Memory {
	m := Memory{ID: id}
	if v, ok := p[payloadID].(string); ok && v != "" {
		m.ID = v
	}
	m.Content, _ = p[payloadContent].(string)
	if v, ok := p[payloadKind].(string); ok {
		m.Kind = Kind(v)
	}
	m.Tags = payloadToStrings(p[payloadTags])
	m.Importance = payloadToFloat(p[payloadImportance])
	m.Scope, _ = p[payloadScope].(string)
	m.CreatedAt = payloadToTime(p[payloadCreatedAt])
	m.UpdatedAt = payloadToTime(p[payloadUpdatedAt])
	m.AccessCount = int(payloadToFloat(p[payloadAccessCnt]))
	if m.CreatedAt.IsZero() {
		m.CreatedAt = m.UpdatedAt
	}
	return m
}

// payloadToStrings 兼容 []string 与 JSON 往返产生的 []any 两种形态。
func payloadToStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func payloadToFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return 0
	}
}

func payloadToTime(v any) time.Time {
	s, _ := v.(string)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// mergeTags 合并新旧标签，保持出现顺序并去重。
func mergeTags(old, new []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(old)+len(new))
	for _, t := range append(append([]string{}, old...), new...) {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// dedupTags 对新建记忆的标签去重。
func dedupTags(tags []string) []string { return mergeTags(nil, tags) }

// importanceWeight 将重要度映射为排序权重：0→0.7，0.5→0.85，1→1.0。
func importanceWeight(importance float64) float64 { return 0.7 + 0.3*importance }

// recencyDecay 按半衰期指数衰减记忆得分：半衰期内衰减到 0.5。
func recencyDecay(now, updatedAt time.Time, halfLifeDays float64) float64 {
	if updatedAt.IsZero() {
		return 1
	}
	days := now.Sub(updatedAt).Hours() / 24
	if days <= 0 {
		return 1
	}
	return math.Pow(0.5, days/halfLifeDays)
}
