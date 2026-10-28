// Package corerpc 是 Core 的 JSON-RPC 2.0 服务（M2 第一刀）。
//
// 它把已有的 coreproto dispatch 表接到 WebSocket 传输上，并把 cli.Session
// 的会话/轮次/交互能力暴露成协议方法。事件走 JSON-RPC notification（evt.*），
// 由领域事件的 threadId/seq 补全后广播给所有已连接客户端。
//
// 边界：本包不碰 moejs、SQLite SQL 或 HTTP 出网；这些都在装配/适配层。
package corerpc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/diag"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/ports"
	"github.com/adag/ada-go/internal/proto"
	"github.com/adag/ada-go/internal/version"
)

// Options 是 Core RPC 服务的装配输入。
type Options struct {
	Session *cli.Session
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
	sess    *cli.Session
	core    *coreproto.Server
	root    context.Context
	token   string
	readyAt time.Time
	onShut  func()
	pid     int

	mu    sync.Mutex
	runs  map[string]*runInfo // threadID -> 当前/最近一轮
	idem  map[string]string   // threadID + idemKey -> runID
	conns map[*client]struct{}
	sub   func()

	shutOnce sync.Once
}

type runInfo struct {
	threadID  string
	runID     string
	status    string // running | terminal
	terminal  string
	startedAt time.Time
	endedAt   time.Time
	cancel    context.CancelFunc
}

// New 创建并注册方法实现；未注册的方法继续走 coreproto 的失败关闭桩。
func New(opts Options) (*Server, error) {
	if opts.Session == nil {
		return nil, errors.New("corerpc: 需要已装配的 cli.Session")
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
		runs: map[string]*runInfo{}, idem: map[string]string{},
		conns: map[*client]struct{}{},
		root:  rootCtx,
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
		"usage.get":              s.handleUsageGet,
	}
	for name, h := range reg {
		if err := s.core.Register(name, h); err != nil {
			return err
		}
	}
	return nil
}

// ---- system ----

func (s *Server) handleHello(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ProtoVersion int    `json:"protoVersion"`
		ProductID    string `json:"productId"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("system.hello: " + err.Error())
	}
	if p.ProtoVersion != proto.ProtoVersion {
		return nil, invalid(fmt.Sprintf("system.hello: 协议版本 %d 与 Core %d 不一致", p.ProtoVersion, proto.ProtoVersion))
	}
	prod := s.sess.Product()
	pluginsOut := make([]proto.PluginSummary, 0, len(s.sess.Assembly().Plugins))
	for _, inst := range s.sess.Assembly().Plugins {
		pluginsOut = append(pluginsOut, pluginSummary(inst))
	}
	return proto.HelloResult{
		ProtoVersion: proto.ProtoVersion,
		APIVersion:   fmt.Sprintf("%d.%d", version.APIVersionMajor, version.APIVersionMinor),
		CoreVersion:  version.String(),
		ProductID:    prod.ID,
		Product:      productDeclaration(prod),
		Capabilities: proto.ProductCapabilities{
			Skills: prod.Capabilities.Skills, Images: prod.Capabilities.Images, Subagents: prod.Capabilities.Subagents,
		},
		Plugins:   pluginsOut,
		Workspace: s.workspaceInfo(),
		PID:       s.pid,
		ReadyAt:   s.readyAt.Format(time.RFC3339Nano),
	}, nil
}

func (s *Server) handlePing(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Echo string `json:"echo"`
	}
	_ = decode(raw, &p)
	return map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "pid": s.pid, "echo": p.Echo}, nil
}

func (s *Server) handleShutdown(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		TimeoutMS int `json:"timeoutMs"`
	}
	_ = decode(raw, &p)
	start := time.Now()
	if s.onShut != nil {
		go s.onShut()
	}
	return map[string]any{
		"flushed":        true,
		"runtimesClosed": len(s.sess.Assembly().Plugins),
		"durationMs":     time.Since(start).Milliseconds(),
	}, nil
}

func (s *Server) workspaceInfo() proto.WorkspaceInfo {
	return proto.WorkspaceInfo{
		InstanceID:   instanceID(s.sess.Product().ID, s.sess.Workspace()),
		Path:         s.sess.Workspace(),
		WorkspaceKey: s.sess.Store().Workspace(),
		DataDir:      s.sess.DataDir(),
		Product:      *productDeclaration(s.sess.Product()),
		PID:          s.pid,
	}
}

func (s *Server) handleWorkspaceInfo(context.Context, json.RawMessage) (any, error) {
	return s.workspaceInfo(), nil
}

func (s *Server) handleWorkspaceUpdatePolicy(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Policies *struct {
			ToolTimeoutMS   *int `json:"toolTimeoutMs"`
			TurnTimeoutMS   *int `json:"turnTimeoutMs"`
			MaxTurnTokens   *int `json:"maxTurnTokens"`
			LoopRepeatLimit *int `json:"loopRepeatLimit"`
			LoopAbortLimit  *int `json:"loopAbortLimit"`
			ParallelTools   *int `json:"parallelTools"`
		} `json:"policies"`
		Model *struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		} `json:"model"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("workspace.updatePolicy: " + err.Error())
	}
	var modelRef ports.ModelRef
	if p.Model != nil {
		ref, ok := s.sess.ResolveModel(p.Model.ProviderID, p.Model.ModelID)
		if !ok {
			return nil, invalid(fmt.Sprintf(
				"workspace.updatePolicy: provider %q/model %q 未在装配中声明", p.Model.ProviderID, p.Model.ModelID))
		}
		modelRef = ref
	}
	var up cli.PolicyUpdate
	if p.Policies != nil {
		up = cli.PolicyUpdate{
			ToolTimeoutMS: p.Policies.ToolTimeoutMS, TurnTimeoutMS: p.Policies.TurnTimeoutMS,
			MaxTurnTokens: p.Policies.MaxTurnTokens, LoopRepeatLimit: p.Policies.LoopRepeatLimit,
			LoopAbortLimit: p.Policies.LoopAbortLimit, ParallelTools: p.Policies.ParallelTools,
		}
	}
	policies, err := s.sess.UpdatePolicies(up)
	if err != nil {
		return nil, invalid("workspace.updatePolicy: " + err.Error())
	}
	if p.Model != nil {
		s.sess.SetWorkspaceModel(modelRef)
	}
	cur := s.sess.ModelRef()
	return map[string]any{
		"policies": policyJSON(policies),
		"model":    proto.ModelRef{ProviderID: cur.ProviderID, ModelID: cur.ModelID},
	}, nil
}

// ---- thread ----

func (s *Server) handleThreadCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Title string `json:"title"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.create: " + err.Error())
	}
	th, err := s.sess.CreateThread(ctx, p.Title)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Limit    int    `json:"limit"`
		Cursor   string `json:"cursor"`
		Archived bool   `json:"archived"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.list: " + err.Error())
	}
	page, err := s.sess.ListThreads(ctx, ports.ListThreadsRequest{
		Workspace: s.sess.Workspace(), Limit: p.Limit, Cursor: p.Cursor,
		IncludeArchived: true,
	})
	if err != nil {
		return nil, rpcError(err)
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, th := range page.Items {
		if th.Archived != p.Archived {
			continue
		}
		items = append(items, threadJSON(th))
	}
	return map[string]any{"threads": items, "nextCursor": page.NextCursor, "hasMore": page.NextCursor != ""}, nil
}

func (s *Server) handleThreadGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID  string `json:"threadId"`
		ItemLimit int    `json:"itemLimit"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("thread.get: 需要 threadId")
	}
	limit := p.ItemLimit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, invalid("thread.get: itemLimit 必须在 1..500")
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	page, err := s.sess.EntriesBefore(ctx, p.ThreadID, 0, int64(limit))
	if err != nil {
		return nil, rpcError(err)
	}
	pending := s.pending(ctx, p.ThreadID)
	items, hasMore := s.items(ctx, page.Items, pending, limit, page.NextCursor != "")
	return map[string]any{
		"thread":       threadJSON(th),
		"items":        items,
		"seq":          s.sess.LastSeq(p.ThreadID),
		"run":          s.runState(p.ThreadID),
		"interactions": interactionsJSON(pending),
		"hasMore":      hasMore,
	}, nil
}

func (s *Server) handleThreadEntries(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Cursor   string `json:"cursor"`
		Limit    int64  `json:"limit"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("thread.entries: 需要 threadId")
	}
	fromSeq, err := decodeSeqCursor(p.Cursor)
	if err != nil {
		return nil, invalid("thread.entries: " + err.Error())
	}
	page, err := s.sess.EntriesBefore(ctx, p.ThreadID, fromSeq, p.Limit)
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]map[string]any, 0, len(page.Items))
	for _, e := range page.Items {
		out = append(out, entryJSON(e))
	}
	return map[string]any{"entries": out, "nextCursor": page.NextCursor, "hasMore": page.NextCursor != ""}, nil
}

func (s *Server) handleThreadRename(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Title    string `json:"title"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.rename: " + err.Error())
	}
	if p.ThreadID == "" {
		return nil, invalid("thread.rename: 需要 threadId")
	}
	if strings.TrimSpace(p.Title) == "" {
		return nil, invalid("thread.rename: title 不能为空")
	}
	if len([]rune(p.Title)) > 200 {
		return nil, invalid("thread.rename: title 不能超过 200 字符")
	}
	if err := s.sess.Store().RenameThread(ctx, p.ThreadID, p.Title); err != nil {
		return nil, rpcError(err)
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadArchive(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Archived *bool  `json:"archived"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.archive: " + err.Error())
	}
	if p.ThreadID == "" || p.Archived == nil {
		return nil, invalid("thread.archive: 需要 threadId 与 archived")
	}
	if err := s.sess.Store().SetArchived(ctx, p.ThreadID, *p.Archived); err != nil {
		return nil, rpcError(err)
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadBranch(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		EntryID  string `json:"entryId"`
		Title    string `json:"title"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.branch: " + err.Error())
	}
	if p.ThreadID == "" || p.EntryID == "" {
		return nil, invalid("thread.branch: 需要 threadId 与 entryId")
	}
	if len([]rune(p.Title)) > 200 {
		return nil, invalid("thread.branch: title 不能超过 200 字符")
	}
	if _, err := s.sess.GetThread(ctx, p.ThreadID); err != nil {
		return nil, rpcError(err)
	}
	entry, err := s.sess.Store().GetEntry(ctx, p.EntryID)
	if err != nil {
		return nil, rpcError(err)
	}
	if entry.ThreadID != p.ThreadID {
		return nil, invalid("thread.branch: entry 不属于该线程")
	}
	if p.Title != "" {
		if err := s.sess.Store().RenameThread(ctx, p.ThreadID, p.Title); err != nil {
			return nil, rpcError(err)
		}
	}
	if err := s.sess.Store().MoveHead(ctx, p.ThreadID, p.EntryID); err != nil {
		return nil, rpcError(err)
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("thread.delete: 需要 threadId")
	}
	if err := s.sess.Store().DeleteThread(ctx, p.ThreadID); err != nil {
		return nil, rpcError(err)
	}
	s.sess.ForgetThreadModel(p.ThreadID)
	return map[string]any{"deleted": true, "cleanupScheduled": true}, nil
}

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
	if len(p.Images) > 0 {
		return nil, invalid("turn.start: M2 第一切片暂不支持图片输入")
	}
	if strings.TrimSpace(p.Text) == "" {
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
	if runID, ok := s.idem[key]; ok {
		s.mu.Unlock()
		return map[string]any{"runId": runID, "duplicate": true}, nil
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
	s.idem[key] = runID
	s.mu.Unlock()

	// 等轮次登记完活跃状态再返回 turn.start：紧随其后的 turn.steer 不会被
	// 误判成「没有运行中轮次」。登记/注销由 RunInThread 成对完成。
	ready := make(chan struct{})
	go func() {
		term, err := s.sess.RunInThreadReady(runCtx, p.ThreadID, runID, p.Text, ready)
		s.mu.Lock()
		if cur := s.runs[p.ThreadID]; cur != nil && cur.runID == runID {
			cur.status = "terminal"
			cur.terminal = string(term)
			cur.endedAt = time.Now().UTC()
			cur.cancel = nil
			if err != nil {
				cur.terminal = string(domain.TerminalError)
			}
		}
		s.mu.Unlock()
		cancel()
	}()
	<-ready
	return map[string]any{"runId": runID, "duplicate": false, "seq": 0}, nil
}

func (s *Server) handleTurnCancel(_ context.Context, raw json.RawMessage) (any, error) {
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
// 对齐。images 在 M2 第一切片暂不支持，非空按 invalid_params 拒绝。
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
	if len(p.Images) > 0 {
		return nil, invalid("turn.steer: M2 第一切片暂不支持图片输入")
	}
	if strings.TrimSpace(p.Text) == "" {
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

	position, err := s.sess.EnqueueSteering(threadID, p.Text)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"queued": true, "position": position}, nil
}

// ---- interaction ----

func (s *Server) handleInteractionList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = decode(raw, &p)
	return map[string]any{"interactions": interactionsJSON(s.pending(ctx, p.ThreadID))}, nil
}

func (s *Server) handleInteractionRespond(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID  string         `json:"threadId"`
		RequestID string         `json:"requestId"`
		Decision  string         `json:"decision"`
		Value     map[string]any `json:"value"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" || p.RequestID == "" {
		return nil, invalid("interaction.respond: 需要 threadId 与 requestId")
	}
	status, value, err := decisionOf(p.Decision, p.Value)
	if err != nil {
		return nil, err
	}
	res, err := s.sess.RespondInteraction(ctx, p.ThreadID, p.RequestID, status, value)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{
		"interactionId": res.RequestID,
		"threadId":      p.ThreadID,
		"status":        string(res.Status),
		"response":      map[string]any{"value": res.Value},
		"resolvedAt":    res.ResolvedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// ---- model/provider/tool/plugin ----

func (s *Server) handleModelList(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = decode(raw, &p)
	asm := s.sess.Assembly()
	models := []map[string]any{}
	if asm.Providers != nil {
		for _, pid := range asm.Providers.Providers() {
			for _, m := range asm.Providers.Models(pid) {
				models = append(models, map[string]any{
					"model": proto.ModelRef{ProviderID: pid, ModelID: m.ID},
					"info":  modelInfo(m),
				})
			}
		}
	}
	cur := s.sess.ModelRef()
	if p.ThreadID != "" {
		cur = s.sess.ModelRefFor(p.ThreadID)
	}
	return map[string]any{
		"models":  models,
		"current": proto.ModelRef{ProviderID: cur.ProviderID, ModelID: cur.ModelID},
	}, nil
}

func (s *Server) handleModelSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Model *struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		} `json:"model"`
		ThreadID string `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("model.set: " + err.Error())
	}
	if p.Model == nil || p.Model.ProviderID == "" || p.Model.ModelID == "" {
		return nil, invalid("model.set: 需要 model.providerId 与 model.modelId")
	}
	ref, ok := s.sess.ResolveModel(p.Model.ProviderID, p.Model.ModelID)
	if !ok {
		return nil, invalid(fmt.Sprintf(
			"model.set: provider %q/model %q 未在装配中声明", p.Model.ProviderID, p.Model.ModelID))
	}
	scope := "workspace"
	if p.ThreadID != "" {
		if _, err := s.sess.GetThread(ctx, p.ThreadID); err != nil {
			return nil, rpcError(err)
		}
		s.sess.SetThreadModel(p.ThreadID, ref)
		scope = "thread"
	} else {
		s.sess.SetWorkspaceModel(ref)
	}
	return map[string]any{
		"model": proto.ModelRef{ProviderID: ref.ProviderID, ModelID: ref.ModelID},
		"scope": scope,
	}, nil
}

func (s *Server) handleProviderList(context.Context, json.RawMessage) (any, error) {
	asm := s.sess.Assembly()
	out := []map[string]any{}
	if asm.Providers != nil {
		for _, pid := range asm.Providers.Providers() {
			models := []proto.ModelInfo{}
			for _, m := range asm.Providers.Models(pid) {
				models = append(models, modelInfo(m))
			}
			out = append(out, map[string]any{
				"id": pid, "displayName": pid, "baseUrl": s.sess.BaseURL(),
				"active": true, "models": models,
			})
		}
	}
	return map[string]any{"providers": out}, nil
}

func (s *Server) handleToolList(ctx context.Context, _ json.RawMessage) (any, error) {
	descs := s.sess.Assembly().Tools.List(ctx)
	out := make([]proto.ToolDescriptor, 0, len(descs))
	for _, d := range descs {
		out = append(out, proto.ToolDescriptor{
			Name: d.Name, Description: d.Description, Parameters: d.Parameters,
			Access: string(d.Access), TimeoutMS: int(d.Timeout / time.Millisecond),
			ParallelSafe: d.ParallelSafe, Rollback: d.Rollback,
			TerminatesTurn: d.TerminatesTurn, PluginID: d.PluginID,
		})
	}
	return map[string]any{"tools": out}, nil
}

func (s *Server) handlePluginList(context.Context, json.RawMessage) (any, error) {
	out := []proto.PluginSummary{}
	for _, inst := range s.sess.Assembly().Plugins {
		out = append(out, pluginSummary(inst))
	}
	return map[string]any{"plugins": out}, nil
}

// ---- usage ----

func (s *Server) handleUsageGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Since    string `json:"since"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("usage.get: " + err.Error())
	}
	var since time.Time
	if strings.TrimSpace(p.Since) != "" {
		t, err := time.Parse(time.RFC3339Nano, p.Since)
		if err != nil {
			if t, err = time.Parse(time.RFC3339, p.Since); err != nil {
				return nil, invalid("usage.get: since 必须是 RFC3339 时间")
			}
		}
		since = t
	}
	st := s.sess.Store()
	// usage 走合并窗口；先强制落库，否则刚结束的轮次在聚合里可能还看不到。
	if err := st.Flush(ctx); err != nil {
		return nil, rpcError(err)
	}
	threadUsage := proto.Usage{}
	if p.ThreadID != "" {
		if since.IsZero() {
			aggs, err := st.UsageByThread(ctx, p.ThreadID)
			if err != nil {
				return nil, rpcError(err)
			}
			threadUsage = usageFromAggregates(aggs)
		} else {
			u, err := s.usageSince(ctx, []string{p.ThreadID}, since)
			if err != nil {
				return nil, rpcError(err)
			}
			threadUsage = u
		}
	}
	var workspace proto.Usage
	if since.IsZero() {
		aggs, err := st.UsageWorkspace(ctx)
		if err != nil {
			return nil, rpcError(err)
		}
		workspace = usageFromAggregates(aggs)
	} else {
		ids, err := s.threadIDs(ctx)
		if err != nil {
			return nil, rpcError(err)
		}
		workspace, err = s.usageSince(ctx, ids, since)
		if err != nil {
			return nil, rpcError(err)
		}
	}
	return map[string]any{"thread": threadUsage, "workspace": workspace}, nil
}

// usageSince 用 runs.usage_json + started_at 做时间窗聚合。
//
// 04 §5.7 的 usage 表读取端口只提供线程/工作区全量聚合（UsageByThread/UsageWorkspace），
// 没有 since 参数；runs 表保存了每轮的使用量快照与开始时间，因此时间窗路径从 runs 汇总。
func (s *Server) usageSince(ctx context.Context, threadIDs []string, since time.Time) (proto.Usage, error) {
	groups := map[[2]string]*sqlite.UsageAggregate{}
	for _, tid := range threadIDs {
		runs, err := s.sess.Store().ListRuns(ctx, tid, 100000)
		if err != nil {
			return proto.Usage{}, err
		}
		ref := s.sess.ModelRefFor(tid)
		for _, run := range runs {
			if run.StartedAt.Before(since) || len(run.UsageJSON) == 0 {
				continue
			}
			var u domain.Usage
			if err := json.Unmarshal(run.UsageJSON, &u); err != nil || u == (domain.Usage{}) {
				continue
			}
			key := [2]string{ref.ProviderID, ref.ModelID}
			agg := groups[key]
			if agg == nil {
				agg = &sqlite.UsageAggregate{Provider: key[0], Model: key[1]}
				groups[key] = agg
			}
			agg.Input += u.InputTokens
			agg.Output += u.OutputTokens
			agg.Cache += u.CacheTokens
			agg.Cost += u.CostMicros
		}
	}
	out := make([]sqlite.UsageAggregate, 0, len(groups))
	for _, agg := range groups {
		out = append(out, *agg)
	}
	return usageFromAggregates(out), nil
}

func (s *Server) threadIDs(ctx context.Context) ([]string, error) {
	var ids []string
	cursor := ""
	for {
		page, err := s.sess.ListThreads(ctx, ports.ListThreadsRequest{
			Workspace: s.sess.Workspace(), Limit: 200, Cursor: cursor, IncludeArchived: true,
		})
		if err != nil {
			return nil, err
		}
		for _, th := range page.Items {
			ids = append(ids, th.ID)
		}
		if page.NextCursor == "" {
			return ids, nil
		}
		cursor = page.NextCursor
	}
}

func usageFromAggregates(aggs []sqlite.UsageAggregate) proto.Usage {
	var out proto.Usage
	providers := map[string]struct{}{}
	models := map[string]struct{}{}
	for _, a := range aggs {
		out.InputTokens += int(a.Input)
		out.OutputTokens += int(a.Output)
		out.CacheTokens += int(a.Cache)
		out.CostMicros += a.Cost
		providers[a.Provider] = struct{}{}
		models[a.Model] = struct{}{}
	}
	// 单一 provider/model 时带出归属；多组混用时留空，避免把合计伪装成某一组。
	if len(providers) == 1 {
		for p := range providers {
			out.Provider = p
		}
	}
	if len(models) == 1 {
		for m := range models {
			out.Model = m
		}
	}
	return out
}

// ---- helpers ----

// projectedItem 是 item 投影的中间形状；payload 保持 entry 原文。
type projectedItem struct {
	key         string
	typ         string
	at          time.Time
	payload     json.RawMessage
	fingerprint string
}

// items 把最近一批 entry 与待处理交互投影成 Item（按 at 升序，最多 limit 条）。
// hasMore 为 true 表示窗口外仍有更早 entry（或交互被窗口截掉）。
func (s *Server) items(_ context.Context, entries []ports.Entry, pending []ports.InteractionRequest, limit int, entriesHasMore bool) ([]map[string]any, bool) {
	all := make([]projectedItem, 0, len(entries)+len(pending))
	for _, e := range entries {
		if it, ok := projectEntry(e); ok {
			all = append(all, it)
		}
	}
	for _, r := range pending {
		if it, ok := projectInteraction(r); ok {
			all = append(all, it)
		}
	}
	total := len(all)
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	if len(all) > limit {
		all = all[total-limit:]
	}
	out := make([]map[string]any, 0, len(all))
	for _, it := range all {
		out = append(out, map[string]any{
			"key": it.key, "type": it.typ,
			"at":          it.at.UTC().Format(time.RFC3339Nano),
			"fingerprint": it.fingerprint,
			"payload":     it.payload,
		})
	}
	return out, entriesHasMore || total > limit
}

func projectEntry(e ports.Entry) (projectedItem, bool) {
	payload := e.Payload
	if len(payload) == 0 || !json.Valid(payload) {
		payload = json.RawMessage("{}")
	}
	var typ, key string
	switch e.Kind {
	case domain.EntryMessage:
		if e.Role == domain.RoleTool {
			var body struct {
				CallID string `json:"callId"`
			}
			_ = json.Unmarshal(payload, &body)
			key = "tool:" + body.CallID
			if body.CallID == "" {
				key = "tool:" + e.ID
			}
			typ = "tool"
		} else {
			key = "msg:" + e.ID
			typ = "message"
		}
	case domain.EntryCompaction:
		key = "compaction:" + e.ID
		typ = "compaction"
	default:
		return projectedItem{}, false
	}
	return projectedItem{key: key, typ: typ, at: e.CreatedAt, payload: payload, fingerprint: fingerprintPayload(payload)}, true
}

func projectInteraction(r ports.InteractionRequest) (projectedItem, bool) {
	m := map[string]any{
		"id": r.ID, "threadId": r.ThreadID, "runId": r.RunID,
		"pluginId": r.PluginID, "kind": string(r.Kind), "title": r.Title,
		"body": r.Body, "status": string(domain.InteractionPending),
		"createdAt": r.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if r.CallID != "" {
		m["callId"] = r.CallID
	}
	if len(r.Options) > 0 {
		m["options"] = r.Options
	}
	if !r.ExpiresAt.IsZero() {
		m["expiresAt"] = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return projectedItem{}, false
	}
	return projectedItem{key: "interaction:" + r.ID, typ: "interaction", at: r.CreatedAt,
		payload: payload, fingerprint: fingerprintPayload(payload)}, true
}

func fingerprintPayload(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

func policyJSON(p coreapp.Policies) map[string]any {
	return map[string]any{
		"toolTimeoutMs": p.ToolTimeoutMS, "turnTimeoutMs": p.TurnTimeoutMS,
		"maxTurnTokens": p.MaxTurnTokens, "loopRepeatLimit": p.LoopRepeatLimit,
		"loopAbortLimit": p.LoopAbortLimit, "parallelTools": p.ParallelTools,
	}
}

func (s *Server) pending(ctx context.Context, threadID string) []ports.InteractionRequest {
	var all []ports.InteractionRequest
	if threadID != "" {
		list, err := s.sess.PendingInteractions(ctx, threadID)
		if err == nil {
			return list
		}
		return nil
	}
	// 未给 threadId 时列本工作区全部线程的 pending。
	page, err := s.sess.ListThreads(ctx, ports.ListThreadsRequest{Workspace: s.sess.Workspace(), Limit: 200, IncludeArchived: true})
	if err != nil {
		return nil
	}
	for _, th := range page.Items {
		list, err := s.sess.PendingInteractions(ctx, th.ID)
		if err == nil {
			all = append(all, list...)
		}
	}
	return all
}

func (s *Server) runState(threadID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runStateLocked(threadID)
}

func (s *Server) runStateLocked(threadID string) map[string]any {
	cur := s.runs[threadID]
	if cur == nil {
		return map[string]any{"runId": "", "status": "terminal", "terminal": domain.TerminalCancelled, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	}
	return runStateJSON(cur)
}

func runStateJSON(r *runInfo) map[string]any {
	out := map[string]any{
		"runId": r.runID, "status": r.status,
		"startedAt": r.startedAt.Format(time.RFC3339Nano),
	}
	if r.status == "terminal" {
		out["terminal"] = r.terminal
	}
	if !r.endedAt.IsZero() {
		out["endedAt"] = r.endedAt.Format(time.RFC3339Nano)
	}
	return out
}

func threadJSON(t ports.Thread) map[string]any {
	out := map[string]any{
		"id": t.ID, "workspace": t.Workspace, "title": t.Title,
		"createdAt": t.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updatedAt": t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"archived":  t.Archived,
	}
	if t.HeadEntryID != "" {
		out["headEntryId"] = t.HeadEntryID
	}
	return out
}

func entryJSON(e ports.Entry) map[string]any {
	out := map[string]any{
		"id": e.ID, "threadId": e.ThreadID, "seq": e.Seq,
		"kind": string(e.Kind), "payload": e.Payload, "tokens": e.Tokens,
		"createdAt": e.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if e.ParentID != "" {
		out["parentId"] = e.ParentID
	}
	if e.Role != "" {
		out["role"] = string(e.Role)
	}
	return out
}

func interactionsJSON(reqs []ports.InteractionRequest) []map[string]any {
	out := make([]map[string]any, 0, len(reqs))
	for _, r := range reqs {
		m := map[string]any{
			"id": r.ID, "threadId": r.ThreadID, "runId": r.RunID,
			"pluginId": r.PluginID, "kind": string(r.Kind), "title": r.Title,
			"body": r.Body, "status": string(domain.InteractionPending),
			"createdAt": r.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if r.CallID != "" {
			m["callId"] = r.CallID
		}
		if len(r.Options) > 0 {
			m["options"] = r.Options
		}
		if !r.ExpiresAt.IsZero() {
			m["expiresAt"] = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, m)
	}
	return out
}

func productDeclaration(p *coreapp.Product) *proto.ProductDeclaration {
	return &proto.ProductDeclaration{
		ID: p.ID, Name: p.Name, Plugins: append([]string(nil), p.Plugins...),
		Capabilities: proto.ProductCapabilities{
			Skills: p.Capabilities.Skills, Images: p.Capabilities.Images, Subagents: p.Capabilities.Subagents,
		},
		Identity: proto.ProductIdentity{Name: p.Identity.Name, Locale: p.Identity.Locale},
		Policies: proto.ProductPolicies{
			ToolTimeoutMS: p.Policies.ToolTimeoutMS, TurnTimeoutMS: p.Policies.TurnTimeoutMS,
			MaxTurnTokens: p.Policies.MaxTurnTokens, LoopRepeatLimit: p.Policies.LoopRepeatLimit,
			LoopAbortLimit: p.Policies.LoopAbortLimit, ParallelTools: p.Policies.ParallelTools,
		},
	}
}

func modelInfo(m host.ModelInfo) proto.ModelInfo {
	return proto.ModelInfo{
		ID: m.ID, DisplayName: m.DisplayName, ContextWindow: m.ContextWindow,
		MaxOutput: m.MaxOutput, Input: append([]string(nil), m.Input...),
		SupportsTools: m.SupportsTools, SupportsThinking: m.SupportsThinking,
	}
}

func pluginSummary(inst interface {
	ID() string
	Manifest() *plugins.Manifest
	Summary() plugins.Summary
}) proto.PluginSummary {
	sum := inst.Summary()
	return proto.PluginSummary{
		ID: sum.ID, Name: sum.Name, Version: sum.Version,
		APIVersion: inst.Manifest().APIVersion, Trust: sum.Trust,
		Enabled: sum.Enabled, Status: sum.Status,
		Fingerprint: sum.Fingerprint, LastError: sum.LastError,
		Contributions: map[string]any{"tools": sum.Tools, "hooks": sum.Hooks, "slots": sum.Slots},
	}
}

func decisionOf(decision string, value map[string]any) (domain.InteractionStatus, string, error) {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "accept":
		return domain.InteractionAccepted, valueString(value), nil
	case "deny":
		return domain.InteractionDenied, valueString(value), nil
	case "cancel":
		return domain.InteractionCancelled, "", nil
	default:
		return "", "", invalid("interaction.respond: decision 只能是 accept/deny/cancel")
	}
}

func valueString(value map[string]any) string {
	for _, key := range []string{"option", "text", "value"} {
		if v, ok := value[key]; ok {
			return fmt.Sprint(v)
		}
	}
	if len(value) == 0 {
		return "true"
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func decode(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func invalid(msg string) *proto.RPCError {
	return &proto.RPCError{Code: coreproto.ErrInvalidParams, Message: msg}
}

func rpcError(err error) *proto.RPCError {
	if err == nil {
		return nil
	}
	var re *proto.RPCError
	if errors.As(err, &re) {
		return re
	}
	var de *domain.Error
	if errors.As(err, &de) {
		data := &proto.ErrorData{Name: de.Name, Retryable: domain.IsRetryable(de.Code)}
		if entry, ok := diag.For(int(de.Code)); ok {
			data.Diagnostic = entry.Diag.String()
			data.NextStep = entry.NextStep
		}
		return &proto.RPCError{Code: int(de.Code), Message: de.Detail, Data: data}
	}
	return &proto.RPCError{Code: coreproto.ErrInternal, Message: err.Error()}
}

func instanceID(product, workspace string) string {
	base := workspace
	if len(base) > 0 {
		base = base[strings.LastIndexAny(base, `/\`)+1:]
	}
	return product + "::" + base
}

func decodeSeqCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, errors.New("游标无法解码")
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("游标形状非法")
	}
	return n, nil
}
