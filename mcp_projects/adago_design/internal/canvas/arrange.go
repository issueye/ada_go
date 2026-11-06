package canvas

import (
	"sort"
	"time"
)

// 按项目分组排版：一块画布上同时展示多个项目时使用。
//
// 背景：项目制落地后，每个项目各自从 (80,80) 开始自动排版 —— 单项目视图没问题，
// 但「全部」视图里不同项目的卡片会完全重叠。这里提供一次性的分行排布：
// 一个项目一行，行内按创建时间横排，超宽换行；行首留出标签位（200px）。
const (
	arrangeStartX = 220 // 行首留白：给左侧项目标签
	arrangeStartY = 60
	arrangeGapX   = 32
	arrangeRowGap = 110 // 行与行之间（含标签高度）
	arrangeMaxW   = 1900
)

// ArrangeByProject 把全部节点按项目分行排布，返回移动过的节点数。
//
// 只处理普通节点；分组框（KindFrame）是自由绘制的背板，不参与自动排版
// —— 它的位置由创建时的成员边界决定，强行重排反而会错位。
func (s *Store) ArrangeByProject() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	projects := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		if !projects[i].CreatedAt.Equal(projects[j].CreatedAt) {
			return projects[i].CreatedAt.Before(projects[j].CreatedAt)
		}
		return projects[i].ID < projects[j].ID
	})

	moved := 0
	y := float64(arrangeStartY)
	now := time.Now()
	for _, p := range projects {
		nodes := make([]*Node, 0, 8)
		for _, n := range s.nodes {
			if n.ProjectID == p.ID && n.Kind != KindFrame {
				nodes = append(nodes, n)
			}
		}
		if len(nodes) == 0 {
			continue
		}
		sort.Slice(nodes, func(i, j int) bool {
			if !nodes[i].CreatedAt.Equal(nodes[j].CreatedAt) {
				return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
			}
			return nodes[i].ID < nodes[j].ID
		})

		x := float64(arrangeStartX)
		rowBottom := y
		for _, n := range nodes {
			w := float64(n.Width)
			if x > arrangeStartX && x+w > arrangeMaxW {
				x = arrangeStartX
				y = rowBottom + arrangeRowGap
				rowBottom = y
			}
			if n.X != x || n.Y != y {
				n.X, n.Y = x, y
				n.UpdatedAt = now
				moved++
			}
			x += w + arrangeGapX
			if bottom := y + float64(n.Height); bottom > rowBottom {
				rowBottom = bottom
			}
		}
		y = rowBottom + arrangeRowGap
	}
	if moved > 0 {
		s.persistLocked()
	}
	return moved, nil
}
