// Package corerpc 是 Core 的 JSON-RPC 2.0 服务（M2 第一刀）。
//
// 它把已有的 coreproto dispatch 表接到 WebSocket 传输上，并把 Session 的会话/轮次/交互能力
// 暴露成协议方法。事件走 JSON-RPC notification（evt.*），
// 由领域事件的 threadId/seq 补全后广播给所有已连接客户端。
//
// 边界：本包不碰 moejs、SQLite SQL 或 HTTP 出网；这些都在装配/适配层。
package corerpc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/agent_core/coreproto"
	"github.com/adag/ada-go/internal/common/domain"
)

// Options 是 Core RPC 服务的装配输入。
type Options struct {
	Session Session
	// Context 是服务根 context；轮次从它派生，Shutdown 时统一取消。
	Context context.Context
	// Token 非空时要求 Authorization: Bearer 或 ?token=；空表示只依赖回环绑定。
	Token string
	// ReadyAt 是 Core 就绪时刻；零值取 New 的时间。
	ReadyAt time.Time
	// OnShutdown 由 system.shutdown 调用（cmd 侧用它取消服务 ctx 并优雅退出）。
	OnShutdown func()
}

// Server 持有方法表、轮次账目与事件订阅。
type Server struct {
	sess    Session
	core    *coreproto.Server
	root    context.Context
	token   string
	readyAt time.Time
	onShut  func()
	pid     int

	mu    sync.Mutex
	runs  map[string]*runInfo  // threadID -> 当前/最近一轮
	idem  map[string]idemEntry // threadID + idemKey -> runID（带登记时刻，超期清理）
	conns map[*client]struct{}
	sub   func()

	streamMu sync.Mutex
	streams  map[string]*pluginStream

	shutOnce sync.Once
}

// pluginStream 是 Gateway 与 Core 之间的一次 SSE 路由登记（prepare → start → cancel）。
type pluginStream struct {
	cancel   context.CancelFunc
	pluginID string
	method   string
	path     string
	threadID string
	req      json.RawMessage
	started  bool
	prepared time.Time
}

type runInfo struct {
	threadID    string
	runID       string
	status      string // running | terminal
	terminal    string
	errorCode   int
	errorDetail string
	startedAt   time.Time
	endedAt     time.Time
	// usage 是本轮累计用量（来自 evt.usage 增量）。输入框下方的
	// 「tok/s」需要它：输出 token 数 ÷ 已运行秒数。
	usage  domain.Usage
	cancel context.CancelFunc
}

// idemEntry 是幂等键的登记：runID + 登记时刻。终态后的保留期内重试仍返回
// 同一 runId（客户端双击/重发重试语义），超期即视为新提交。
type idemEntry struct {
	runID string
	at    time.Time
}

// idemRetention 是幂等键的保留时长；它同时给 idem map 一个有界上界——
// 此前该 map 只增不减，长跑 Core 会随历史轮次数单调增长。
var idemRetention = time.Hour

// pruneIdemLocked 删除超过保留期的幂等键（调用方持有 s.mu）。
// 只有 map 变大时才做 O(n) 扫描：turn.start 每次用户消息至多发生一次，成本可忽略。
func pruneIdemLocked(m map[string]idemEntry, now time.Time) {
	if len(m) < 512 {
		return
	}
	for key, ent := range m {
		if now.Sub(ent.at) > idemRetention {
			delete(m, key)
		}
	}
}

// New 创建并注册方法实现；未注册的方法继续走 coreproto 的失败关闭桩。
func New(opts Options) (*Server, error) {
	if opts.Session == nil {
		return nil, errors.New("corerpc: 需要已装配的 Session")
	}
	readyAt := opts.ReadyAt
	if readyAt.IsZero() {
		readyAt = time.Now().UTC()
	}
	rootCtx := opts.Context
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	p := opts.Session.Product()
	s := &Server{
		sess:  opts.Session,
		core:  coreproto.NewServer(p.ID, instanceID(p.ID, opts.Session.Workspace()), os.Getpid()),
		token: opts.Token, readyAt: readyAt, onShut: opts.OnShutdown,
		runs: map[string]*runInfo{}, idem: map[string]idemEntry{},
		conns: map[*client]struct{}{}, streams: map[string]*pluginStream{},
		root: rootCtx,
	}
	s.pid = os.Getpid()
	if err := s.register(); err != nil {
		return nil, err
	}
	s.sub = opts.Session.Subscribe(s.broadcast)
	return s, nil
}

// Close 取消事件订阅并断开所有客户端。
func (s *Server) Close() {
	s.shutOnce.Do(func() {
		if s.sub != nil {
			s.sub()
		}
		s.mu.Lock()
		conns := make([]*client, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.conns = map[*client]struct{}{}
		s.mu.Unlock()
		s.streamMu.Lock()
		streams := make([]*pluginStream, 0, len(s.streams))
		for id, st := range s.streams {
			streams = append(streams, st)
			delete(s.streams, id)
		}
		s.streamMu.Unlock()
		for _, st := range streams {
			if st.cancel != nil {
				st.cancel()
			}
		}
		for _, c := range conns {
			c.close()
		}
	})
}

// Core 返回底层 dispatch 表（verify/测试用）。
func (s *Server) Core() *coreproto.Server { return s.core }

func (s *Server) register() error {
	reg := map[string]coreproto.Handler{
		"system.hello":           s.handleHello,
		"system.ping":            s.handlePing,
		"system.shutdown":        s.handleShutdown,
		"workspace.info":         s.handleWorkspaceInfo,
		"workspace.updatePolicy": s.handleWorkspaceUpdatePolicy,
		"thread.create":          s.handleThreadCreate,
		"thread.list":            s.handleThreadList,
		"thread.get":             s.handleThreadGet,
		"thread.entries":         s.handleThreadEntries,
		"thread.rename":          s.handleThreadRename,
		"thread.archive":         s.handleThreadArchive,
		"thread.branch":          s.handleThreadBranch,
		"thread.delete":          s.handleThreadDelete,
		"turn.start":             s.handleTurnStart,
		"turn.cancel":            s.handleTurnCancel,
		"turn.get":               s.handleTurnGet,
		"turn.steer":             s.handleTurnSteer,
		"interaction.list":       s.handleInteractionList,
		"interaction.respond":    s.handleInteractionRespond,
		"model.list":             s.handleModelList,
		"model.set":              s.handleModelSet,
		"provider.list":          s.handleProviderList,
		"tool.list":              s.handleToolList,
		"plugin.list":            s.handlePluginList,
		"plugin.reload":          s.handlePluginReload,
		"plugin.setEnabled":      s.handlePluginSetEnabled,
		"plugin.http":            s.handlePluginHTTP,
		"plugin.httpStream":      s.handlePluginHTTPStream,
		"action.invoke":          s.handleActionInvoke,
		"skill.list":             s.handleSkillList,
		"change.list":            s.handleChangeList,
		"change.revert":          s.handleChangeRevert,
	}
	for name, h := range reg {
		if err := s.core.Register(name, h); err != nil {
			return err
		}
	}
	return nil
}

// 各协议域的 handler 见 handlers_*.go；条目投影与 JSON 转换见 projection.go，
// RPC 错误/解码小工具见 helpers.go（纯搬移拆分，无语义变更）。
