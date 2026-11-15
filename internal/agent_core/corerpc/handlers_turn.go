package corerpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/adag/ada-go/internal/common/domain"
)

// ---- turn ----

func (s *Server) handleTurnStart(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID       string            `json:"threadId"`
		IdempotencyKey string            `json:"idempotencyKey"`
		Text           string            `json:"text"`
		Images         []json.RawMessage `json:"images"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("turn.start: " + err.Error())
	}
	if p.ThreadID == "" || len(p.IdempotencyKey) < 8 {
		return nil, invalid("turn.start: 需要 threadId，且 idempotencyKey 至少 8 个字符")
	}
	images, imgErr := s.decodeTurnImages(p.ThreadID, p.Images)
	if imgErr != nil {
		return nil, invalid("turn.start: " + imgErr.Error())
	}
	if strings.TrimSpace(p.Text) == "" && len(images) == 0 {
		return nil, invalid("turn.start: text 与 images 至少其一非空")
	}
	if err := s.sess.ModelError(); err != nil {
		return nil, rpcError(err)
	}
	if _, err := s.sess.GetThread(ctx, p.ThreadID); err != nil {
		return nil, rpcError(err)
	}

	key := p.ThreadID + "\x00" + p.IdempotencyKey
	s.mu.Lock()
	if ent, ok := s.idem[key]; ok {
		s.mu.Unlock()
		return map[string]any{"runId": ent.runID, "duplicate": true}, nil
	}
	if cur := s.runs[p.ThreadID]; cur != nil && cur.status == "running" {
		s.mu.Unlock()
		return nil, rpcError(domain.NewError(domain.CodeThreadBusy, "线程已有运行中轮次"))
	}
	runID, err := s.sess.NewRunID(ctx)
	if err != nil {
		s.mu.Unlock()
		return nil, rpcError(err)
	}
	runCtx, cancel := context.WithCancel(s.root)
	info := &runInfo{threadID: p.ThreadID, runID: runID, status: "running", startedAt: time.Now().UTC(), cancel: cancel}
	s.runs[p.ThreadID] = info
	s.idem[key] = idemEntry{runID: runID, at: time.Now().UTC()}
	pruneIdemLocked(s.idem, time.Now().UTC())
	s.mu.Unlock()

	// 等轮次登记完活跃状态再返回 turn.start：紧随其后的 turn.steer 不会被
	// 误判成「没有运行中轮次」。登记/注销由 RunInThread 成对完成。
	ready := make(chan struct{})
	go func() {
		term, err := s.sess.RunInThreadReady(runCtx, p.ThreadID, runID, p.Text, images, ready)
		s.mu.Lock()
		if cur := s.runs[p.ThreadID]; cur != nil && cur.runID == runID {
			cur.status = "terminal"
			cur.terminal = string(term)
			cur.endedAt = time.Now().UTC()
			cur.cancel = nil
			if err != nil {
				cur.terminal = string(domain.TerminalError)
				var de *domain.Error
				if errors.As(err, &de) {
					cur.errorCode = int(de.Code)
				}
			}
		}
		s.mu.Unlock()
		cancel()
	}()
	<-ready
	return map[string]any{"runId": runID, "duplicate": false, "seq": 0}, nil
}

func (s *Server) handleTurnCancel(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("turn.cancel: 需要 threadId")
	}
	s.mu.Lock()
	cur := s.runs[p.ThreadID]
	if cur == nil || cur.status != "running" || cur.cancel == nil {
		state := s.runStateLocked(p.ThreadID)
		s.mu.Unlock()
		return map[string]any{"run": state, "noActiveRun": true}, nil
	}
	cur.cancel()
	state := runStateJSON(cur)
	s.mu.Unlock()
	// 审计（09 §10）：取消是用户可见的控制面动作，落 audit 表（M5）。
	s.sess.Audit(ctx, "turn.cancel", p.ThreadID, "", map[string]any{"runId": cur.runID})
	return map[string]any{"run": state}, nil
}

func (s *Server) handleTurnGet(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("turn.get: 需要 threadId")
	}
	return map[string]any{
		"run":         s.runState(p.ThreadID),
		"queueLength": s.sess.SteeringQueueLength(p.ThreadID),
	}, nil
}

// handleTurnSteer 排队一条中途消息（turn.steer）。
//
// 只有在 runs 表里存在 running 状态的轮次时才接受；没有运行中轮次返回
// core_unavailable（1002），与协议 errors: ["invalid_params","core_unavailable"]
// 对齐。images 支持 ≤4 张图片，双门校验失败按 invalid_params 拒绝。
func (s *Server) handleTurnSteer(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string            `json:"threadId"`
		Text     string            `json:"text"`
		Images   []json.RawMessage `json:"images"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("turn.steer: " + err.Error())
	}
	if strings.TrimSpace(p.ThreadID) == "" {
		return nil, invalid("turn.steer: 需要 threadId")
	}
	threadID := strings.TrimSpace(p.ThreadID)
	images, imgErr := s.decodeTurnImages(p.ThreadID, p.Images)
	if imgErr != nil {
		return nil, invalid("turn.steer: " + imgErr.Error())
	}
	if strings.TrimSpace(p.Text) == "" && len(images) == 0 {
		return nil, invalid("turn.steer: text 与 images 至少其一非空")
	}
	if utf8.RuneCountInString(p.Text) > 65536 {
		return nil, invalid("turn.steer: text 不能超过 65536 字符")
	}

	s.mu.Lock()
	cur := s.runs[threadID]
	running := cur != nil && cur.status == "running"
	s.mu.Unlock()
	if !running {
		return nil, rpcError(domain.NewError(domain.CodeCoreUnavailable,
			"该线程没有运行中轮次，无法排队中途消息"))
	}

	position, err := s.sess.EnqueueSteering(threadID, p.Text, images)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"queued": true, "position": position}, nil
}

// decodeTurnImages 把 turn.start/steer 的 images 参数解码成消息部件，并执行
// 双门校验（产品 capabilities.images + 模型 input 含 image）。规格：≤4 张，
// PNG/JPEG/WebP/GIF，单张解码后 ≤8MB。
func (s *Server) decodeTurnImages(threadID string, raw []json.RawMessage) ([]domain.Part, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 4 {
		return nil, errors.New("图片最多 4 张")
	}
	allowedMime := map[string]bool{
		"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true,
	}
	out := make([]domain.Part, 0, len(raw))
	for i, item := range raw {
		var part domain.Part
		if err := json.Unmarshal(item, &part); err != nil {
			return nil, fmt.Errorf("第 %d 张图片不是合法 JSON", i+1)
		}
		if part.Type != domain.PartImage {
			return nil, fmt.Errorf("第 %d 个 images 元素必须是 image 部件", i+1)
		}
		part.Mime = strings.ToLower(strings.TrimSpace(part.Mime))
		if !allowedMime[part.Mime] {
			return nil, fmt.Errorf("第 %d 张图片的 MIME %q 不支持（PNG/JPEG/WebP/GIF）", i+1, part.Mime)
		}
		data, err := base64.StdEncoding.DecodeString(part.Data)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张图片不是合法 base64", i+1)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("第 %d 张图片内容为空", i+1)
		}
		if len(data) > 8<<20 {
			return nil, fmt.Errorf("第 %d 张图片超过 8MB 上限", i+1)
		}
		out = append(out, domain.Part{Type: domain.PartImage, Mime: part.Mime, Data: part.Data})
	}
	if err := s.imagesGate(threadID); err != nil {
		return nil, err
	}
	return out, nil
}

// imagesGate 是图片输入的双门：产品 capabilities.images 与当前模型 input。
// 模型不在供应商目录里（如测试注入的 fake）时无法判定，放行由运行期报错兜底。
func (s *Server) imagesGate(threadID string) error {
	if !s.sess.Product().Capabilities.Images {
		return errors.New("当前产品未开启图片输入（capabilities.images=false）")
	}
	ref := s.sess.ModelRefFor(threadID)
	for _, m := range s.sess.ProviderModels(ref.ProviderID) {
		if m.ID != ref.ModelID {
			continue
		}
		for _, input := range m.Input {
			if input == "image" {
				return nil
			}
		}
		return fmt.Errorf("当前模型 %s 不支持图片输入", ref.ModelID)
	}
	return nil
}
