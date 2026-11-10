package host

// agent.every 的通用定时器：宿主按固定间隔用新 Context 调用 handler，
// Close 后停止；它不解释任何 cron/任务语义。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

func TestEveryTimerInvokesHandlerWithFreshContext(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin, pluginapi.CapGlobalConfig)
	src := `
export default function (agent) {
  agent.every(1000, async (_req, ctx) => {
    await ctx.config.set("tick", { at: Date.now() });
  });
}
`
	script := testScript(t, caps, src)
	p, err := Load(context.Background(), script, caps, Context{ConfigDir: t.TempDir()}, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	dir := p.BaseContext().ConfigDir
	path := filepath.Join(dir, "test-plugin.json")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("定时器没有调用 handler（期望 %s）", path)
}
