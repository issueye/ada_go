// Package sqlite 的测试。
//
// 并行性（10 §11.2：测试必须可并行）：绝大多数用例只依赖 t.TempDir() 与自己的
// 工作区锁，可安全 t.Parallel()。少数用例刻意不并行，原因写在用例注释里
// （例如依赖写协程计时器粒度、或会占住 CPU 让退避计时不准的并发压测）。
package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// newWorkspace 造一个独占的临时工作区（测试绝不依赖 ~/.adago）。
func newWorkspace(t *testing.T) (workspace, dataDir string) {
	t.Helper()
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("建工作区失败: %v", err)
	}
	return ws, filepath.Join(ws, ".adago")
}

// testConfig 用固定时间源与固定 id 源之外的默认值（时间用真实时钟，
// 因为 seq 连续性与 created_at 无关；需要确定性时用 withNow 覆盖）。
func testConfig(t *testing.T, workspace, dataDir string) Config {
	t.Helper()
	return Config{
		Workspace:        workspace,
		DataDir:          dataDir,
		WriterQueueSize:  64,
		UsageFlushWindow: 20 * time.Millisecond,
		CloseTimeout:     10 * time.Second,
	}
}

// openStore 打开 Core 句柄并注册清理。
func openStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	st, err := OpenCore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("OpenCore 失败: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close 失败: %v", err)
		}
	})
	return st
}

// mustThread 建线程并在失败时 t.Fatal。
func mustThread(t *testing.T, st *Store, title string) ports.Thread {
	t.Helper()
	th, err := st.CreateThread(context.Background(), st.Workspace(), title)
	if err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return th
}

// appendMessage 追加一条 message entry。
func appendMessage(t *testing.T, st *Store, threadID, parentID, text string) ports.Entry {
	t.Helper()
	e, err := st.AppendEntry(context.Background(), portsEntryFor(threadID, parentID, `{"text":`+quoteJSON(text)+`}`))
	if err != nil {
		t.Fatalf("AppendEntry 失败: %v", err)
	}
	return e
}

// portsEntryFor 造一条最小的 user message entry（测试夹具）。
func portsEntryFor(threadID, parentID, payload string) ports.Entry {
	return ports.Entry{
		ThreadID: threadID,
		ParentID: parentID,
		Kind:     domain.EntryMessage,
		Role:     domain.RoleUser,
		Payload:  json.RawMessage(payload),
	}
}

// quoteJSON 给字符串做最小 JSON 转义（测试夹具够用）。
func quoteJSON(s string) string {
	q := new(strings.Builder)
	q.WriteString(`"`)
	for _, r := range s {
		switch r {
		case '"':
			q.WriteString(`\"`)
		case '\\':
			q.WriteString(`\\`)
		default:
			q.WriteRune(r)
		}
	}
	q.WriteString(`"`)
	return q.String()
}
