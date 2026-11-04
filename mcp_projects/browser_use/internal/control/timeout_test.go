package control

import (
	"encoding/json"
	"testing"
	"time"

	"browseruse/internal/config"
)

// TestCallTimeoutForHonorsToolBudget 钉住“工具参数里的 timeout_ms 必须延长
// 外层 deadline”这条契约：否则 CLI/MCP 发 wait(timeout_ms=60000) 时仍会在
// 服务端默认 30s 被 context 取消，调用方看到的是 timeout 而不是工具自己的结果。
func TestCallTimeoutForHonorsToolBudget(t *testing.T) {
	s := &Server{cfg: config.Config{
		Agent:  config.AgentConfig{DefaultTimeoutMS: 30000},
		Script: config.ScriptConfig{MaxTimeoutMS: 300000},
	}}
	cases := []struct {
		name  string
		tool  string
		args  string
		reqMS int
		want  time.Duration
	}{
		{"default", "snapshot", `{}`, 0, 30 * time.Second},
		{"tool budget", "wait", `{"timeout_ms":60000}`, 0, 65 * time.Second},
		{"tool budget beats request", "wait", `{"timeout_ms":60000}`, 30000, 65 * time.Second},
		{"request only", "wait", `{}`, 120000, 120 * time.Second},
		{"script uses max timeout", "script", `{"timeout_ms":60000}`, 0, 300 * time.Second},
		{"malformed args", "wait", `not-json`, 0, 30 * time.Second},
		{"negative tool budget", "wait", `{"timeout_ms":-1}`, 0, 30 * time.Second},
		{"capped", "wait", `{"timeout_ms":600000}`, 0, maxCallTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.callTimeoutFor(c.tool, json.RawMessage(c.args), c.reqMS); got != c.want {
				t.Fatalf("callTimeoutFor(%q, %s, %d) = %v, want %v", c.tool, c.args, c.reqMS, got, c.want)
			}
		})
	}
}

func TestTimeoutFromArgs(t *testing.T) {
	if got := timeoutFromArgs(nil); got != 0 {
		t.Fatalf("nil args = %d, want 0", got)
	}
	if got := timeoutFromArgs(json.RawMessage(`{"timeout_ms":1500}`)); got != 1500 {
		t.Fatalf("timeout_ms = %d, want 1500", got)
	}
	if got := timeoutFromArgs(json.RawMessage(`{"timeout_ms":"1500"}`)); got != 0 {
		t.Fatalf("string timeout_ms = %d, want 0", got)
	}
}
