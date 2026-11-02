package corerpc_test

// 回归：运行中的轮次 + plugin.reload 不能把 Core 的 RPC 连接堵死。
//
// 曾经的实现是串行读循环（读一条 → Handle 一条 → 再读下一条）。plugin.reload 在轮次
// 运行期间要等在途轮次排空、再关掉旧装配的插件运行时，这段时间里同一条连接上的
// interaction.respond / turn.cancel / thread.get 全部排队，界面表现为「点重载就卡死」；
// 最坏情况是真死锁：关旧装配要等插件的 owner goroutine 退出，它正卡在 ask_user 里等
// 回答，而回答必须走 interaction.respond——排在重载后面，永远送不进来。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/adag/ada-go/internal/ports"
)

// blockingModel 的流一直阻塞到 ctx 取消：让轮次保持 running，模拟「会话正在跑」。
type blockingModel struct{ entered chan struct{} }

func (m *blockingModel) Stream(ctx context.Context, _ ports.Request) (ports.Stream, error) {
	return &blockingStream{ctx: ctx, entered: m.entered}, nil
}

type blockingStream struct {
	ctx     context.Context
	entered chan struct{}
}

func (s *blockingStream) Recv() (ports.Delta, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.ctx.Done()
	return ports.Delta{}, s.ctx.Err()
}

func (s *blockingStream) Close() error { return nil }

// sendAsync 发一个请求但不等响应（roundTrip 的 t.Fatalf 不能用在 goroutine 里）。
func (c *rpcClient) sendAsync(method string, params any) chan wireResponse {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan wireResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	raw, _ := json.Marshal(params)
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(raw)})
	_ = c.conn.Write(context.Background(), websocket.MessageText, req)
	return ch
}

func TestPluginReloadDuringTurnKeepsRPCResponsive(t *testing.T) {
	model := &blockingModel{entered: make(chan struct{}, 1)}
	sess := newSession(t, model)
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")

	var created struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(c.call(t, "thread.create", map[string]any{"title": "重载测试"}), &created); err != nil {
		t.Fatal(err)
	}
	threadID := created.Thread.ID
	if threadID == "" {
		t.Fatal("thread.create 没有返回 id")
	}

	c.call(t, "turn.start", map[string]any{
		"threadId": threadID, "idempotencyKey": "reload-test-1", "text": "hi",
	})
	c.waitEvent(t, "evt.run.status", "running")
	select {
	case <-model.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("模型流没有启动，轮次没跑起来")
	}

	// 重载会等在途轮次排空（上限 30s）；它挂着的这段时间，同一连接上的其它请求必须照常响应。
	reloadCh := c.sendAsync("plugin.reload", map[string]any{})
	select {
	case resp := <-reloadCh:
		t.Fatalf("plugin.reload 不应在轮次运行中立刻返回: %+v", resp)
	case <-time.After(300 * time.Millisecond):
	}

	getCh := c.sendAsync("thread.get", map[string]any{"threadId": threadID, "itemLimit": 20})
	select {
	case resp := <-getCh:
		if len(resp.Error) > 0 && string(resp.Error) != "null" {
			t.Fatalf("thread.get 在重载期间失败: %s", resp.Error)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("重载期间同一连接上的 thread.get 被堵住：界面会表现为「点重载就卡死」")
	}

	// 停掉轮次，让排空结束、重载收尾（这一步同样不能被堵住）。
	c.call(t, "turn.cancel", map[string]any{"threadId": threadID})
	select {
	case <-reloadCh:
	case <-time.After(15 * time.Second):
		t.Fatal("轮次结束后 plugin.reload 仍未返回")
	}
}
