// Package canvas 提供无限画布的节点存储、截图任务队列与回环 HTTP API。
package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Node 是画布上的一个节点（组件/页面预览卡片或便签）。
type Node struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	System string   `json:"system,omitempty"`
	Kind   string   `json:"kind"` // screen | component | palette | note
	HTML   string   `json:"html"` // 完整预览文档（iframe srcdoc）
	Width  int      `json:"width"`
	Height int      `json:"height"`
	X      float64  `json:"x"`
	Y      float64  `json:"y"`
	Z      int      `json:"z"`
	Tags   []string `json:"tags,omitempty"`

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
)

// ValidKind 校验节点种类。
func ValidKind(k string) bool {
	switch k {
	case KindScreen, KindComponent, KindPalette, KindNote:
		return true
	}
	return false
}

// 常见错误（稳定码，见 DESIGN.md §5）。
var (
	ErrNotFound       = errors.New("node not found")
	ErrEmptyStorePath = errors.New("store path is empty")
)

// Store 是线程安全的节点仓库，变更即持久化。
type Store struct {
	mu        sync.RWMutex
	path      string // canvas.json；空 = 不持久化
	nodes     map[string]*Node
	seq       int64 // 用于 ID 生成
	nextZ     int
	lastClear []*Node // 最近一次清空的快照（仅内存，供恢复）
	clearedAt time.Time
}

// OpenStore 打开（或创建）存储。dir 为空时仅内存。
func OpenStore(dir string) (*Store, error) {
	s := &Store{nodes: map[string]*Node{}, nextZ: 1}
	if dir == "" {
		return s, nil
	}
	s.path = filepath.Join(dir, "canvas.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	raw, err := os.ReadFile(s.path)
	if err == nil && len(raw) > 0 {
		var nodes []*Node
		if err := json.Unmarshal(raw, &nodes); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
		for _, n := range nodes {
			if n == nil || n.ID == "" {
				continue
			}
			s.nodes[n.ID] = n
			if n.Z >= s.nextZ {
				s.nextZ = n.Z + 1
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	return s, nil
}

// List 返回全部节点副本（按 UpdatedAt 升序）。
func (s *Store) List() []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Node, 0, len(s.nodes))
	for _, n := range s.nodes {
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
func (s *Store) Add(n Node) Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	n.ID = fmt.Sprintf("n-%d-%04d", now.UnixMilli(), s.seq%10000)
	s.seq++
	n.CreatedAt = now
	n.UpdatedAt = now
	if n.Z <= 0 {
		n.Z = s.nextZ
		s.nextZ++
	}
	if n.Kind == "" {
		n.Kind = KindNote
	}
	if n.Width <= 0 {
		n.Width = 420
	}
	if n.Height <= 0 {
		n.Height = 320
	}
	s.nodes[n.ID] = &n
	s.persistLocked()
	return n.Clone()
}

// Update 更新已有节点：以 mut 对副本做修改后写回。
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
	s.nodes[id] = &cp
	s.persistLocked()
	return cp.Clone(), nil
}

// Remove 删除节点，返回是否确实删除。
func (s *Store) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[id]; !ok {
		return false, nil
	}
	delete(s.nodes, id)
	s.persistLocked()
	return true, nil
}

// Clear 清空全部节点，返回清除数量。快照保留在内存中供 RestoreLastClear。
func (s *Store) Clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.nodes)
	if n == 0 {
		return 0
	}
	snapshot := make([]*Node, 0, n)
	for _, node := range s.nodes {
		cp := node.Clone()
		snapshot = append(snapshot, &cp)
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].CreatedAt.Before(snapshot[j].CreatedAt) })
	s.lastClear = snapshot
	s.clearedAt = time.Now()
	s.nodes = map[string]*Node{}
	s.persistLocked()
	return n
}

// RestoreLastClear 恢复最近一次清空的节点；无可恢复内容返回 false。
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
		cp := n.Clone()
		s.nodes[n.ID] = &cp
		if cp.Z >= s.nextZ {
			s.nextZ = cp.Z + 1
		}
		restored++
	}
	s.lastClear = nil
	s.persistLocked()
	return restored, s.clearedAt, true
}

// AutoPosition 为没有坐标的新节点找位置：排在最右节点右侧，
// 超过画布换行宽度则回落到新行（以最高节点底边为基准）。
func (s *Store) AutoPosition(w, h int) (float64, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	const gap, wrapWidth = 48, 3600
	if len(s.nodes) == 0 {
		return 80, 80
	}
	maxX, maxBottom := 0.0, 0.0
	for _, n := range s.nodes {
		if right := n.X + float64(n.Width); right > maxX {
			maxX = right
		}
		if bottom := n.Y + float64(n.Height); bottom > maxBottom {
			maxBottom = bottom
		}
	}
	if maxX+float64(w)+gap > wrapWidth {
		return 80, maxBottom + gap
	}
	return maxX + gap, 80
}

// persistLocked 把当前状态原子写入磁盘（调用方需持写锁）。
func (s *Store) persistLocked() {
	if s.path == "" {
		return
	}
	nodes := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CreatedAt.Before(nodes[j].CreatedAt) })
	raw, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}
