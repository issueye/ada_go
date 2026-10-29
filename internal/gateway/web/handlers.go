package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// pluginSummaryDTO 对应 Core plugin.list 的一行（只声明 SSR 用到的字段）。
type pluginSummaryDTO struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Version       string         `json:"version"`
	Trust         string         `json:"trust"`
	Source        string         `json:"source"`
	Enabled       bool           `json:"enabled"`
	Status        string         `json:"status"`
	LastError     string         `json:"lastError"`
	Contributions map[string]any `json:"contributions"`
}

type pluginListResult struct {
	Plugins []pluginSummaryDTO `json:"plugins"`
}

// ---- 对话页 ----------------------------------------------------------------

// handleChatHome 渲染对话页（无激活会话）：侧栏列会话，主区是新会话空态。
func (s *Server) handleChatHome(w http.ResponseWriter, r *http.Request) {
	threads, err := s.listThreads(r, 50)
	data := threadData{
		baseData: s.base(r, "chat", "新对话", threads, "", err == nil),
	}
	s.render(w, http.StatusOK, "chat", data)
}

// handleThread 渲染会话页：thread.get + thread.entries（最近 100 条）。
func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		s.renderError(w, r, http.StatusNotFound, "没有找到这个会话",
			"链接里缺少会话 id。请从会话列表重新打开一个会话。", "", "/", "返回会话列表")
		return
	}
	rawGet, err := s.call(r, "thread.get", map[string]any{"threadId": id, "itemLimit": 100})
	if err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	var get threadGetResult
	if err := json.Unmarshal(rawGet, &get); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "无法解析会话",
			"Core 返回了无法识别的会话数据。请重试；若持续出现，请检查 Core 日志。",
			err.Error(), threadPath(id), "重试")
		return
	}

	rawEntries, err := s.call(r, "thread.entries", map[string]any{"threadId": id, "limit": 100})
	if err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	var page entryPage
	if err := json.Unmarshal(rawEntries, &page); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "无法解析会话历史",
			"Core 返回了无法识别的历史数据。请重试；若出现持续错误，请检查 Core 日志。",
			err.Error(), threadPath(id), "重试")
		return
	}

	transcript := buildTranscript(page.Entries)
	if len(transcript) == 0 {
		transcript = transcriptFromItems(get.Items)
	}
	transcript = append(transcript, noticesFromItems(get.Items)...)
	run := buildRun(get.Run)
	threads, _ := s.listThreads(r, 50)
	title := strings.TrimSpace(get.Thread.Title)
	if title == "" {
		title = "新会话"
	}
	base := s.base(r, "chat", title, threads, id, true)
	base.TopbarActions = threadTopbarActions(id, run.Active)
	models, modelLabel, modelValue := s.modelOptions(r, id)
	data := threadData{
		baseData:          base,
		Thread:            buildThreadInfo(get.Thread),
		Run:               run,
		Transcript:        transcript,
		Interactions:      buildInteractions(get.Interactions, id, s.csrf(r)),
		TurnKey:           newIdempotencyKey(),
		Models:            models,
		CurrentModelLabel: modelLabel,
		CurrentModelValue: modelValue,
	}
	if run.Active && !jsClient(r) {
		// 无 JS 轮询：运行中每 2 秒刷新一次，完成即停止（7.8）。
		// 有 JS 的浏览器由 chat-live.js 吃 SSE 增量，绝不能再发 meta refresh。
		data.RefreshSeconds = 2
	}
	s.render(w, http.StatusOK, "chat", data)
}

// jsClient 判断这个浏览器是否已经在跑脚本（sidebar-boot.js 首屏种的 cookie）。
// 判断错了只会影响「用轮询还是用增量」，两边的兜底都在：cookie 缺失时退到轮询。
func jsClient(r *http.Request) bool {
	c, err := r.Cookie(JSClientCookieName)
	return err == nil && c.Value == "1"
}

// handleCreateThread 创建线程并 303 到会话页。
func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单，可能超过 1MB 上限。请返回后重试。",
			err.Error(), "/", "返回")
		return
	}
	title := strings.TrimSpace(r.PostFormValue("title"))
	raw, err := s.call(r, "thread.create", map[string]any{"title": title})
	if err != nil {
		s.renderCoreError(w, r, err, "/")
		return
	}
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Thread.ID == "" {
		s.renderError(w, r, http.StatusBadGateway, "创建会话失败",
			"Core 没有返回新会话 id。请返回后重试；若持续出现，请检查 Core 日志。",
			"thread.create 返回值缺少 thread.id", "/", "返回")
		return
	}
	redirect(w, threadPath(res.Thread.ID))
}

// ThreadIDFromRequest 从 query threadId、页面路径（/threads/{id}、/thread/{id}）
// 或 Referer 推断请求所属线程。插件 route 用它把 ctx.state / 交互类数据收敛到
// 当前会话；无法确定的请求返回空串（工作区级）。
func ThreadIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if v := strings.TrimSpace(r.URL.Query().Get("threadId")); v != "" {
		return sanitizeThreadID(v)
	}
	if id := threadIDFromPath(r.URL.Path); id != "" {
		return id
	}
	if ref := strings.TrimSpace(r.Header.Get("Referer")); ref != "" {
		if u, err := url.Parse(ref); err == nil {
			return threadIDFromPath(u.Path)
		}
	}
	return ""
}

func threadIDFromPath(p string) string {
	for _, prefix := range []string{"/threads/", "/thread/"} {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		seg, _, _ := strings.Cut(rest, "/")
		id, err := url.PathUnescape(seg)
		if err != nil {
			continue
		}
		if id = sanitizeThreadID(id); id != "" {
			return id
		}
	}
	return ""
}

func sanitizeThreadID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\`+"\x00") {
		return ""
	}
	return id
}

// handleExtAPI 是插件动态 HTTP API 的 Gateway 入口（route）。
// 请求经 Core RPC plugin.http 转发到插件路由；返回插件结果 JSON。
// 认证由外部安全中间件负责（cookie/Bearer）；写方法额外要求 CSRF（同一中间件）。
func (s *Server) handleExtAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIError(w, http.StatusNotFound, "缺少插件 id")
		return
	}
	if wantsEventStream(r) {
		s.handleExtAPIStream(w, r, id)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "只支持 GET / POST")
		return
	}
	apiPath := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	if len(apiPath) > 1 {
		apiPath = strings.TrimSuffix(apiPath, "/")
	}
	query := map[string]string{}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}
	var body any
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "请求体读取失败（上限 1MB）")
			return
		}
		if text := strings.TrimSpace(string(raw)); text != "" {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "请求体必须是合法 JSON")
				return
			}
		}
	}
	params := map[string]any{
		"pluginId": id,
		"method":   r.Method,
		"path":     apiPath,
		"query":    query,
	}
	// 线程上下文：显式 query 优先；否则从页面路径/Referer 推断。
	// 插件 route 的 ctx.state 按这个线程命名空间读写（见 host.StateOp.ThreadID）。
	if threadID := ThreadIDFromRequest(r); threadID != "" {
		params["threadId"] = threadID
	}
	if body != nil {
		params["body"] = body
	}
	raw, err := s.call(r, "plugin.http", params)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "插件接口暂时不可用："+publicReason(err))
		return
	}
	var res struct {
		Ok               bool            `json:"ok"`
		Result           json.RawMessage `json:"result"`
		Error            string          `json:"error"`
		Status           int             `json:"status"`
		NotFound         bool            `json:"notFound"`
		MethodNotAllowed bool            `json:"methodNotAllowed"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		writeAPIError(w, http.StatusBadGateway, "Core 返回了无法识别的接口结果")
		return
	}
	if !res.Ok {
		status := http.StatusBadGateway
		switch {
		case res.Status >= 400 && res.Status <= 599:
			status = res.Status
		case res.NotFound:
			status = http.StatusNotFound
		case res.MethodNotAllowed:
			status = http.StatusMethodNotAllowed
		}
		msg := strings.TrimSpace(res.Error)
		if msg == "" {
			msg = "插件接口调用失败"
		}
		writeAPIError(w, status, msg)
		return
	}
	if len(res.Result) == 0 {
		res.Result = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Result)
}

// writeAPIError 输出与 action.invoke 一致的 JSON 错误形状（前端 fetch 可统一解析）。
func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": message},
	})
}

// wantsEventStream 判断浏览器是否希望以 SSE 消费插件接口（EventSource 会带 Accept）。
func wantsEventStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// handleExtAPIStream 是 SSE 路由的 Gateway 端：
//
//	订阅 Core 事件 → prepare 登记 → 写 SSE 头 → start → 转发 evt.plugin.stream
//
// prepare/start 分开保证「插件开始推帧」发生在订阅与响应头都就绪之后。
func (s *Server) handleExtAPIStream(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "SSE 接口只支持 GET")
		return
	}
	apiPath := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	if len(apiPath) > 1 {
		apiPath = strings.TrimSuffix(apiPath, "/")
	}
	query := map[string]string{}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}

	core, release, err := s.acquire(r.Context(), r)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "Core 暂时不可用："+publicReason(err))
		return
	}
	defer release()

	events, unsubscribe := core.Subscribe(1024)
	defer unsubscribe()

	streamID := newPluginStreamID()
	prepare := map[string]any{
		"action": "prepare", "streamId": streamID, "pluginId": id,
		"method": http.MethodGet, "path": apiPath, "query": query,
	}
	if threadID := ThreadIDFromRequest(r); threadID != "" {
		prepare["threadId"] = threadID
	}
	raw, err := core.Call(r.Context(), "plugin.httpStream", prepare)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "插件接口暂时不可用："+publicReason(err))
		return
	}
	var prepared struct {
		Prepared bool   `json:"prepared"`
		Status   int    `json:"status"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &prepared); err != nil {
		writeAPIError(w, http.StatusBadGateway, "Core 返回了无法识别的流结果")
		return
	}
	if !prepared.Prepared {
		status := prepared.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		message := strings.TrimSpace(prepared.Error)
		if message == "" {
			message = "无法打开插件事件流"
		}
		writeAPIError(w, status, message)
		return
	}

	// 断开时通知 Core 取消插件处理器；即使已自然结束，cancel 也是幂等的。
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = core.Call(ctx, "plugin.httpStream", map[string]any{"action": "cancel", "streamId": streamID})
	}()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}

	startCtx, cancelStart := context.WithTimeout(r.Context(), 5*time.Second)
	_, err = core.Call(startCtx, "plugin.httpStream", map[string]any{"action": "start", "streamId": streamID})
	cancelStart()
	if err != nil {
		_ = writeSSE(w, "error", map[string]any{"error": map[string]any{"message": publicReason(err)}})
		_ = writeSSE(w, "done", map[string]any{})
		_ = rc.Flush()
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Method != "evt.plugin.stream" {
				continue
			}
			var frame struct {
				StreamID string          `json:"streamId"`
				Event    string          `json:"event"`
				Data     json.RawMessage `json:"data"`
				Done     bool            `json:"done"`
				Error    string          `json:"error"`
			}
			if json.Unmarshal(ev.Params, &frame) != nil || frame.StreamID != streamID {
				continue
			}
			if frame.Done {
				if frame.Error != "" {
					_ = writeSSE(w, "error", map[string]any{"error": map[string]any{"message": frame.Error}})
				}
				_ = writeSSE(w, "done", map[string]any{})
				_ = rc.Flush()
				return
			}
			data := frame.Data
			if len(data) == 0 {
				data = json.RawMessage("null")
			}
			if err := writeSSE(w, frame.Event, data); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// writeSSE 写一帧 EventSource 数据；data 按行拆成多个 data: 字段。
func writeSSE(w io.Writer, event string, data any) error {
	name := strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "", ":", "").Replace(event))
	if name == "" {
		name = "message"
	}
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("null")
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", name); err != nil {
		return err
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err = io.WriteString(w, "\n")
	return err
}

func newPluginStreamID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("ps_%d", time.Now().UnixNano())
	}
	return "ps_" + hex.EncodeToString(buf[:])
}

// handleTurn 提交用户消息，调用 turn.start，然后 303 回会话页。
func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单，可能超过 1MB 上限。请返回后重试。",
			err.Error(), threadPath(id), "返回")
		return
	}
	text := r.PostFormValue("text")
	if strings.TrimSpace(text) == "" {
		s.renderError(w, r, http.StatusBadRequest, "消息不能为空",
			"请输入消息正文后再发送。", "", threadPath(id), "返回会话")
		return
	}
	key := strings.TrimSpace(r.PostFormValue("idempotencyKey"))
	if key == "" {
		key = newIdempotencyKey()
	}
	params := map[string]any{
		"threadId":       id,
		"idempotencyKey": key,
		"text":           text,
	}
	if _, err := s.call(r, "turn.start", params); err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	redirect(w, threadPath(id))
}

// handleThreadDelete 删除会话（Core 软删 + 清理任务），成功后回到来源页。
// 删的是当前会话时回首页；next 只接受站内路径。
func (s *Server) handleThreadDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	next := strings.TrimSpace(r.PostFormValue("next"))
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		next = safeReturnPath(r)
	}
	if next == threadPath(id) || next == "/thread/"+id {
		next = "/"
	}
	if id == "" {
		redirect(w, flashRedirect(next, "error", "缺少会话 id"))
		return
	}
	if _, err := s.call(r, "thread.delete", map[string]any{"threadId": id}); err != nil {
		redirect(w, flashRedirect(next, "error", cleanRPCError(err)))
		return
	}
	redirect(w, flashRedirect(next, "notice", "会话已删除"))
}

// modelOptions 是输入框旁模型选择器的数据（model.list 投影）；失败时返回空列表，
// 不阻塞会话页渲染。
func (s *Server) modelOptions(r *http.Request, threadID string) ([]modelOption, string, string) {
	raw, err := s.call(r, "model.list", map[string]any{"threadId": threadID})
	if err != nil {
		return nil, "", ""
	}
	var res struct {
		Models []struct {
			Model struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
			} `json:"model"`
			Info struct {
				DisplayName string `json:"displayName"`
			} `json:"info"`
		} `json:"models"`
		Current struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		} `json:"current"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, "", ""
	}
	out := make([]modelOption, 0, len(res.Models))
	currentLabel := ""
	for _, m := range res.Models {
		if m.Model.ProviderID == "" || m.Model.ModelID == "" {
			continue
		}
		short := strings.TrimSpace(m.Info.DisplayName)
		if short == "" {
			short = m.Model.ModelID
		}
		label := short
		if short != m.Model.ModelID {
			label += " · " + m.Model.ModelID
		}
		current := res.Current.ProviderID == m.Model.ProviderID && res.Current.ModelID == m.Model.ModelID
		if current {
			currentLabel = short
		}
		out = append(out, modelOption{
			ProviderID: m.Model.ProviderID,
			ModelID:    m.Model.ModelID,
			Label:      label,
			ShortLabel: short,
			Current:    current,
		})
	}
	value := ""
	if res.Current.ProviderID != "" && res.Current.ModelID != "" {
		value = res.Current.ProviderID + "|" + res.Current.ModelID
	}
	if currentLabel == "" {
		currentLabel = "选择模型"
	}
	return out, currentLabel, value
}

// handleCancel 取消当前轮。
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.call(r, "turn.cancel", map[string]any{"threadId": id}); err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	redirect(w, threadPath(id))
}

// handleInteraction 提交交互响应。decision 直接映射到协议；value 按 kind 包装。
func (s *Server) handleInteraction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	requestID := r.PathValue("requestId")
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单，可能超过 1MB 上限。请返回后重试。",
			err.Error(), threadPath(id), "返回")
		return
	}
	decision := strings.TrimSpace(r.PostFormValue("decision"))
	switch decision {
	case "accept", "deny", "cancel":
	default:
		s.renderError(w, r, http.StatusBadRequest, "无效的交互决定",
			"只支持 accept、deny、cancel 三种决定。请返回后重新选择。",
			"decision="+decision, threadPath(id), "返回会话")
		return
	}
	params := map[string]any{
		"threadId":  id,
		"requestId": requestID,
		"decision":  decision,
	}
	if decision == "accept" {
		if strings.TrimSpace(r.PostFormValue("kind")) == "form" {
			// 分步表单：题目字段是 q0、q1…，按序拼成 answers 交给插件校验。
			params["value"] = map[string]any{"answers": formAnswers(r)}
		} else {
			value := strings.TrimSpace(r.PostFormValue("value"))
			if value != "" {
				params["value"] = interactionValue(r.PostFormValue("kind"), value)
			}
		}
	}
	if _, err := s.call(r, "interaction.respond", params); err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	redirect(w, threadPath(id))
}

// ---- 模型 ----------------------------------------------------------------

// handleModelSelect 切换当前模型（工作区级，或带 threadId 时线程级）。
// 供前端插件页面（供应商管理）的表单调用：303 回来源页。
func (s *Server) handleModelSelect(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单。", err.Error(), safeReturnPath(r), "返回")
		return
	}
	providerID := strings.TrimSpace(r.PostFormValue("providerId"))
	modelID := strings.TrimSpace(r.PostFormValue("modelId"))
	threadID := strings.TrimSpace(r.PostFormValue("threadId"))
	if providerID == "" || modelID == "" {
		s.renderError(w, r, http.StatusBadRequest, "缺少模型参数",
			"需要 providerId 与 modelId。", "", safeReturnPath(r), "返回")
		return
	}
	params := map[string]any{
		"model": map[string]any{"providerId": providerID, "modelId": modelID},
	}
	if threadID != "" {
		params["threadId"] = threadID
	}
	if _, err := s.call(r, "model.set", params); err != nil {
		s.renderCoreError(w, r, err, safeReturnPath(r))
		return
	}
	redirect(w, safeReturnPath(r))
}

// ---- 工作区 ----------------------------------------------------------------

// interactionValue 把表单里的单个字符串映射为协议要求的回答对象（spec InteractionResponse）：
// select → {"option": ...}；input → {"text": ...}；confirm → true。
func interactionValue(kind, value string) any {
	switch kind {
	case "select":
		return map[string]any{"option": value}
	case "confirm":
		return true
	default:
		return map[string]any{"text": value}
	}
}

// formAnswers 按 q0、q1… 的顺序取出分步表单的全部答案；遇到断号就停
// （题目编号由服务端按序渲染，不会跳号）。
func formAnswers(r *http.Request) []string {
	form := r.PostForm
	out := make([]string, 0, len(form))
	for i := 0; ; i++ {
		values, ok := form["q"+strconv.Itoa(i)]
		if !ok || len(values) == 0 {
			return out
		}
		out = append(out, strings.TrimSpace(values[0]))
	}
}

// handleWorkspaceGet 让 GET 回到工作区页（a_da 的 /workspaces）。
func (s *Server) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	redirect(w, "/workspaces")
}

// handleWorkspaceSelect 记住选择：只接受配置里的工作区（path 或 index）。
func (s *Server) handleWorkspaceSelect(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单，可能超过 1MB 上限。请返回后重试。",
			err.Error(), "/workspaces", "返回")
		return
	}
	index := -1
	rawPath := strings.TrimSpace(r.PostFormValue("path"))
	rawWorkspace := strings.TrimSpace(r.PostFormValue("workspace"))
	if rawPath == "" {
		rawPath = rawWorkspace
	}
	if rawPath != "" {
		index = workspaceIndex(s.cfg.Workspaces, rawPath)
		if index < 0 {
			// 允许相对/绝对路径差异：按规范化路径再匹配一次。
			index = workspaceIndexFolded(s.cfg.Workspaces, rawPath)
		}
		if index < 0 {
			s.renderError(w, r, http.StatusBadRequest, "这个目录不是工作区",
				"Gateway 的工作区在启动时固定。请从“磁盘与常用位置”里选择已配置的工作区。",
				"path="+rawPath, "/workspaces", "返回")
			return
		}
	} else {
		raw := strings.TrimSpace(r.PostFormValue("index"))
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed >= len(s.cfg.Workspaces) {
			s.renderError(w, r, http.StatusBadRequest, "没有这个工作区",
				"请求的工作区不在 Gateway 配置中。请返回后从列表中选择。",
				"index="+raw, "/workspaces", "返回")
			return
		}
		index = parsed
	}
	http.SetCookie(w, &http.Cookie{
		Name:     WorkspaceCookieName,
		Value:    strconv.Itoa(index),
		Path:     "/",
		MaxAge:   WorkspaceCookieMaxAge,
		SameSite: http.SameSiteLaxMode,
		HttpOnly: false,
		Secure:   s.cfg.CookieSecure,
	})
	redirect(w, "/")
}

// handleWorkspaces 渲染工作区页：磁盘/常用位置 + 最近工作区 + 目录浏览。
// 浏览范围限制在当前工作区子树内（不把网关所在机器的整个文件系统暴露给浏览器）。
func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	root, _ := s.resolveWorkspace(r)
	browse := strings.TrimSpace(r.URL.Query().Get("path"))
	if browse == "" || !withinRoot(root, browse) {
		browse = root
	}
	threads, _ := s.listThreads(r, 200)
	rootKey := normalizePath(root)
	currentCount := 0
	if withinRoot(root, browse) {
		currentCount = len(threads)
	}

	roots := make([]workspaceRoot, 0, len(s.cfg.Workspaces))
	for _, ws := range s.cfg.Workspaces {
		name := filepath.Base(ws)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = ws
		}
		roots = append(roots, workspaceRoot{
			Label: name,
			Path:  ws,
			Href:  "/workspaces?path=" + urlQueryEscape(ws),
			Kind:  "project",
		})
	}
	recent := make([]workspaceRecent, 0, len(s.cfg.Workspaces))
	for _, ws := range s.cfg.Workspaces {
		name := filepath.Base(ws)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = ws
		}
		row := workspaceRecent{Path: ws, Name: name, Known: normalizePath(ws) == rootKey}
		if row.Known {
			row.Sessions = currentCount
		}
		recent = append(recent, row)
	}

	entries, truncated, omitted := listDirectories(browse, 200)
	parent := ""
	if normalizePath(browse) != rootKey {
		if up := filepath.Dir(browse); withinRoot(root, up) {
			parent = up
		}
	}

	data := workspacesData{
		baseData:   s.base(r, "workspaces", "工作区", threads, "", true),
		Roots:      roots,
		Recent:     recent,
		Current:    browse,
		Parent:     parent,
		Entries:    entries,
		Truncated:  truncated,
		Omitted:    omitted,
		BindThread: strings.TrimSpace(r.URL.Query().Get("bind")),
		Selectable: workspaceIndexFolded(s.cfg.Workspaces, browse) >= 0,
	}
	if parent != "" {
		data.ParentHref = "/workspaces?path=" + urlQueryEscape(parent)
	}
	s.render(w, http.StatusOK, "workspaces", data)
}

// handleWorkspaceMkdir 在工作区子树内新建目录（目录选择器的“新建文件夹”）。
func (s *Server) handleWorkspaceMkdir(w http.ResponseWriter, r *http.Request) {
	root, _ := s.resolveWorkspace(r)
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单。", err.Error(), "/workspaces", "返回")
		return
	}
	parent := strings.TrimSpace(r.PostFormValue("parent"))
	if parent == "" || !withinRoot(root, parent) {
		parent = root
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		s.renderError(w, r, http.StatusBadRequest, "目录名不合法",
			"目录名不能为空，也不能包含路径分隔符。", "name="+name, "/workspaces?path="+urlQueryEscape(parent), "返回")
		return
	}
	target := filepath.Join(parent, name)
	if !withinRoot(root, target) {
		s.renderError(w, r, http.StatusBadRequest, "目录越界",
			"新建目录必须位于当前工作区内。", "", "/workspaces", "返回")
		return
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "无法新建目录",
			"服务端创建目录失败。请检查权限后重试。", err.Error(),
			"/workspaces?path="+urlQueryEscape(parent), "返回")
		return
	}
	redirect(w, "/workspaces?path="+urlQueryEscape(parent))
}

// ---- 概览 ----------------------------------------------------------------

// handleOverview 渲染概览页：统计卡 + 最近会话 + 插件卡片。
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	threads, err := s.listThreads(r, 200)
	if err != nil {
		data := offlineData{
			baseData: s.base(r, "overview", "概览", nil, "", false),
			Reason:   publicReason(err),
		}
		s.render(w, http.StatusOK, "offline", data)
		return
	}
	s.render(w, http.StatusOK, "overview", s.overviewData(r, threads))
}

// handleOverviewPartial 是概览页的 htmx 片段（每 5 秒换新）。
func (s *Server) handleOverviewPartial(w http.ResponseWriter, r *http.Request) {
	threads, err := s.listThreads(r, 200)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`<div class="empty-state"><p class="text-sm font-medium text-ink">未连接</p><p class="mt-1 text-xs text-faint">` + esc(publicReason(err)) + `</p></div>`))
		return
	}
	s.render(w, http.StatusOK, "overview_partial", s.overviewData(r, threads))
}

func (s *Server) overviewData(r *http.Request, threads []threadSummaryDTO) overviewData {
	recent := make([]overviewThread, 0, 8)
	for i, t := range threads {
		if i >= 8 {
			break
		}
		recent = append(recent, overviewThread{
			ID:        t.ID,
			Title:     t.Title,
			ItemCount: 0,
		})
	}
	newest := int64(0)
	for _, t := range threads {
		if ms := parseTimeMS(t.UpdatedAt); ms > newest {
			newest = ms
		}
	}
	lastActivity := "—"
	if newest > 0 {
		lastActivity = relativeTime(newest)
	}
	pluginCount := len(s.collectPlugins(r))
	stats := []overviewStat{
		{Label: "会话", Value: strconv.Itoa(len(threads)), Hint: "本地会话总数", Tone: "brand"},
		{Label: "最近活动", Value: lastActivity, Hint: "最后一个会话的更新时间", Tone: "neutral"},
		{Label: "工作区", Value: strconv.Itoa(len(s.cfg.Workspaces)), Hint: "Gateway 配置的工作区", Tone: "neutral"},
		{Label: "插件", Value: strconv.Itoa(pluginCount), Hint: "前端与后端插件总数", Tone: "brand"},
	}
	return overviewData{
		baseData: s.base(r, "overview", "概览", threads, "", true),
		Stats:    stats,
		Recent:   recent,
	}
}

// ---- 插件 ----------------------------------------------------------------

// handleExtensions 渲染插件页：前端与后端插件合并成一份列表（列表里不分区）。
func (s *Server) handleExtensions(w http.ResponseWriter, r *http.Request) {
	threads, err := s.listThreads(r, 50)
	plugins := s.collectPlugins(r)
	data := extensionsData{
		baseData:    s.base(r, "extensions", "插件", threads, "", err == nil),
		Plugins:     plugins,
		PluginCount: len(plugins),
	}
	s.render(w, http.StatusOK, "extensions", data)
}

// handlePluginDetail 渲染单个插件的信息：前端/后端归属只在这里展示。
func (s *Server) handlePluginDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	for _, p := range s.collectPlugins(r) {
		if p.ID != id {
			continue
		}
		threads, err := s.listThreads(r, 50)
		s.render(w, http.StatusOK, "plugin_detail", pluginDetailData{
			baseData: s.base(r, "extensions", p.Name, threads, "", err == nil),
			Plugin:   p,
		})
		return
	}
	s.renderError(w, r, http.StatusNotFound, "没有这个插件",
		"插件 "+id+" 不存在或未装配。请从插件列表重新进入。", "", "/extensions", "返回插件列表")
}

// collectPlugins 合并前端插件（渲染桥）与后端插件（Core plugin.list）。
// 同一个 id 同时具备两段能力时合并成一条；前端/后端标记只在详情页展示。
func (s *Server) collectPlugins(r *http.Request) []pluginRow {
	byID := make(map[string]*pluginRow)
	order := make([]string, 0, 8)
	upsert := func(id string) *pluginRow {
		if p, ok := byID[id]; ok {
			return p
		}
		p := &pluginRow{ID: id, Enabled: true, Status: "ready"}
		byID[id] = p
		order = append(order, id)
		return p
	}
	if lister, ok := s.cfg.PluginUI.(PluginLister); ok {
		for _, p := range lister.List() {
			row := upsert(p.ID)
			row.Frontend = true
			if p.Name != "" {
				row.Name = p.Name
			}
			if p.Version != "" {
				row.Version = p.Version
			}
			if p.Description != "" {
				row.Description = p.Description
			}
			row.Slots = p.SlotCount
			row.Pages = p.PageCount
			row.Nav = p.NavCount
			row.FirstPage = p.FirstPage
			row.HasDashboard = p.HasDashboard
			if p.Status != "" {
				row.Status = p.Status
			}
		}
	}
	if raw, err := s.call(r, "plugin.list", map[string]any{}); err == nil {
		var res pluginListResult
		if json.Unmarshal(raw, &res) == nil {
			for _, p := range res.Plugins {
				row := upsert(p.ID)
				row.Frontend = row.Frontend || contributionBool(p.Contributions["frontend"])
				// 旧 Core 不返回段标记时按后端处理（历史上 plugin.list 只有后端行）。
				row.Backend = contributionBool(p.Contributions["backend"]) || !row.Frontend
				if row.Name == "" {
					row.Name = p.Name
				}
				if row.Version == "" {
					row.Version = p.Version
				}
				row.Source = pluginScope(p.Source, p.Trust)
				row.Status = p.Status
				row.Enabled = p.Enabled
				row.Error = p.LastError
				row.Tools = contributionCount(p.Contributions["tools"])
				row.Hooks = contributionCount(p.Contributions["hooks"])
				row.Skills = contributionCount(p.Contributions["skills"])
			}
		}
	}
	rows := make([]pluginRow, 0, len(order))
	for _, id := range order {
		rows = append(rows, *byID[id])
	}
	for i := range rows {
		rows[i].StatusLabel, rows[i].StatusTone = pluginStatusView(rows[i].Status, rows[i].Enabled)
	}
	sort.Slice(rows, func(i, j int) bool {
		ai, aj := rows[i].Name, rows[j].Name
		if ai == "" {
			ai = rows[i].ID
		}
		if aj == "" {
			aj = rows[j].ID
		}
		if ai != aj {
			return ai < aj
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// handleExtensionsReload 重载全部 agent 插件（前端插件在装配期固定，不在此处重载）。
func (s *Server) handleExtensionsReload(w http.ResponseWriter, r *http.Request) {
	path := safeReturnPath(r)
	if _, err := s.call(r, "plugin.reload", map[string]any{}); err != nil {
		redirect(w, flashRedirect(path, "error", cleanRPCError(err)))
		return
	}
	redirect(w, flashRedirect(path, "notice", "插件已重载"))
}

// handleAgentPluginsReload 与 handleExtensionsReload 同义（a_da 的两个按钮）。
func (s *Server) handleAgentPluginsReload(w http.ResponseWriter, r *http.Request) {
	s.handleExtensionsReload(w, r)
}

func (s *Server) handlePluginEnable(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, true)
}

func (s *Server) handlePluginDisable(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, false)
}

func (s *Server) setPluginEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	path := safeReturnPath(r)
	if id == "" {
		redirect(w, flashRedirect(path, "error", "缺少插件 id"))
		return
	}
	if _, err := s.call(r, "plugin.setEnabled", map[string]any{"pluginId": id, "enabled": enabled}); err != nil {
		// 业务拒绝（例如禁用唯一审批策略插件）回到列表用提示条说明，不渲染 502。
		redirect(w, flashRedirect(path, "error", cleanRPCError(err)))
		return
	}
	action := "已启用"
	if !enabled {
		action = "已停用"
	}
	redirect(w, flashRedirect(path, "notice", action+"插件 "+id))
}

// flashRedirect 给回跳地址加上提示参数。
func flashRedirect(path, kind, msg string) string {
	if path == "" {
		path = "/"
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + kind + "=" + url.QueryEscape(msg)
}

// cleanRPCError 去掉 relay 传输前缀，只留 Core 的人话错误。
func cleanRPCError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	msg = strings.TrimPrefix(msg, "relay: ")
	if i := strings.Index(msg, "RPC 错误 "); i >= 0 {
		rest := msg[i+len("RPC 错误 "):]
		if j := strings.Index(rest, ": "); j >= 0 {
			msg = rest[j+2:]
		}
	}
	return clip(msg, 600)
}

// ---- 通用 ----------------------------------------------------------------

// listThreads 读取会话列表（侧栏/概览共用）；错误交给调用方决定降级方式。
func (s *Server) listThreads(r *http.Request, limit int) ([]threadSummaryDTO, error) {
	raw, err := s.call(r, "thread.list", map[string]any{"limit": limit, "archived": false})
	if err != nil {
		return nil, err
	}
	var res threadListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Threads, nil
}

func pluginScope(source, trust string) string {
	if strings.TrimSpace(source) == "" {
		return "bundled"
	}
	return source
}

// threadTopbarActions 是会话页顶栏右侧：刷新 + 运行中时的停止。
func threadTopbarActions(threadID string, running bool) template.HTML {
	out := fmt.Sprintf(`<a class="ada-icon-btn" href="/threads/%s" title="刷新" aria-label="刷新">%s</a>`,
		esc(threadID), iconHTML("i-refresh", 14))
	if running {
		out += fmt.Sprintf(`<form class="ada-action-form" method="post" action="/threads/%s/abort" data-confirm="确定中止当前轮次吗？"><button class="ada-btn ada-btn--danger ada-btn--sm" type="submit">%s<span class="ada-btn__label">停止</span></button></form>`,
			esc(threadID), iconHTML("i-stop", 14))
	}
	return template.HTML(out)
}

func contributionCount(value any) int {
	switch v := value.(type) {
	case []any:
		return len(v)
	case []string:
		return len(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func contributionBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

// pluginStatusView 把 Core 的状态映射成列表/详情的中文标签与色调。
// unused = 随包但当前产品未声明，不是故障。
func pluginStatusView(status string, enabled bool) (label, tone string) {
	switch status {
	case "ready":
		return "启用", "success"
	case "disabled":
		return "停用", "neutral"
	case "unused":
		return "未启用", "neutral"
	case "failed":
		return "失败", "danger"
	case "degraded":
		return "降级", "warn"
	default:
		if enabled {
			return "启用", "success"
		}
		return "停用", "neutral"
	}
}

func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

func withinRoot(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, candAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func normalizePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

func workspaceIndexFolded(list []string, p string) int {
	target := normalizePath(p)
	for i, candidate := range list {
		if normalizePath(candidate) == target {
			return i
		}
	}
	return -1
}

// listDirectories 列出目录下的子目录（只列目录；最多 limit 项）。
func listDirectories(dir string, limit int) ([]workspaceEntry, bool, int) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, 0
	}
	dirs := make([]fs.DirEntry, 0, len(items))
	for _, item := range items {
		if item.IsDir() {
			dirs = append(dirs, item)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
	omitted := 0
	if len(dirs) > limit {
		omitted = len(dirs) - limit
		dirs = dirs[:limit]
	}
	entries := make([]workspaceEntry, 0, len(dirs))
	for _, item := range dirs {
		full := filepath.Join(dir, item.Name())
		entries = append(entries, workspaceEntry{
			Name: item.Name(),
			Path: full,
			Href: "/workspaces?path=" + urlQueryEscape(full),
		})
	}
	return entries, omitted > 0, omitted
}

// handleEvents 把 SSE 交给外部注入的 handler；未注入时 404。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Events == nil {
		http.NotFound(w, r)
		return
	}
	s.cfg.Events.ServeHTTP(w, r)
}

// handleStatic 提供内嵌的静态资源（a_da token/CSS/JS 副本与 app-local.css）。
// 只服务 embed FS 中真实存在的普通文件：目录、空路径与任何非规范路径
// （. / .. / 重复斜杠 / 反斜杠）一律 404，因此不存在目录穿越面。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name, ok := staticAssetName(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", staticContentType(name))
	// 静态资源带 query 版本号（?v=…），可以长缓存；HTML 页面仍是 no-store。
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// staticAssetName 把 /static/ 之后的路径规范化；只接受规范相对路径。
func staticAssetName(urlPath string) (string, bool) {
	name := strings.TrimPrefix(urlPath, staticPrefix)
	if name == "" || strings.ContainsAny(name, `\`) {
		return "", false
	}
	// 先按 path.Clean 归一：任何 . / .. / 重复或末尾斜杠都会让结果与原名不同。
	if strings.TrimPrefix(path.Clean("/"+name), "/") != name {
		return "", false
	}
	if !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

// staticContentType 按扩展名给出稳定 Content-Type，不依赖系统 mime.types，
// 保证 /static/app.js 始终是 application/javascript。
func staticContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// handleLoginPage 只说明认证由外部中间件负责，不做密码校验。
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	threads, _ := s.listThreads(r, 50)
	s.render(w, http.StatusOK, "login", loginData{baseData: s.base(r, "chat", "登录", threads, "", true)})
}

// handleLoginSubmit 只做 303；真正的认证在外部 Protect 里。
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	redirect(w, "/")
}

// handleHealth 返回最小健康检查。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// parseForm 统一限制请求体大小并解析表单。
func (s *Server) parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return err
	}
	return nil
}

// renderCoreError 把 Core 调用失败转成人话错误页：
// 找不到线程 → 404；其余（没启动、连接断开、业务错误）→ 502。
func (s *Server) renderCoreError(w http.ResponseWriter, r *http.Request, err error, retryPath string) {
	status := http.StatusBadGateway
	heading := "Core 暂时不可用"
	message := "无法从工作区 Core 取到数据。请确认 Core 正在运行，然后重试。"
	if isNotFound(err) {
		status = http.StatusNotFound
		heading = "没有找到这个会话"
		message = "会话可能已删除，或它属于另一个工作区。请从会话列表重新打开。"
	}
	s.renderError(w, r, status, heading, message, err.Error(), retryPath, "重试")
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, heading, message, detail, retryPath, retryLabel string) {
	if retryPath == "" {
		retryPath = "/"
	}
	if retryLabel == "" {
		retryLabel = "重试"
	}
	threads, _ := s.listThreads(r, 50)
	s.render(w, status, "error", errorData{
		baseData:   s.base(r, "chat", heading, threads, "", false),
		Status:     status,
		Heading:    heading,
		Message:    message,
		Detail:     detail,
		RetryPath:  retryPath,
		RetryLabel: retryLabel,
	})
}

// isNotFound 只做保守的文本识别：Core 的错误类型不在 contracts 里暴露。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"not found", "不存在", "no such", "no rows"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// publicReason 把内部错误收敛成可展示的一句话（不泄露路径与令牌）。
func publicReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
