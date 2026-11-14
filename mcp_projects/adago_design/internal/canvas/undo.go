// 操作历史（迭代 3-②）：画布节点/连线操作的撤销与重做。
//
// 命令模式：每个变更操作在持锁状态下压入一对 undo/redo 闭包（捕获操作前后的
// 副本）；闭包只改内存态（putNodeLocked / removeNodeLocked / map 操作），
// 由 Undo/Redo 统一推进版本号并持久化。历史只管「画布内容」——
// 项目本身的增删改不进历史；历史栈只在内存，进程退出即清空。
package canvas

import "strings"

// undoLimit 是历史上限（步数）：防长会话内存无界增长。
const undoLimit = 50

type undoEntry struct {
	desc string
	undo func()
	redo func()
}

// nodeDisplayTitle 拼操作描述用的节点名：空标题回落到 id/端点串。
func nodeDisplayTitle(title, fallback string) string {
	if strings.TrimSpace(title) == "" {
		return fallback
	}
	return "「" + title + "」"
}

// Undo 撤销最近一次画布操作，返回操作描述；无历史返回 false。
func (s *Store) Undo() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.undoStack) == 0 {
		return "", false
	}
	e := s.undoStack[len(s.undoStack)-1]
	s.undoStack = s.undoStack[:len(s.undoStack)-1]
	e.undo()
	s.redoStack = append(s.redoStack, e)
	s.persistLocked()
	return e.desc, true
}

// Redo 重做最近一次被撤销的操作，返回操作描述；无可重做返回 false。
func (s *Store) Redo() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.redoStack) == 0 {
		return "", false
	}
	e := s.redoStack[len(s.redoStack)-1]
	s.redoStack = s.redoStack[:len(s.redoStack)-1]
	e.redo()
	s.undoStack = append(s.undoStack, e)
	s.persistLocked()
	return e.desc, true
}

// HistoryDepth 返回 (可撤销步数, 可重做步数)。
func (s *Store) HistoryDepth() (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.undoStack), len(s.redoStack)
}

// pushUndoLocked 压入一条历史（调用方需持写锁）；新操作会清空重做栈。
func (s *Store) pushUndoLocked(e undoEntry) {
	s.undoStack = append(s.undoStack, e)
	if len(s.undoStack) > undoLimit {
		s.undoStack = append([]undoEntry(nil), s.undoStack[len(s.undoStack)-undoLimit:]...)
	}
	s.redoStack = nil
}
