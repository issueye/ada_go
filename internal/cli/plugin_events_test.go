package cli

// 插件事件投影：只读 Emitter 缓冲、按类型/条数过滤。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
)

func TestQueryPluginEvents(t *testing.T) {
	em := coreproto.NewEmitter(nil)
	s := &Session{emitter: em}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		kind := domain.EventKind("evt.usage")
		if i == 2 {
			kind = "evt.error"
		}
		if err := em.Emit(ctx, domain.Event{Kind: kind, ThreadID: "th_1", Payload: json.RawMessage(`{"n":1}`)}); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.queryPluginEvents(ctx, host.EventQuery{ThreadID: "th_1", Types: []string{"evt.usage"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Type != "evt.usage" || views[0].Seq == 0 {
		t.Fatalf("views = %+v", views)
	}
	if _, err := s.queryPluginEvents(ctx, host.EventQuery{ThreadID: "th_2", Limit: 1}); err != nil {
		t.Fatalf("空线程应返回空而不是错误: %v", err)
	}
}
