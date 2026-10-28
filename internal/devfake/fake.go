// Package devfake 提供无模型依赖的确定性 ModelClient（CLI --fake 与本地冒烟）。
//
// 它不是测试替身（那是 conformance/testkit）：这里只做最小可运行的「读最后一条用户消息，
// 回显并 finish」，让 CLI 在没有 API key 时也能跑通 RunTurn、SQLite 与事件链。
package devfake

import (
	"context"
	"io"
	"strings"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// Model 是确定性的回显模型。
type Model struct {
	// Prefix 是回复前缀；空时 "fake: "。
	Prefix string
}

var _ ports.ModelClient = (*Model)(nil)

// New 创建回显模型。
func New() *Model { return &Model{Prefix: "fake: "} }

// Stream 实现端口：一次性产出 text.start/delta/end 与 finish。
func (m *Model) Stream(ctx context.Context, req ports.Request) (ports.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := lastUserText(req.Messages)
	if text == "" {
		text = "(empty)"
	}
	prefix := m.Prefix
	if prefix == "" {
		prefix = "fake: "
	}
	return &stream{deltas: []ports.Delta{
		{Kind: ports.DeltaTextStart},
		{Kind: ports.DeltaTextDelta, Text: prefix + text},
		{Kind: ports.DeltaTextEnd},
		{Kind: ports.DeltaUsage, Usage: &domain.Usage{InputTokens: 1, OutputTokens: 1}},
		{Kind: ports.DeltaFinish, FinishReason: "stop", StartMarker: "fake"},
	}}, nil
}

func lastUserText(msgs []domain.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != domain.RoleUser {
			continue
		}
		var b strings.Builder
		for _, p := range msgs[i].Parts {
			if p.Type == domain.PartText {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

type stream struct {
	deltas []ports.Delta
	i      int
	closed bool
}

func (s *stream) Recv() (ports.Delta, error) {
	if s.closed || s.i >= len(s.deltas) {
		return ports.Delta{}, io.EOF
	}
	d := s.deltas[s.i]
	s.i++
	return d, nil
}

func (s *stream) Close() error {
	s.closed = true
	return nil
}
