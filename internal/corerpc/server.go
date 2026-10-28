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
		"plugin.reload":          s.handlePluginReload,
		"plugin.setEnabled":      s.handlePluginSetEnabled,
		"action.invoke":          s.handleActionInvoke,
		"skill.list":             s.handleSkillList,
		"skill.read":             s.handleSkillRead,
		"usage.get":              s.handleUsageGet,
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

// handlePluginList 列出随包 + 第三方插件（M3）。
// 第三方行带 enabled/status/fingerprint/source(workspace|global)；includeDisabled
// 缺省 true（协议默认值），显式传 false 时跳过被用户禁用的插件。
func (s *Server) handlePluginList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		IncludeDisabled *bool `json:"includeDisabled"`
	}
	_ = decode(raw, &p)
	views := s.sess.PluginViews(ctx)
	out := make([]proto.PluginSummary, 0, len(views))
	for _, v := range views {
		if v.Status == "disabled" && p.IncludeDisabled != nil && !*p.IncludeDisabled {
			continue
		}
		out = append(out, pluginViewJSON(v))
	}
	return map[string]any{"plugins": out}, nil
}

// handlePluginReload 重载一个或全部插件（双代原子替换；在途调用用旧代，6.9）。
func (s *Server) handlePluginReload(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		PluginID string `json:"pluginId"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("plugin.reload: " + err.Error())
	}
	views, err := s.sess.ReloadPlugins(ctx, strings.TrimSpace(p.PluginID))
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]proto.PluginSummary, 0, len(views))
	for _, v := range views {
		out = append(out, pluginViewJSON(v))
	}
	return map[string]any{"plugins": out}, nil
}

// handlePluginSetEnabled 启用/禁用插件并热重载；持久化到 plugin_meta，
// 重启后仍生效。禁用唯一策略插件且存在写/执行工具时拒绝（plugin_denied）。
func (s *Server) handlePluginSetEnabled(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		PluginID string `json:"pluginId"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("plugin.setEnabled: " + err.Error())
	}
	if strings.TrimSpace(p.PluginID) == "" || p.Enabled == nil {
		return nil, invalid("plugin.setEnabled: 需要 pluginId 与 enabled")
	}
	view, err := s.sess.SetPluginEnabled(ctx, p.PluginID, *p.Enabled)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"plugin": pluginViewJSON(view)}, nil
}

// ---- action（M4：前端动作 → 插件后端动作）----

// handleActionInvoke 实现 action.invoke：ActionEnvelope 进，{ok, result|error} 出。
// 超时/崩溃/拒绝等插件侧失败按协议收敛成 ok=false + 人话错误（不是 RPC 异常），
// 让界面可以就地显示；参数缺失才是 invalid_params。
func (s *Server) handleActionInvoke(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.ActionEnvelope
	if err := decode(raw, &p); err != nil {
		return nil, invalid("action.invoke: " + err.Error())
	}
	if p.PluginID == "" || p.Action == "" {
		return nil, invalid("action.invoke: 需要 pluginId 与 action")
	}
	input := json.RawMessage("{}")
	if len(p.Input) > 0 {
		enc, err := json.Marshal(p.Input)
		if err != nil {
			return nil, invalid("action.invoke: input 无法编码: " + err.Error())
		}
		input = enc
	}
	out, err := s.sess.CallAction(ctx, p.PluginID, p.Action, input, p.ThreadID)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return map[string]any{"ok": false, "error": de.Detail}, nil
		}
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	var result any
	if len(out) > 0 {
		if err := json.Unmarshal(out, &result); err != nil {
			return map[string]any{"ok": false, "error": "动作结果不是合法 JSON: " + err.Error()}, nil
		}
	}
	return map[string]any{"ok": true, "result": result}, nil
}

// ---- skill（6.7：装配期只注入 name/description，正文按需披露）----

// skillSummaryJSON 把一条技能元数据编成 spec 的 SkillSummary（不含正文：渐进披露）。
func skillSummaryJSON(m domain.SkillMeta) map[string]any {
	out := map[string]any{
		"name":        m.Name,
		"description": m.Description,
		"origin":      m.Origin,
	}
	if m.PluginID != "" {
		out["pluginId"] = m.PluginID
	}
	return out
}

// handleSkillList 列出当前装配的技能（插件贡献 + 工作区，同名工作区优先）；
// conflicts 是同名冲突的技能名（装配期已记警告，这里再暴露给界面）。
func (s *Server) handleSkillList(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct{}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("skill.list: " + err.Error())
	}
	metas, conflicts := s.sess.SkillSummaries()
	skills := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		skills = append(skills, skillSummaryJSON(m))
	}
	out := map[string]any{"skills": skills}
	if len(conflicts) > 0 {
		out["conflicts"] = conflicts
	}
	return out, nil
}

// handleSkillRead 读取一个技能的 SKILL.md 正文（frontmatter 之后；模型工具
// read_skill 走同一实现）。截断到 16KB 并标 truncated。
func (s *Server) handleSkillRead(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("skill.read: " + err.Error())
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, invalid("skill.read: 需要非空的 name")
	}
	meta, content, truncated, ok := s.sess.ReadSkill(name)
	if !ok {
		return nil, invalid(fmt.Sprintf("skill.read: 没有名为 %q 的技能（skill.list 查看可用技能）", name))
	}
	out := map[string]any{
		"skill":     skillSummaryJSON(meta),
		"content":   content,
		"truncated": truncated,
	}
	return out, nil
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

// ---- change（M5：检查点账目与回滚，08 §8.2）----

// handleChangeList 实现 change.list：列检查点账目（线程可选、limit 1..200、
// 偏移游标）。真源是数据目录 changes/ 下的持久化 manifest，进程重启后仍在。
func (s *Server) handleChangeList(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Limit    int    `json:"limit"`
		Cursor   string `json:"cursor"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("change.list: " + err.Error())
	}
	if p.Limit < 0 || p.Limit > 200 {
		return nil, invalid("change.list: limit 必须在 0..200（0 取默认 50）")
	}
	page, err := s.sess.Changes(strings.TrimSpace(p.ThreadID), p.Limit, p.Cursor)
	if err != nil {
		return nil, invalid(err.Error())
	}
	return map[string]any{
		"changeSets": page.ChangeSets,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	}, nil
}

// handleChangeRevert 实现 change.revert：把检查点备份写回工作区，返回
// 恢复/删除/跳过/失效四份清单（不谎报成功）。审计由 Session.Revert 写
// storage.restore。
//
// 边界：回滚只恢复文件，不改会话历史，也不终止当前线程正在运行的轮次——
// runInfo 不被触碰，事件照常广播；回滚之后同一文件的再次写入会形成新的
// 检查点，可以再次回滚。
func (s *Server) handleChangeRevert(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID     string `json:"threadId"`
		Scope        string `json:"scope"`
		CheckpointID string `json:"checkpointId"`
		Path         string `json:"path"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("change.revert: " + err.Error())
	}
	if strings.TrimSpace(p.ThreadID) == "" {
		return nil, invalid("change.revert: 需要 threadId")
	}
	switch p.Scope {
	case "item", "file", "all":
	case "":
		return nil, invalid("change.revert: 需要 scope（item/file/all）")
	default:
		return nil, invalid("change.revert: scope 只能是 item/file/all")
	}
	if p.Scope == "item" && strings.TrimSpace(p.CheckpointID) == "" {
		return nil, invalid("change.revert: scope=item 需要 checkpointId")
	}
	if p.Scope == "file" && strings.TrimSpace(p.Path) == "" {
		return nil, invalid("change.revert: scope=file 需要 path")
	}
	res, err := s.sess.Revert(ctx, cli.RevertRequest{
		ThreadID: p.ThreadID, Scope: p.Scope, CheckpointID: p.CheckpointID, Path: p.Path,
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{
		"restored":    res.Restored,
		"deleted":     res.Deleted,
		"skipped":     res.Skipped,
		"invalidated": res.Invalidated,
	}, nil
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
		Contributions: map[string]any{"tools": sum.Tools, "hooks": sum.Hooks, "slots": sum.Slots, "skills": sum.Skills},
	}
}

// pluginViewJSON 把 cli.PluginView 编成 plugin.list 的行。
//
// pluginViewJSON 把 cli.PluginView 编成 plugin.list 的行。
// Source 已进协议（spec PluginSummary.source，M3 安装/发现）。
func pluginViewJSON(v cli.PluginView) proto.PluginSummary {
	return proto.PluginSummary{
		ID: v.ID, Name: v.Name, Version: v.Version, APIVersion: v.APIVersion,
		Trust: v.Trust, Enabled: v.Enabled, Status: v.Status,
		Fingerprint: v.Fingerprint, LastError: v.LastError, Source: v.Source,
		Contributions: map[string]any{"tools": v.Tools, "hooks": v.Hooks, "skills": v.Skills},
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
