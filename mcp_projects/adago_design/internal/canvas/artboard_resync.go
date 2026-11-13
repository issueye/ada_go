package canvas

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 画板尺寸的显式重同步。
//
// 设计决定（IMPROVEMENTS §8.4 / §12.3）：切换画板或画布比例**不会**自动迁移已有
// 节点尺寸 —— 那会把用户精心摆放的画布打乱。这里提供一条显式通路：
// 由 Agent 或用户主动调用，按节点自己的 artboard 重新计算宽高，并压入一条
// 可撤销的批量历史。
//
// 依赖倒置：canvas 不引用 design，画板解析函数由装配层注入（与 CatalogSource 同理）。

// ErrArtboardResolver 表示当前 Store 未注入画板解析器（纯测试环境可能如此）。
var ErrArtboardResolver = errors.New("artboard resolver is not configured")

// NodeSizeChange 是单个节点的尺寸调整记录。
type NodeSizeChange struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artboard   string `json:"artboard"`
	FromWidth  int    `json:"from_width"`
	FromHeight int    `json:"from_height"`
	ToWidth    int    `json:"to_width"`
	ToHeight   int    `json:"to_height"`
}

// ResyncResult 是一次画板重同步的结果汇总。
type ResyncResult struct {
	ProjectID        string           `json:"project,omitempty"` // 空 = 全部项目
	Total            int              `json:"total"`             // 范围内的节点数
	Changed          int              `json:"changed"`           // 实际调整的节点数
	Skipped          int              `json:"skipped"`           // 无画板 / 未知画板
	UnknownArtboards []string         `json:"unknown_artboards,omitempty"`
	Nodes            []NodeSizeChange `json:"nodes"`
}

// ResyncArtboards 按节点自身的 artboard 重算宽高。
//
// 规则：
//   - 只处理 artboard 非空的节点；无画板的节点（便签/分组框/自由文本）跳过。
//   - 组件（KindComponent）只纠正宽度：高度由客户端实测回写（AutoHeight），
//     写死一个服务端高度会立刻被下一次测高覆盖。
//   - 其余画板节点（如整页 screen）同时纠正宽高。
//   - 未知画板不报错，逐条记入 UnknownArtboards，便于模型自纠。
//   - 有变化时合并为**一条** undo 记录：一次撤销即恢复全部节点尺寸。
func (s *Store) ResyncArtboards(projectID string) (ResyncResult, error) {
	projectID = strings.TrimSpace(projectID)
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ArtboardSize == nil {
		return ResyncResult{}, ErrArtboardResolver
	}
	if projectID != "" {
		if _, ok := s.projects[projectID]; !ok {
			return ResyncResult{}, ErrUnknownProject
		}
	}

	type pending struct {
		before, after Node
		project       string
	}
	var changes []pending
	unknown := map[string]bool{}
	res := ResyncResult{ProjectID: projectID, Nodes: []NodeSizeChange{}}

	for _, n := range s.nodes {
		if projectID != "" && n.ProjectID != projectID {
			continue
		}
		res.Total++
		if n.Artboard == "" {
			res.Skipped++
			continue
		}
		w, h, ok := s.ArtboardSize(n.Artboard, n.Kind)
		if !ok || w <= 0 || h <= 0 {
			res.Skipped++
			if !unknown[n.Artboard] {
				unknown[n.Artboard] = true
				res.UnknownArtboards = append(res.UnknownArtboards, n.Artboard)
			}
			continue
		}
		toW, toH := w, h
		if n.Kind == KindComponent {
			toH = n.Height // 高度由客户端实测回写，这里只纠正宽度
		}
		if n.Width == toW && n.Height == toH {
			continue
		}
		after := n.Clone()
		after.Width = toW
		after.Height = toH
		after.UpdatedAt = time.Now()
		changes = append(changes, pending{before: n.Clone(), after: after, project: n.ProjectID})
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].before.CreatedAt.Equal(changes[j].before.CreatedAt) {
			return changes[i].before.ID < changes[j].before.ID
		}
		return changes[i].before.CreatedAt.Before(changes[j].before.CreatedAt)
	})
	sort.Strings(res.UnknownArtboards)

	for _, c := range changes {
		s.putNodeLocked(c.after)
		res.Nodes = append(res.Nodes, NodeSizeChange{
			ID: c.after.ID, Title: c.after.Title, Artboard: c.after.Artboard,
			FromWidth: c.before.Width, FromHeight: c.before.Height,
			ToWidth: c.after.Width, ToHeight: c.after.Height,
		})
	}
	res.Changed = len(changes)
	if res.Changed == 0 {
		return res, nil
	}

	befores := make([]Node, len(changes))
	afters := make([]Node, len(changes))
	seen := map[string]bool{}
	for i, c := range changes {
		befores[i] = c.before.Clone()
		afters[i] = c.after.Clone()
		if !seen[c.project] {
			seen[c.project] = true
			s.touchProjectLocked(c.project)
		}
	}
	s.pushUndoLocked(undoEntry{
		desc: fmt.Sprintf("按画板重同步尺寸（%d 个节点）", len(changes)),
		undo: func() {
			for i := range befores {
				s.putNodeLocked(befores[i].Clone())
			}
		},
		redo: func() {
			for i := range afters {
				s.putNodeLocked(afters[i].Clone())
			}
		},
	})
	s.persistLocked()
	return res, nil
}
