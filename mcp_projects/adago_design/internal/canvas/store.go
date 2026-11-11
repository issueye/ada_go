// Package canvas 提供无限画布的节点存储、连线、截图任务队列与回环 HTTP API。
package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Node 是画布上的一个节点（组件/页面预览卡片或便签）。
//
// ProjectID 是**必填**归属：设计稿必须挂在已存在的项目下（见 Store.Add 的校验），
// 这是「先建项目、再加设计稿」这条流程约束在数据层的最小落点 ——
// HTTP 与 MCP 两条入口都绕不过它。
type Node struct {
	ID         string   `json:"id"`
	ProjectID  string   `json:"project_id"`
	Title      string   `json:"title"`
	System     string   `json:"system,omitempty"`
	Kind       string   `json:"kind"`               // screen | component | palette | note | frame
	HTML       string   `json:"html"`               // 完整预览文档（iframe srcdoc）；frame 节点为空
	Artboard   string   `json:"artboard,omitempty"` // 尺寸契约（design.Artboard.ID）
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	AutoHeight bool     `json:"auto_height,omitempty"` // 允许客户端按内容实测高度回写
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Z          int      `json:"z"`
	Tags       []string `json:"tags,omitempty"`

	// HTMLRev 是正文最近一次变更的时间。节点列表默认剥掉 html 大字段，
	// 客户端靠比较 html_rev 决定「要不要取正文/重载 iframe」——
	// 只有位置/标题变化的更新不再触发正文拉取。
	HTMLRev time.Time `json:"html_rev"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Clone 返回节点深拷贝。
func (n Node) Clone() Node {
	cp := n
	if n.Tags != nil {
		cp.Tags = append([]string(nil), n.Tags...)
	}
	return cp
}

// 节点种类常量。
const (
	KindScreen    = "screen"
	KindComponent = "component"
	KindPalette   = "palette"
	KindNote      = "note"
	KindFrame     = "frame" // 分组框：承载成员的背板
)

// ValidKind 校验节点种类。
func ValidKind(k string) bool {
	switch k {
	case KindScreen, KindComponent, KindPalette, KindNote, KindFrame:
		return true
	}
	return false
}

// Edge 是两节点之间的连线（画布 SVG 层渲染）。
type Edge struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// Clone 返回连线副本。
func (e Edge) Clone() Edge { return e }

// Project 是设计项目：设计稿的归属容器。
//
// 一个项目对应一块画布作用域：画布 UI 只展示当前项目的节点，
// 导出也按项目分目录（exports/<project-id>/）。项目本身不可为空创建 ——
// Name 必填且同名去重，避免 Agent 反复 project_create 造出一堆空项目。
type Project struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Note      string         `json:"note,omitempty"`
	Canvas    *ProjectCanvas `json:"canvas,omitempty"` // nil = 自由画布（无边界）
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Clone 返回项目副本（Canvas 深拷贝，避免调用方改到存储内部状态）。
func (p Project) Clone() Project {
	cp := p
	if p.Canvas != nil {
		canvasCopy := *p.Canvas
		cp.Canvas = &canvasCopy
	}
	return cp
}

// ProjectSummary 是带节点计数的项目视图（列表接口用）。
type ProjectSummary struct {
	Project
	NodeCount  int `json:"node_count"`
	FrameCount int `json:"frame_count"`
}

// LegacyProjectID 是旧数据（v1/v2，节点无 project_id）迁移时落脚的固定项目 id。
const LegacyProjectID = "p-default"

// 常见错误（稳定码，见 DESIGN.md §5）。
var (
	ErrNotFound         = errors.New("node not found")
	ErrEmptyStorePath   = errors.New("store path is empty")
	ErrEdgeEndpoint     = errors.New("edge endpoint node missing")
	ErrEdgeDuplicate    = errors.New("edge already exists")
	ErrEdgeSelfLoop     = errors.New("edge endpoints must differ")
	ErrEdgeCrossProject = errors.New("edge endpoints must belong to the same project")
	ErrProjectRequired  = errors.New("project is required")
	ErrUnknownProject   = errors.New("unknown project")
	ErrProjectNotFound  = errors.New("project not found")
	ErrProjectNameEmpty = errors.New("project name is required")
	ErrProjectDuplicate = errors.New("project name already exists")
	ErrProjectNotEmpty  = errors.New("project still has design nodes")
	// 画布版式（比例/尺寸）校验错误
	ErrUnknownCanvasRatio = errors.New("unknown canvas ratio")
	ErrCanvasSizeInvalid  = errors.New("invalid canvas size")
)

// Store 是线程安全的项目/节点/连线仓库，变更即持久化。
//
// 持久化策略（P0 加固）：紧凑序列化 + 备份轮换（canvas.json.bak/.bak2）+
// 写盘失败记入 lastPersistErr 并打日志（健康检查可见），不再静默丢错误。
type Store struct {
	mu        sync.RWMutex
	path      string // canvas.json；空 = 不持久化
	projects  map[string]*Project
	nodes     map[string]*Node
	edges     map[string]*Edge
	seq       int64 // 用于 ID 生成
	nextZ     int
	lastClear []*Node // 最近一次清空的快照（仅内存，供恢复）
	lastEdges []*Edge // 清空时的连线快照
	clearedAt time.Time

	rev            uint64                     // 变更版本号：每次变更 +1（SSE 客户端据此判断有无新数据）
	subs           map[chan struct{}]struct{} // 变更订阅者（SSE）
	lastPersistErr error                      // 最近一次写盘失败原因（nil = 正常）
}

// persistedFile 是 canvas.json 的 v3 格式（项目 + 节点 + 连线）；
// v2 无 projects、v1 为裸节点数组，读取时都按需迁移。
type persistedFile struct {
	Version  int        `json:"version"`
	Projects []*Project `json:"projects,omitempty"`
	Nodes    []*Node    `json:"nodes"`
	Edges    []*Edge    `json:"edges,omitempty"`
}

// OpenStore 打开（或创建）存储。dir 为空时仅内存。
//
// 主文件 canvas.json 缺失或损坏时依次尝试 canvas.json.bak / .bak2：
// 备份轮换的「重命名三连」中断在中间态时（旧主文件已改名、新文件未就位），
// 下一轮启动能从备份把画布捡回来，而不是当成空库。
func OpenStore(dir string) (*Store, error) {
	s := &Store{
		projects: map[string]*Project{},
		nodes:    map[string]*Node{},
		edges:    map[string]*Edge{},
		subs:     map[chan struct{}]struct{}{},
		nextZ:    1,
	}
	if dir == "" {
		return s, nil
	}
	s.path = filepath.Join(dir, "canvas.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	raw, readErr := os.ReadFile(s.path)
	if readErr != nil {
		// 主文件不可读（缺失/权限）：尝试备份，两个都没有就按新库处理
		for _, alt := range []string{s.path + ".bak", s.path + ".bak2"} {
			if b, e := os.ReadFile(alt); e == nil && len(b) > 0 {
				log.Printf("主文件不可读（%v），已从 %s 恢复", readErr, alt)
				raw = b
				break
			}
		}
	}
	if len(raw) > 0 {
		migrated, parseErr := s.loadSnapshot(raw)
		if parseErr != nil {
			// 主文件解析失败：先试备份，全失败才报错
			for _, alt := range []string{s.path + ".bak", s.path + ".bak2"} {
				b, e := os.ReadFile(alt)
				if e != nil || len(b) == 0 {
					continue
				}
				if m2, e2 := s.loadSnapshot(b); e2 == nil {
					log.Printf("主文件损坏（%v），已从 %s 恢复", parseErr, alt)
					migrated = m2
					parseErr = nil
					break
				}
			}
		}
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, parseErr)
		}
		if migrated {
			// 迁移后立刻回写：否则磁盘还停在旧版本，API 已经是项目制，两边对不上
			s.persistLocked()
		}
	}
	return s, nil
}

// loadSnapshot 解析并装载一份快照（v1 裸数组 / v2 / v3），返回是否发生迁移。
func (s *Store) loadSnapshot(raw []byte) (bool, error) {
	var nodes []*Node
	if err := json.Unmarshal(raw, &nodes); err == nil {
		return s.loadAll(nil, nodes, nil), nil
	}
	var file persistedFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return false, err
	}
	return s.loadAll(file.Projects, file.Nodes, file.Edges), nil
}

// loadAll 装载 v1/v2/v3 任意版本的快照，并完成「节点必须归属项目」的迁移。
//
// 迁移策略：v1/v2 的节点没有 project_id，统一落到固定的 p-default 项目
// （名称「默认项目（历史数据）」）。这是唯一的自动建项目场景 ——
// 目的是不丢历史数据，而不是给新设计稿开后门：Add 仍然拒绝空 project_id。
func (s *Store) loadAll(projects []*Project, nodes []*Node, edges []*Edge) bool {
	migrated := false
	for _, p := range projects {
		if p == nil || p.ID == "" {
			continue
		}
		cp := p.Clone()
		s.projects[cp.ID] = &cp
	}
	var orphans []*Node
	for _, n := range nodes {
		if n == nil || n.ID == "" {
			continue
		}
		cp := n.Clone()
		// 旧快照的节点没有 CSP：装载时补齐（自愈，只在真变了时算迁移）
		if s := SanitizePreviewHTML(cp.HTML); s != cp.HTML {
			cp.HTML = s
			migrated = true
		}
		s.nodes[cp.ID] = &cp
		if cp.ProjectID == "" {
			orphans = append(orphans, s.nodes[cp.ID])
		}
		if cp.Z >= s.nextZ {
			s.nextZ = cp.Z + 1
		}
	}
	if len(orphans) > 0 {
		s.ensureLegacyProjectLocked(orphans)
		migrated = true
	}
	// 项目已被删除但节点残留（手工改文件等）：一并归入历史项目，避免出现无主设计稿
	var lost []*Node
	for _, n := range s.nodes {
		if _, ok := s.projects[n.ProjectID]; !ok {
			lost = append(lost, n)
		}
	}
	if len(lost) > 0 {
		s.ensureLegacyProjectLocked(lost)
		migrated = true
	}
	for _, e := range edges {
		if e == nil || e.ID == "" || e.From == "" || e.To == "" {
			continue
		}
		a, okA := s.nodes[e.From]
		b, okB := s.nodes[e.To]
		if !okA || !okB || a.ProjectID != b.ProjectID {
			continue
		}
		s.edges[e.ID] = e
	}
	// 有项目段但版本更旧（v2 手工加了 projects）也视为需要规范化落盘
	if len(projects) == 0 && len(s.projects) > 0 {
		migrated = true
	}
	return migrated
}

func (s *Store) ensureLegacyProjectLocked(nodes []*Node) {
	if _, ok := s.projects[LegacyProjectID]; !ok {
		created := time.Now()
		for _, n := range nodes {
			if !n.CreatedAt.IsZero() && n.CreatedAt.Before(created) {
				created = n.CreatedAt
			}
		}
		s.projects[LegacyProjectID] = &Project{
			ID: LegacyProjectID, Name: "默认项目（历史数据）",
			Note:      "升级到项目制时自动创建，用于收纳升级前的设计稿",
			CreatedAt: created, UpdatedAt: time.Now(),
		}
	}
	for _, n := range nodes {
		n.ProjectID = LegacyProjectID
	}
}

// List 返回全部节点副本（按 UpdatedAt 升序）。
func (s *Store) List() []Node {
	return s.ListByProject("")
}

// ListByProject 返回某项目的节点（projectID 为空 = 全部），按 UpdatedAt 升序。
func (s *Store) ListByProject(projectID string) []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		if projectID != "" && n.ProjectID != projectID {
			continue
		}
		out = append(out, n.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out
}

// Get 取单个节点副本。
func (s *Store) Get(id string) (Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	if !ok {
		return Node{}, ErrNotFound
	}
	return n.Clone(), nil
}

// Add 新建节点（忽略传入的 ID/时间戳，由存储生成）。
//
// 流程约束：ProjectID 必填且项目必须已存在 —— 「先建项目、再加设计稿」。
// 校验放在 Store 而不是 handler，是因为节点有两条写入路径（HTTP 直连 / MCP 工具），
// 只有在数据层拦截才能保证两条路径都绕不过去。
func (s *Store) Add(n Node) (Node, error) {
	if strings.TrimSpace(n.ProjectID) == "" {
		return Node{}, ErrProjectRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[n.ProjectID]; !ok {
		return Node{}, ErrUnknownProject
	}
	now := time.Now()
	n.ID = fmt.Sprintf("n-%d-%04d", now.UnixMilli(), s.seq%10000)
	s.seq++
	n.CreatedAt = now
	n.UpdatedAt = now
	if n.HTML != "" {
		n.HTML = SanitizePreviewHTML(n.HTML) // 入库即注入 CSP（见 sanitize.go）
		n.HTMLRev = now
	}
	if n.Z <= 0 {
		if n.Kind == KindFrame {
			n.Z = 0 // 分组框垫底
		} else {
			n.Z = s.nextZ
			s.nextZ++
		}
	}
	if n.Kind == "" {
		n.Kind = KindNote
	}
	// 带画板的预览节点默认允许客户端实测高度回写（消除卡片内滚动条与截图裁切）
	if n.Artboard != "" {
		n.AutoHeight = true
	}
	if n.Width <= 0 {
		if n.Kind == KindFrame {
			n.Width = 600
		} else {
			n.Width = 420
		}
	}
	if n.Height <= 0 {
		if n.Kind == KindFrame {
			n.Height = 400
		} else {
			n.Height = 320
		}
	}
	// 零坐标节点（分组框除外）自动排版，避免 MCP 工具叠加堆放；
	// 排版只看同项目内的节点，不同项目各自从原点铺开。
	if n.X == 0 && n.Y == 0 && n.Kind != KindFrame {
		n.X, n.Y = s.autoPositionLocked(n.ProjectID, n.Width, n.Height)
	}
	s.nodes[n.ID] = &n
	s.touchProjectLocked(n.ProjectID)
	s.persistLocked()
	return n.Clone(), nil
}

// Update 更新已有节点：以 mut 对副本做修改后写回。
// 正文（HTML）真的变了才刷新 HTMLRev —— 列表剥掉 HTML 后，客户端靠它
// 区分「正文变了要取要重载」与「只是挪了位置」。
func (s *Store) Update(id string, mut func(*Node)) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok {
		return Node{}, ErrNotFound
	}
	cp := n.Clone()
	if mut != nil {
		mut(&cp)
	}
	cp.ID = n.ID
	cp.CreatedAt = n.CreatedAt
	cp.UpdatedAt = time.Now()
	if cp.HTML != n.HTML {
		cp.HTML = SanitizePreviewHTML(cp.HTML)
		cp.HTMLRev = cp.UpdatedAt
	} else {
		cp.HTMLRev = n.HTMLRev
	}
	s.nodes[id] = &cp
	s.persistLocked()
	return cp.Clone(), nil
}

// Remove 删除节点，返回是否确实删除；级联删除关联连线。
func (s *Store) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[id]; !ok {
		return false, nil
	}
	delete(s.nodes, id)
	for eid, e := range s.edges {
		if e.From == id || e.To == id {
			delete(s.edges, eid)
		}
	}
	s.persistLocked()
	return true, nil
}

// Clear 清空**指定项目**的节点与连线（projectID 必填），返回清除数量。
// 快照保留在内存中供 RestoreLastClear —— 恢复回到原项目，不会跨项目串味。
func (s *Store) Clear(projectID string) (int, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, ErrProjectRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return 0, ErrUnknownProject
	}
	ids := map[string]bool{}
	for id, node := range s.nodes {
		if node.ProjectID == projectID {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	snapshot := make([]*Node, 0, len(ids))
	for id := range ids {
		cp := s.nodes[id].Clone()
		snapshot = append(snapshot, &cp)
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].CreatedAt.Before(snapshot[j].CreatedAt) })
	edgeSnapshot := make([]*Edge, 0, 4)
	for _, e := range s.edges {
		if ids[e.From] || ids[e.To] {
			cp := e.Clone()
			edgeSnapshot = append(edgeSnapshot, &cp)
		}
	}
	s.lastClear = snapshot
	s.lastEdges = edgeSnapshot
	s.clearedAt = time.Now()
	for id := range ids {
		delete(s.nodes, id)
	}
	for _, e := range edgeSnapshot {
		delete(s.edges, e.ID)
	}
	s.touchProjectLocked(projectID)
	s.persistLocked()
	return len(snapshot), nil
}

// RestoreLastClear 恢复最近一次清空的节点与连线；无可恢复内容返回 false。
// 恢复保留原 ID 与坐标；与现有节点 ID 冲突时跳过该节点。
func (s *Store) RestoreLastClear() (int, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lastClear) == 0 {
		return 0, time.Time{}, false
	}
	restored := 0
	for _, n := range s.lastClear {
		if _, exists := s.nodes[n.ID]; exists {
			continue
		}
		if _, ok := s.projects[n.ProjectID]; !ok {
			continue // 项目已被删除，恢复会造出无主设计稿
		}
		cp := n.Clone()
		s.nodes[n.ID] = &cp
		if cp.Z >= s.nextZ {
			s.nextZ = cp.Z + 1
		}
		restored++
	}
	for _, e := range s.lastEdges {
		if _, ok := s.edges[e.ID]; ok {
			continue
		}
		if _, a := s.nodes[e.From]; !a {
			continue
		}
		if _, b := s.nodes[e.To]; !b {
			continue
		}
		cp := e.Clone()
		s.edges[e.ID] = &cp
	}
	s.lastClear = nil
	s.lastEdges = nil
	s.persistLocked()
	return restored, s.clearedAt, true
}

// AddEdge 新建连线；两端节点必须存在、不可自环、不可重复。
func (s *Store) AddEdge(from, to, label string) (Edge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if from == to {
		return Edge{}, ErrEdgeSelfLoop
	}
	if _, ok := s.nodes[from]; !ok {
		return Edge{}, ErrEdgeEndpoint
	}
	if _, ok := s.nodes[to]; !ok {
		return Edge{}, ErrEdgeEndpoint
	}
	// 连线不跨项目：项目之间是独立画布作用域，跨项目连线在 UI 上没有意义
	if s.nodes[from].ProjectID != s.nodes[to].ProjectID {
		return Edge{}, ErrEdgeCrossProject
	}
	for _, e := range s.edges {
		if (e.From == from && e.To == to) || (e.From == to && e.To == from) {
			return Edge{}, ErrEdgeDuplicate
		}
	}
	now := time.Now()
	e := Edge{
		ID:    fmt.Sprintf("e-%d-%04d", now.UnixMilli(), s.seq%10000),
		From:  from,
		To:    to,
		Label: strings.TrimSpace(label),
	}
	s.seq++
	s.edges[e.ID] = &e
	s.persistLocked()
	return e.Clone(), nil
}

// ListEdges 返回全部连线副本（按 ID 升序）。
func (s *Store) ListEdges() []Edge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Edge, 0, len(s.edges))
	for _, e := range s.edges {
		out = append(out, e.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RemoveEdge 删除连线，返回是否确实删除。
func (s *Store) RemoveEdge(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.edges[id]; !ok {
		return false, nil
	}
	delete(s.edges, id)
	s.persistLocked()
	return true, nil
}

// AutoPosition 为没有坐标的新节点找位置：排在同项目最右节点右侧，
// 超过画布换行宽度则回落到新行（以最高节点底边为基准）。
// 分组框（frame）不参与布局计算；不同项目各从原点铺开，互不影响。
func (s *Store) AutoPosition(projectID string, w, h int) (float64, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.autoPositionLocked(projectID, w, h)
}

// autoPositionLocked 是 AutoPosition 的锁内实现（调用方需已持锁）。
func (s *Store) autoPositionLocked(projectID string, w, h int) (float64, float64) {
	const gap, wrapWidth = 48, 3600
	maxX, maxBottom := 0.0, 0.0
	any := false
	for _, n := range s.nodes {
		if n.ProjectID != projectID || n.Kind == KindFrame {
			continue
		}
		any = true
		if right := n.X + float64(n.Width); right > maxX {
			maxX = right
		}
		if bottom := n.Y + float64(n.Height); bottom > maxBottom {
			maxBottom = bottom
		}
	}
	if !any {
		return 80, 80
	}
	if maxX+float64(w)+gap > wrapWidth {
		return 80, maxBottom + gap
	}
	return maxX + gap, 80
}

// Rev 返回存储变更版本号：任何变更都会 +1，SSE 客户端据此判断有无新数据。
func (s *Store) Rev() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}

// PersistError 返回最近一次写盘失败的原因（nil = 一切正常）。
// 健康检查用它把「看起来在保存、实际在丢数据」暴露出来。
func (s *Store) PersistError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastPersistErr
}

// Subscribe 订阅变更通知。变更时向每个订阅者非阻塞广播一次（容量 1，
// 连续变更合帧成一条）；返回的 cancel 解除订阅。
func (s *Store) Subscribe() (<-chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 1)
	if s.subs == nil {
		s.subs = map[chan struct{}]struct{}{}
	}
	s.subs[ch] = struct{}{}
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs, ch)
	}
}

// notifyLocked 向全部订阅者广播（调用方需持锁；非阻塞，慢订阅者只保留最新信号）。
func (s *Store) notifyLocked() {
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// persistLocked 把当前状态原子写入磁盘（调用方需持写锁），v3 格式。
// 每次变更都会先推进版本号并广播订阅者 —— 版本推进不依赖写盘是否成功，
// 内存态才是唯一真相，磁盘只是它的持久化。
//
// 写盘三步：tmp 写入 → 旧主文件轮换进备份（.bak → .bak2）→ tmp 就位主文件。
// 任一步失败都记入 lastPersistErr 并打日志，不再静默吞掉。
func (s *Store) persistLocked() {
	s.rev++
	s.notifyLocked()
	if s.path == "" {
		return
	}
	nodes := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CreatedAt.Before(nodes[j].CreatedAt) })
	edges := make([]*Edge, 0, len(s.edges))
	for _, e := range s.edges {
		edges = append(edges, e)
	}
	projects := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].CreatedAt.Before(projects[j].CreatedAt) })
	file := persistedFile{Version: 3, Projects: projects, Nodes: nodes, Edges: edges}
	// 紧凑序列化：缩进只让文件无谓膨胀 30%+，快照是给机器读的
	raw, err := json.Marshal(file)
	if err == nil {
		tmp := s.path + ".tmp"
		err = os.WriteFile(tmp, raw, 0o644)
		if err == nil {
			// 旧主文件轮换进备份：.bak → .bak2，主文件 → .bak。
			// 中断在中间态也没关系：OpenStore 会从 .bak/.bak2 降级恢复。
			if _, statErr := os.Stat(s.path); statErr == nil {
				if _, bakErr := os.Stat(s.path + ".bak"); bakErr == nil {
					_ = os.Rename(s.path+".bak", s.path+".bak2")
				}
				_ = os.Rename(s.path, s.path+".bak")
			}
			err = os.Rename(tmp, s.path)
		}
	}
	if err != nil {
		s.lastPersistErr = fmt.Errorf("persist %s: %w", s.path, err)
		log.Printf("画布持久化失败（数据仍在内存，稍后变更会重试）: %v", s.lastPersistErr)
		return
	}
	s.lastPersistErr = nil
}
