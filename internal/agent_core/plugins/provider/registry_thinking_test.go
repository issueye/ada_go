package provider

import (
	"testing"

	"github.com/adag/ada-go/internal/common/ports"
)

func TestApplyThinkingLevel(t *testing.T) {
	model := Model{SupportsThinking: true, ThinkingLevels: []string{"low", "high", "max"}}
	tests := []struct {
		name      string
		model     Model
		selected  string
		wantThink *bool
		wantLevel string
	}{
		{"default first", model, "", boolPtr(true), "low"},
		{"selected high", model, "high", boolPtr(true), "high"},
		{"case folded", model, "MAX", boolPtr(true), "max"},
		{"invalid ignored", model, "extreme", nil, ""},
		{"no levels", Model{SupportsThinking: true}, "high", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p ports.Params
			applyThinkingLevel(&p, tc.model, tc.selected)
			if tc.wantThink == nil {
				if p.Thinking != nil || p.ReasoningEffort != "" {
					t.Fatalf("期望不改动，得到 thinking=%v effort=%q", p.Thinking, p.ReasoningEffort)
				}
				return
			}
			if p.Thinking == nil || *p.Thinking != *tc.wantThink {
				t.Fatalf("thinking = %v，期望 %v", p.Thinking, *tc.wantThink)
			}
			if p.ReasoningEffort != tc.wantLevel {
				t.Fatalf("effort = %q，期望 %q", p.ReasoningEffort, tc.wantLevel)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }
