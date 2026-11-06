package canvas

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 项目（设计稿的归属容器）的增删改查。
//
// 为什么单独成文件：项目是「流程约束」的载体而不是画布渲染的一部分 ——
// Store 里节点/连线/截图任务各有自己的关注点，项目只负责「谁能装设计稿」。

// AddProject 新建项目。Name 必填且同名去重：
// 允许重名会让 Agent 反复 project_create 造出一堆空项目，反而更难管理。
func (s *Store) AddProject(name, note string) (Project, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return Project{}, ErrProjectNameEmpty
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.projects {
		if strings.EqualFold(p.Name, trimmed) {
			return Project{}, ErrProjectDuplicate
		}
	}
	now := time.Now()
	s.seq++
	p := &Project{
		ID:        fmt.Sprintf("p-%d-%04d", now.UnixMilli(), s.seq%10000),
		Name:      trimmed,
		Note:      strings.TrimSpace(note),
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.projects[p.ID] = p
	s.persistLocked()
	return p.Clone(), nil
}

// ListProjects 返回项目列表（按创建时间升序）与各自的节点计数。
// 计数包含分组框，另外单列 FrameCount，方便 UI 显示「N 张设计稿 / M 个分组」。
func (s *Store) ListProjects() []ProjectSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProjectSummary, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, ProjectSummary{Project: p.Clone()})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	index := make(map[string]int, len(out))
	for i := range out {
		index[out[i].ID] = i
	}
	for _, n := range s.nodes {
		i, ok := index[n.ProjectID]
		if !ok {
			continue
		}
		out[i].NodeCount++
		if n.Kind == KindFrame {
			out[i].FrameCount++
		}
	}
	return out
}

// GetProject 取单个项目副本。
func (s *Store) GetProject(id string) (Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	return p.Clone(), nil
}

// UpdateProject 改名/改备注；空字符串表示不改该字段。
func (s *Store) UpdateProject(id, name, note string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	trimmed := strings.TrimSpace(name)
	if trimmed != "" {
		for pid, other := range s.projects {
			if pid != id && strings.EqualFold(other.Name, trimmed) {
				return Project{}, ErrProjectDuplicate
			}
		}
		p.Name = trimmed
	}
	if note != "" {
		p.Note = strings.TrimSpace(note)
	}
	p.UpdatedAt = time.Now()
	s.persistLocked()
	return p.Clone(), nil
}

// RemoveProject 删除空项目。仍有设计稿时拒绝（ErrProjectNotEmpty）：
// 删除项目会连带丢掉里面的设计稿，必须由调用方先显式 canvas_clear，
// 避免「删项目」变成一次静默的数据丢失。
func (s *Store) RemoveProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[id]; !ok {
		return ErrProjectNotFound
	}
	for _, n := range s.nodes {
		if n.ProjectID == id {
			return ErrProjectNotEmpty
		}
	}
	for _, e := range s.edges {
		a, okA := s.nodes[e.From]
		b, okB := s.nodes[e.To]
		if okA && okB && a.ProjectID == id && b.ProjectID == id {
			delete(s.edges, e.ID)
		}
	}
	delete(s.projects, id)
	s.persistLocked()
	return nil
}

// touchProjectLocked 更新项目活跃时间（调用方需持写锁）。
func (s *Store) touchProjectLocked(id string) {
	if p, ok := s.projects[id]; ok {
		p.UpdatedAt = time.Now()
	}
}
