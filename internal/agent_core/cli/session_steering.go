package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
)

// BeginTurn 登记线程有一个运行中轮次（引用计数，可安全重复调用）。
//
// 通常由 runInThread 调用；直接驱动轮次的调用方也可以用它显式开启 steering
// 窗口，再成对调用 EndTurn。steering 只在线程有活跃轮次时可入队。
func (s *Session) BeginTurn(threadID string) {
	if threadID == "" {
		return
	}
	s.steerMu.Lock()
	if s.turnActive == nil {
		s.turnActive = map[string]int{}
	}
	s.turnActive[threadID]++
	s.steerMu.Unlock()
}

// EndTurn 注销一次活跃轮次登记；计数归零时丢弃仍未排空的中途消息并记录日志。
//
// 丢弃而不是留给下一轮，是为了让 turn.steer 的语义可预期：入队只对本次运行中
// 的轮次有效，轮次结束后的排队不会偷偷注入下一轮。丢弃必须有可见记录（不静默）。
func (s *Session) EndTurn(threadID string) {
	if threadID == "" {
		return
	}
	s.steerMu.Lock()
	if n := s.turnActive[threadID]; n > 1 {
		s.turnActive[threadID] = n - 1
		s.steerMu.Unlock()
		return
	}
	delete(s.turnActive, threadID)
	pending := s.steerQueues[threadID]
	delete(s.steerQueues, threadID)
	s.steerMu.Unlock()

	if len(pending) == 0 {
		return
	}
	msg := fmt.Sprintf("cli: 线程 %s 的轮次已结束，丢弃 %d 条未排空的中途消息", threadID, len(pending))
	if s.cfg.Log != nil {
		s.cfg.Log("warn", msg, map[string]any{"threadId": threadID, "dropped": len(pending)})
		return
	}
	out := s.cfg.Err
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintln(out, msg)
}

// EnqueueSteering 把一条中途消息排入线程队列，返回其位置（从 1 开始）。
//
// 只有线程存在运行中轮次时才接受；否则返回 core_unavailable，而不是静默收下
// 再在轮次结束时丢掉。
func (s *Session) EnqueueSteering(threadID, text string, images []domain.Part) (int, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("cli: 中途消息需要 threadId")
	}
	if strings.TrimSpace(text) == "" {
		return 0, errors.New("cli: 中途消息不能为空")
	}

	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if s.turnActive[threadID] <= 0 {
		return 0, domain.NewError(domain.CodeCoreUnavailable, "线程没有运行中轮次，无法排队中途消息")
	}
	if s.steerQueues == nil {
		s.steerQueues = map[string][]domain.Message{}
	}
	s.steerQueues[threadID] = append(s.steerQueues[threadID], domain.Message{
		Role: domain.RoleUser,
		Parts: append([]domain.Part{{Type: domain.PartText, Text: text}},
			images...),
	})
	return len(s.steerQueues[threadID]), nil
}

// DrainSteering 排空线程队列并返回消息；实现 engine.SteeringSource。
func (s *Session) DrainSteering(_ context.Context, threadID string) []domain.Message {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	out := s.steerQueues[threadID]
	if len(out) == 0 {
		return nil
	}
	delete(s.steerQueues, threadID)
	return out
}

// SteeringQueueLength 返回线程待排空的中途消息数（turn.get 的 queueLength）。
func (s *Session) SteeringQueueLength(threadID string) int {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	return len(s.steerQueues[threadID])
}

// waitTurnsDrained 等待全部在途轮次结束（turnActive 计数归零）；超时返回 false。
func (s *Session) waitTurnsDrained(max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		s.steerMu.Lock()
		active := 0
		for _, n := range s.turnActive {
			active += n
		}
		s.steerMu.Unlock()
		if active == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
