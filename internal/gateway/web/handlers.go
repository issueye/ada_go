package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleHome 渲染根页面：新建会话 + 最近线程。
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.renderThreadList(w, r, 20, "", true)
}

// handleThreads 渲染线程列表（limit/cursor 分页）。
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 50)
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	s.renderThreadList(w, r, limit, cursor, false)
}

func (s *Server) renderThreadList(w http.ResponseWriter, r *http.Request, limit int, cursor string, home bool) {
	ctx := r.Context()
	params := map[string]any{"limit": limit, "archived": false}
	if cursor != "" {
		params["cursor"] = cursor
	}
	raw, err := s.call(ctx, "thread.list", params)
	if err != nil {
		s.renderCoreError(w, r, err, listRetryPath(home))
		return
	}
	var res threadListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "无法解析线程列表",
			"Core 返回了无法识别的数据。请稍后重试；若持续出现，请检查 Core 日志。",
			err.Error(), listRetryPath(home), "重试")
		return
	}
	data := threadsData{
		baseData:   s.base(r, "会话"),
		IsHome:     home,
		Threads:    buildThreadList(res.Threads),
		Limit:      limit,
		NextCursor: res.NextCursor,
		HasMore:    res.HasMore,
	}
	s.render(w, http.StatusOK, "threads", data)
}

func listRetryPath(home bool) string {
	if home {
		return "/"
	}
	return "/threads"
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
	raw, err := s.call(r.Context(), "thread.create", map[string]any{"title": title})
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

// handleThread 渲染会话页：thread.get + thread.entries（最近 100 条）。
func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		s.renderError(w, r, http.StatusNotFound, "没有找到这个会话",
			"链接里缺少会话 id。请从会话列表重新打开一个会话。", "", "/", "返回会话列表")
		return
	}
	ctx := r.Context()
	rawGet, err := s.call(ctx, "thread.get", map[string]any{"threadId": id, "itemLimit": 100})
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

	rawEntries, err := s.call(ctx, "thread.entries", map[string]any{"threadId": id, "limit": 100})
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
	data := threadData{
		baseData:     s.base(r, "会话"),
		Thread:       buildThreadInfo(get.Thread),
		Run:          run,
		Transcript:   transcript,
		Interactions: buildInteractions(get.Interactions, id, s.csrf(r)),
		TurnKey:      newIdempotencyKey(),
	}
	if run.Active {
		// 无 JS 轮询：运行中每 2 秒刷新一次，完成即停止（7.8）。
		data.RefreshSeconds = 2
	}
	s.render(w, http.StatusOK, "thread", data)
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
	if _, err := s.call(r.Context(), "turn.start", params); err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	redirect(w, threadPath(id))
}

// handleCancel 取消当前轮。
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.call(r.Context(), "turn.cancel", map[string]any{"threadId": id}); err != nil {
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
		value := strings.TrimSpace(r.PostFormValue("value"))
		if value != "" {
			params["value"] = interactionValue(r.PostFormValue("kind"), value)
		}
	}
	if _, err := s.call(r.Context(), "interaction.respond", params); err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	redirect(w, threadPath(id))
}

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

// handleEvents 把 SSE 交给外部注入的 handler；未注入时 404。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Events == nil {
		http.NotFound(w, r)
		return
	}
	s.cfg.Events.ServeHTTP(w, r)
}

// handleCSS 提供内嵌的最小样式表。
func (s *Server) handleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(appCSS)
}

// handleLoginPage 只说明认证由外部中间件负责，不做密码校验。
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "login", loginData{baseData: s.base(r, "登录")})
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
	s.render(w, status, "error", errorData{
		baseData:   s.base(r, heading),
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
