package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"unicode/utf8"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/gateway/dispatch"
)

// imageParts 把表单里的图片（map 形态，直接透传给 turn.start）转成调度层的
// 强类型部件：转换只在这一处，避免同一份数据出现两种形状。
func imageParts(images []map[string]any) []domain.Part {
	if len(images) == 0 {
		return nil
	}
	out := make([]domain.Part, 0, len(images))
	for _, item := range images {
		p := domain.Part{Type: domain.PartImage}
		if v, ok := item["mime"].(string); ok {
			p.Mime = v
		}
		if v, ok := item["data"].(string); ok {
			p.Data = v
		}
		out = append(out, p)
	}
	return out
}

// pluginSummaryDTO 对应 Core plugin.list 的一行（只声明 SSR 用到的字段）。
type pluginSummaryDTO struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	APIVersion    int             `json:"apiVersion"`
	Trust         string          `json:"trust"`
	Source        string          `json:"source"`
	Path          string          `json:"path"`
	Enabled       bool            `json:"enabled"`
	Status        string          `json:"status"`
	LastError     string          `json:"lastError"`
	Readme        string          `json:"readme"`
	Files         []pluginFileDTO `json:"files"`
	Contributions map[string]any  `json:"contributions"`
}

type pluginListResult struct {
	Plugins []pluginSummaryDTO `json:"plugins"`
}

// ---- 对话流数据（SPA 取数面）------------------------------------------------

// handleShell 渲染 SPA 外壳：history fallback 的统一出口。所有页面路径
// （/、/threads/{id}、/overview、/workspaces、/extensions、/ext/{id}/…）都返回
// 同一份 shell，数据由客户端按路径取 JSON 端点（dev-plan R3）。
func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "shell", s.base(r, ""))
}

// handleFallback 是未匹配路径的兜底：GET 返回 shell（客户端路由自会给出
// 404 视图），其余方法按 404 渲染错误页。
func (s *Server) handleFallback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		s.handleShell(w, r)
		return
	}
	s.handleNotFound(w, r)
}

// handleThreadState 是会话页的遥测轮询接口：一次 thread.get，
// 返回运行状态与上下文占用两块只读数据，供插件岛在运行中反复取最新值。
//
// 这里刻意不知道任何插件：数据是基座的通用会话投影。token/成本等展示口径
// 由插件自己的后端 route 聚合（events.read），基座不在这里补 usage.get。
func (s *Server) handleThreadState(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusNotFound, "缺少会话 id")
		return
	}
	rawGet, err := s.call(r, "thread.get", map[string]any{"threadId": id, "itemLimit": 1})
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, publicReason(err))
		return
	}
	var get threadGetResult
	if err := json.Unmarshal(rawGet, &get); err != nil {
		writeJSONError(w, http.StatusBadGateway, "Core 返回了无法识别的会话状态")
		return
	}
	out := map[string]any{"run": get.Run}
	if get.Context != nil {
		out["context"] = get.Context
	}
	// 交互托盘是基座组件（docs/00 §1.5.7）：state 响应把 pending 交互一并投影，
	// SPA 会话页据此渲染托盘（HEAD 页面的 .Interactions 同源：thread.get）。
	out["interactions"] = interactionStates(get.Interactions)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(out)
}

// interactionStates 把 Core 的交互列表投影成托盘数据：只留 pending；
// form 交互的题目存在 body 里（interactions 表没有 payload 列），
// 解析失败就当空表单，让用户至少能取消。
func interactionStates(in []interactionDTO) []interactionViewState {
	out := make([]interactionViewState, 0, len(in))
	for _, x := range in {
		if x.Status != "" && x.Status != "pending" {
			continue
		}
		view := interactionViewState{
			ID:      x.ID,
			Kind:    x.Kind,
			Title:   x.Title,
			Body:    x.Body,
			Options: append([]string(nil), x.Options...),
		}
		if strings.EqualFold(x.Kind, "form") {
			view.Questions = parseInteractionQuestions(x.Body)
			view.Body = ""
		}
		out = append(out, view)
	}
	return out
}

// parseInteractionQuestions 解析 form 交互 body 里的题目数组。
func parseInteractionQuestions(raw string) []interactionQuestionState {
	var items []struct {
		Question string   `json:"question"`
		Kind     string   `json:"kind"`
		Options  []string `json:"options"`
	}
	if json.Unmarshal([]byte(raw), &items) != nil {
		return nil
	}
	out := make([]interactionQuestionState, 0, len(items))
	for i, it := range items {
		kind := strings.TrimSpace(it.Kind)
		if kind != "confirm" && kind != "select" && kind != "input" {
			kind = "input"
		}
		out = append(out, interactionQuestionState{
			Index: i, Question: it.Question, Kind: kind, Options: it.Options,
		})
	}
	return out
}

// handleTranscriptJSON 只返回对话流 JSON 数据行（keyed patch 用）。
// 行形状见 view.go 的 transcriptRowState 注释：助手行是 VNode 数据，
// 用户/工具/notice 行是纯数据字段，渲染全部在客户端组件里。
//
// 历史分页（8.2 游标）：?before=<nextCursor>&limit=N 返回该游标之前更早的
// 一窗，响应带 hasMore/nextCursor 供「加载更早消息」入口续翻；不带参数时
// 返回最近 100 条（与 HEAD 的 OlderHref 语义对齐，只是从链接变成 fetch）。
func (s *Server) handleTranscriptJSON(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusNotFound, "缺少会话 id")
		return
	}
	rawGet, err := s.call(r, "thread.get", map[string]any{"threadId": id, "itemLimit": 100})
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, publicReason(err))
		return
	}
	var get threadGetResult
	if err := json.Unmarshal(rawGet, &get); err != nil {
		writeJSONError(w, http.StatusBadGateway, "Core 返回了无法识别的会话数据")
		return
	}
	entriesParams := map[string]any{"threadId": id, "limit": 100}
	cursor := strings.TrimSpace(r.URL.Query().Get("before"))
	if cursor != "" {
		entriesParams["cursor"] = cursor
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			entriesParams["limit"] = n
		}
	}
	rawEntries, err := s.call(r, "thread.entries", entriesParams)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, publicReason(err))
		return
	}
	var page entryPage
	if err := json.Unmarshal(rawEntries, &page); err != nil {
		writeJSONError(w, http.StatusBadGateway, "Core 返回了无法识别的历史数据")
		return
	}
	transcript := buildTranscript(page.Entries)
	entriesUsable := len(transcript) > 0
	if len(transcript) == 0 {
		transcript = transcriptFromItems(get.Items)
	}
	if cursor == "" {
		// 只在最新一窗追加 notice（更早的历史窗口没有实时 notice 概念）。
		transcript = append(transcript, noticesFromItems(get.Items)...)
	}
	if entriesUsable {
		s.prepareTranscriptEdits(r, transcript, id, get.Run.Status)
	}
	payload := s.transcriptPayload(r, id, get.Run.Status, transcript, newBlockBudget())
	if entriesUsable {
		payload.HasMore = page.HasMore
		payload.NextCursor = page.NextCursor
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) transcriptPayload(r *http.Request, threadID, runStatus string, items []transcriptItem, budget *blockBudget) transcriptPayload {
	rows := make([]transcriptRowState, 0, len(items))
	for i, item := range items {
		key := item.Key
		if key == "" {
			key = fmt.Sprintf("row:%d", i)
		}
		switch item.Kind {
		case "assistant":
			parts := make([]transcriptPartState, 0, len(item.Parts))
			for _, part := range item.Parts {
				switch part.Type {
				case "text":
					parts = append(parts, transcriptPartState{
						Type:  "text",
						Nodes: s.markdownNodesJSONWithBlocks(r, part.Text, budget),
					})
				case "thinking":
					parts = append(parts, transcriptPartState{Type: "thinking", Text: part.Text})
				case "image":
					parts = append(parts, transcriptPartState{Type: "image"})
				}
			}
			if len(parts) == 0 {
				continue
			}
			rows = append(rows, transcriptRowState{
				Key: key, Kind: "assistant", At: item.At, Parts: parts,
			})
		case "user":
			// 纯数据行：旧 SSR 模板 row/parts_user 用到的字段等价输出。
			parts := make([]transcriptPartState, 0, len(item.Parts))
			for _, part := range item.Parts {
				switch part.Type {
				case "text":
					parts = append(parts, transcriptPartState{Type: "text", Text: part.Text})
				case "thinking":
					parts = append(parts, transcriptPartState{Type: "thinking", Text: part.Text})
				case "image":
					parts = append(parts, transcriptPartState{Type: "image", Mime: part.Mime, Data: part.Data})
				}
			}
			row := transcriptRowState{Key: key, Kind: "user", At: item.At, Parts: parts}
			if item.CanEdit {
				row.CanEdit = true
				row.EditKey = item.EditKey
				row.EditText = item.EditText
				row.ThreadID = item.ThreadID
				row.CSRF = item.CSRF
			}
			rows = append(rows, row)
		case "tool":
			if item.Tool == nil {
				continue
			}
			rows = append(rows, transcriptRowState{
				Key: key, Kind: "tool", At: item.At,
				Tool: &toolRowState{
					CallID:      item.Tool.CallID,
					Name:        item.Tool.Tool,
					Status:      item.Tool.Status,
					StatusLabel: item.Tool.StatusLabel,
					DurationMS:  item.Tool.DurationMS,
					Truncated:   item.Tool.Truncated,
					Args:        item.Tool.Args,
					ArgsHint:    item.Tool.ArgsHint,
					Output:      item.Tool.Output,
					Details:     item.Tool.Details,
					Reason:      item.Tool.Reason,
				},
			})
		case "notice":
			rows = append(rows, transcriptRowState{
				Key: key, Kind: "notice", At: item.At, Level: item.Level, Text: item.Text,
			})
		default:
			// buildTranscript 只产出上述四种 kind；未知 kind 按纯文本行兜底
			// （旧模板的 plain 行语义）。
			rows = append(rows, transcriptRowState{
				Key: key, Kind: "plain", At: item.At, Text: item.Text,
			})
		}
	}
	return transcriptPayload{ThreadID: threadID, RunStatus: runStatus, Rows: rows}
}

// prepareTranscriptEdits 给用户消息补上“编辑并重发”的内联表单上下文。
// 只有当前 head 路径上的 user 行（Key 是 entry id）才会出现表单；运行中禁用，
// 避免用户改写正在落库的历史。
func (s *Server) prepareTranscriptEdits(r *http.Request, items []transcriptItem, threadID, runStatus string) {
	editable := editableRunStatus(runStatus)
	token := s.csrf(r)
	for i := range items {
		if items[i].Kind != "user" || items[i].Key == "" {
			continue
		}
		var b strings.Builder
		for _, p := range items[i].Parts {
			if p.Type != "text" || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
		text := b.String()
		items[i].ThreadID = threadID
		items[i].CSRF = token
		items[i].EditText = text
		items[i].CanEdit = editable && strings.TrimSpace(text) != ""
		if items[i].CanEdit {
			items[i].EditKey = newIdempotencyKey()
		}
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// handleCreateThread 创建任务：可指定工作区，并可直接带上第一条消息。
//
// 新建任务页把「选工作区 + 输入任务 + 发送」合成一次表单提交：
// 先按所选工作区 thread.create，再在同一 Core 上 turn.start。两步都成功
// 才跳会话页；turn.start 失败时线程已经建好，回跳并显示错误提示。
func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	images, form, imgErr := s.parseTurnForm(w, r)
	if imgErr != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单或图片",
			"请求体无效：可能超过大小上限（整包 48MB，图片单张 8MB、最多 4 张）。请返回后重试。",
			imgErr.Error(), "/", "返回")
		return
	}

	workspace, index := s.resolveWorkspace(r)
	if raw := strings.TrimSpace(form.Get("workspace")); raw != "" {
		idx, ok := s.registerWorkspace(raw)
		if !ok {
			s.renderError(w, r, http.StatusBadRequest, "这个目录不能作为工作区",
				"请选择一个存在且可访问的目录。", "path="+raw, "/", "返回新建任务")
			return
		}
		list := s.workspaceList()
		if idx >= len(list) {
			s.renderError(w, r, http.StatusInternalServerError, "工作区登记失败",
				"服务端没有找到刚登记的工作区，请重试。", "", "/", "返回新建任务")
			return
		}
		workspace, index = list[idx], idx
	}

	// 无 JS 时模型选择器用原生 select 提交；有 JS 时选中的那一刻已通过
	// /model/select 切换，这里不会有值。模型是工作区级（还没有线程）。
	if rawModel := strings.TrimSpace(form.Get("model")); rawModel != "" {
		providerID, modelID, ok := strings.Cut(rawModel, "|")
		if ok && providerID != "" && modelID != "" {
			params := map[string]any{
				"model": map[string]any{"providerId": providerID, "modelId": modelID},
			}
			if _, err := s.callWorkspace(r, workspace, "model.set", params); err != nil {
				s.renderCoreError(w, r, err, "/")
				return
			}
		}
	}

	text := strings.TrimSpace(form.Get("text"))
	if text == "" && len(images) > 0 {
		text = "（图片任务）"
	}
	title := strings.TrimSpace(form.Get("title"))
	if title == "" {
		title = titleFromTask(text)
	}

	// 建线程与首轮投递都交给调度层：页面不再自己拼 thread.create + turn.start。
	receipt, err := s.dispatch.Send(r.Context(), dispatch.UserSender(), dispatch.SendRequest{
		Workspace:      workspace,
		Title:          title,
		Text:           text,
		Images:         imageParts(images),
		Mode:           dispatch.DeliveryAuto,
		IdempotencyKey: newIdempotencyKey(),
		// 新建会话页允许只开空白会话（不投递首轮消息）。
		AllowEmpty: true,
	})
	if receipt.ThreadID == "" {
		if err == nil {
			s.renderError(w, r, http.StatusBadGateway, "创建任务失败",
				"Core 没有返回新会话 id。请返回后重试；若持续出现，请检查 Core 日志。",
				"thread.create 返回值缺少 thread.id", "/", "返回")
			return
		}
		s.renderCoreError(w, r, err, "/")
		return
	}

	// 后续页面、SSE 与会话请求都要落到同一个工作区：先写 cookie 再跳转。
	s.setWorkspaceCookie(w, index)

	if err != nil {
		// 线程已经建好、消息没发出去：跳到会话页显示原因，而不是丢掉它。
		redirect(w, flashRedirect(threadPath(receipt.ThreadID), "error", cleanRPCError(err)))
		return
	}
	redirect(w, threadPath(receipt.ThreadID))
}

// titleFromTask 用任务首行做标题；空任务回落为“新任务”。
func titleFromTask(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	if text == "" {
		return "新任务"
	}
	const maxRunes = 40
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return text
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

// handleTurn 提交用户消息：空闲时 turn.start；运行中则排队为 steering 消息。
//
// 运行中仍允许输入是刻意的（用户能在工具执行时补充约束）。直接 turn.start
// 会撞上 thread_busy 并整页报错，所以这里先读 turn.get：
//   - running       → turn.steer，在当前工具批次后送达；
//   - waiting_input → 提示先回答待处理交互（同时只允许一个待答项）；
//   - finishing     → 提示轮次正在收尾。
func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 图片输入：multipart 表单（附件按钮/粘贴/拖拽），文本与图片一起提交。
	// 上限：单张 ≤8MB（解码后）、最多 4 张、整包 ≤40MB；文本路径仍走 1MB 表单上限。
	images, form, imgErr := s.parseTurnForm(w, r)
	if imgErr != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单或图片",
			"请求体无效：可能超过大小上限（文本 1MB，图片单张 8MB、最多 4 张）。请返回后重试。",
			imgErr.Error(), threadPath(id), "返回会话")
		return
	}
	text := strings.TrimSpace(form.Get("text"))
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		s.renderError(w, r, http.StatusBadRequest, "消息不能为空",
			"请输入消息正文或附加图片后再发送。", "", threadPath(id), "返回会话")
		return
	}
	_ = form

	// 无 JS 的浏览器用 <noscript> 里的原生 select 提交 model（provider|model）；
	// 有 JS 时模型在选中的那一刻已经通过 /model/select 切换，这里不会有值。
	if raw := strings.TrimSpace(r.PostFormValue("model")); raw != "" {
		providerID, modelID, ok := strings.Cut(raw, "|")
		if ok && providerID != "" && modelID != "" {
			params := map[string]any{
				"model":    map[string]any{"providerId": providerID, "modelId": modelID},
				"threadId": id,
			}
			if _, err := s.call(r, "model.set", params); err != nil {
				s.renderCoreError(w, r, err, threadPath(id))
				return
			}
		}
	}

	key := strings.TrimSpace(r.PostFormValue("idempotencyKey"))
	if key == "" {
		key = newIdempotencyKey()
	}

	// 投递只有一条路径（调度层）：空闲 start、运行中 steer、等待回答/收尾时
	// 给提示。页面不再自己读 turn.get 再分支，避免与插件投递各写一份。
	workspace, _ := s.resolveWorkspace(r)
	receipt, err := s.dispatch.Send(r.Context(), dispatch.UserSender(), dispatch.SendRequest{
		Workspace:      workspace,
		ThreadID:       id,
		Text:           text,
		Images:         imageParts(images),
		Mode:           dispatch.DeliveryAuto,
		IdempotencyKey: key,
	})
	if err != nil {
		switch dispatch.CodeOf(err) {
		case dispatch.CodeWaitingInput:
			redirect(w, flashRedirect(threadPath(id), "error", "请先回答待处理的问题，再发送新消息"))
		case dispatch.CodeFinishing:
			redirect(w, flashRedirect(threadPath(id), "error", "当前轮次正在收尾，请稍后再发送"))
		default:
			s.renderCoreError(w, r, err, threadPath(id))
		}
		return
	}
	if receipt.Mode == dispatch.DeliverySteer {
		redirect(w, flashRedirect(threadPath(id), "notice", "已加入队列，会在当前步骤后送达"))
		return
	}
	redirect(w, threadPath(id))
}

// handleTurnResend 编辑并重发一条用户消息：把 head 移到该消息的父节点（舍弃它
// 之后的整条分支），再用新文本开始一轮。entry 只读、不改写；旧分支保留在存储里
// 但不再出现在当前 head 路径上（04 §5.7「编辑并重发」）。
func (s *Server) handleTurnResend(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.renderError(w, r, http.StatusNotFound, "没有找到这个会话",
			"链接里缺少会话 id。请从会话列表重新打开一个会话。", "", "/", "返回会话列表")
		return
	}

	images, form, err := s.parseTurnForm(w, r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单或图片",
			"请求体无效：可能超过大小上限（文本 1MB，图片单张 8MB、最多 4 张）。请返回后重试。",
			err.Error(), threadPath(id), "返回会话")
		return
	}
	entryID := strings.TrimSpace(form.Get("entryId"))
	text := strings.TrimSpace(form.Get("text"))
	if entryID == "" {
		redirect(w, flashRedirect(threadPath(id), "error", "缺少要编辑的消息，请刷新页面后重试"))
		return
	}
	if text == "" && len(images) == 0 {
		redirect(w, flashRedirect(threadPath(id), "error", "消息不能为空"))
		return
	}

	// 运行中丢弃历史会和正在落库的增量打架：先中止，再编辑。
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
	if !editableRunStatus(get.Run.Status) {
		redirect(w, flashRedirect(threadPath(id), "error", "当前轮次尚未结束（运行中或等待答复），请先中止再编辑重发"))
		return
	}

	// 目标消息必须落在当前 head 路径上；thread.entries 已按路径过滤。
	rawEntries, err := s.call(r, "thread.entries", map[string]any{"threadId": id, "limit": 500})
	if err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	var page entryPage
	if err := json.Unmarshal(rawEntries, &page); err != nil {
		s.renderError(w, r, http.StatusBadGateway, "无法解析会话历史",
			"Core 返回了无法识别的历史数据。请重试；若持续出现，请检查 Core 日志。",
			err.Error(), threadPath(id), "重试")
		return
	}
	var target *entryDTO
	for i := range page.Entries {
		if page.Entries[i].ID == entryID {
			target = &page.Entries[i]
			break
		}
	}
	if target == nil {
		redirect(w, flashRedirect(threadPath(id), "error", "找不到这条消息；它可能已不在当前对话分支上"))
		return
	}
	var payload messagePayload
	_ = json.Unmarshal(target.Payload, &payload)
	role := target.Role
	if role == "" {
		role = payload.Role
	}
	if target.Kind != "message" || role != "user" {
		redirect(w, flashRedirect(threadPath(id), "error", "只能编辑并重发用户消息"))
		return
	}

	// 编辑框只改文字：没有重新附加图片时沿用原消息里的图片，避免编辑文字时丢附件。
	if len(images) == 0 {
		for _, p := range payload.Parts {
			if p.Type == "image" && p.Mime != "" && p.Data != "" {
				images = append(images, map[string]any{"type": "image", "mime": p.Mime, "data": p.Data})
			}
		}
	}

	oldHead := get.Thread.HeadEntryID
	// entryId 为空表示目标是首条消息，清空 head 后从根重新开始。
	if _, err := s.call(r, "thread.branch", map[string]any{
		"threadId": id, "entryId": target.ParentID,
	}); err != nil {
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}

	// 每个编辑表单带独立幂等键：双击提交时 turn.start 是幂等的，
	// 不会在分支后又起第二条轮次。
	key := strings.TrimSpace(form.Get("idempotencyKey"))
	if len(key) < 8 || len(key) > 64 {
		key = newIdempotencyKey()
	}
	// 分支是业务操作（上面已做完），最后一步投递仍走调度层。
	workspace, _ := s.resolveWorkspace(r)
	if _, err := s.dispatch.Send(r.Context(), dispatch.UserSender(), dispatch.SendRequest{
		Workspace:      workspace,
		ThreadID:       id,
		Text:           text,
		Images:         imageParts(images),
		Mode:           dispatch.DeliveryStart,
		IdempotencyKey: key,
	}); err != nil {
		// 起轮失败时把 head 拨回原分支，别把会话停在半截。
		_, _ = s.call(r, "thread.branch", map[string]any{"threadId": id, "entryId": oldHead})
		s.renderCoreError(w, r, err, threadPath(id))
		return
	}
	redirect(w, flashRedirect(threadPath(id), "notice", "已重新发送；这条消息之后的内容已舍弃"))
}

// editableRunStatus 报告当前轮次状态是否允许改写历史：只有空闲/已终态可以。
func editableRunStatus(status string) bool {
	switch status {
	case "", "terminal":
		return true
	default:
		return false
	}
}

// parseTurnForm 解析发送表单：multipart（带图片）与 urlencoded（纯文本）双形态。
// 返回文本字段与已校验的图片部件（base64 + mime，直接透传 turn.start/steer）。
func (s *Server) parseTurnForm(w http.ResponseWriter, r *http.Request) ([]map[string]any, url.Values, error) {
	form := url.Values{}
	images := []map[string]any{}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			return nil, nil, err
		}
		for key, vals := range r.PostForm {
			form[key] = vals
		}
		return images, form, nil
	}
	// multipart：整包上限由安全中间件包 48MB（gateway.MaxBodyBytes 同源）。
	// 安全中间件的 CSRF 检查会先 ParseMultipartForm 消费请求体，这里复用缓存。
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		if errors.Is(err, http.ErrNotMultipart) {
			return nil, nil, err
		}
		return nil, nil, err
	}
	allowedMime := map[string]bool{
		"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true,
	}
	for key, vals := range r.MultipartForm.Value {
		form[key] = append(form[key], vals...)
	}
	for _, fh := range r.MultipartForm.File["images"] {
		if len(images) >= 4 {
			return nil, nil, errors.New("图片最多 4 张")
		}
		file, err := fh.Open()
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		if len(data) == 0 {
			return nil, nil, errors.New("图片内容为空")
		}
		if len(data) > 8<<20 {
			return nil, nil, fmt.Errorf("图片 %s 超过 8MB 上限", fh.Filename)
		}
		mime := strings.ToLower(http.DetectContentType(data))
		mime = strings.Split(mime, ";")[0]
		if !allowedMime[mime] {
			return nil, nil, fmt.Errorf("图片 %s 的类型 %s 不支持（PNG/JPEG/WebP/GIF）", fh.Filename, mime)
		}
		images = append(images, map[string]any{
			"type": "image",
			"mime": mime,
			"data": base64.StdEncoding.EncodeToString(data),
		})
	}
	return images, form, nil
}

// turnStatus 读取线程当前轮次状态；读不到返回空串，让调用方按空闲处理
// （此时 turn.start 会给出真正的错误，而不是把 Core 故障伪装成排队）。
func (s *Server) turnStatus(r *http.Request, threadID string) string {
	raw, err := s.call(r, "turn.get", map[string]any{"threadId": threadID})
	if err != nil {
		return ""
	}
	var res struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return ""
	}
	return res.Run.Status
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
	// 删除前经 Core 校验运行态（turnStatus 读不到时按空闲处理，见其注释）：
	// 侧栏对运行中的会话已隐藏删除按钮，这里是纵深防御——拿到旧列表的页面
	// 仍然可能把删除请求打过来。
	if status := s.turnStatus(r, id); status == "running" {
		redirect(w, flashRedirect(next, "error", "会话正在运行，请先停止当前轮次再删除。"))
		return
	}
	if _, err := s.call(r, "thread.delete", map[string]any{"threadId": id}); err != nil {
		redirect(w, flashRedirect(next, "error", cleanRPCError(err)))
		return
	}
	redirect(w, flashRedirect(next, "notice", "会话已删除"))
}

// handleThreadArchive 归档会话：从侧栏隐藏但不软删，归档记录仍保留在 state.db。
//
// 与删除一样只接受站内 next，并拒绝正在运行的会话；归档当前会话时回首页，
// 避免刷新后落到一个已经不在侧栏里的页面。
func (s *Server) handleThreadArchive(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	next := strings.TrimSpace(r.PostFormValue("next"))
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		next = safeReturnPath(r)
	}
	if next == threadPath(id) || next == "/thread/"+id || strings.HasPrefix(next, threadPath(id)+"?") {
		next = "/"
	}
	if id == "" {
		redirect(w, flashRedirect(next, "error", "缺少会话 id"))
		return
	}
	switch s.turnStatus(r, id) {
	case "running", "waiting_input", "finishing":
		redirect(w, flashRedirect(next, "error", "会话正在运行，请先停止当前轮次再归档。"))
		return
	}
	if _, err := s.call(r, "thread.archive", map[string]any{"threadId": id, "archived": true}); err != nil {
		redirect(w, flashRedirect(next, "error", cleanRPCError(err)))
		return
	}
	redirect(w, flashRedirect(next, "notice", "会话已归档"))
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
	level := strings.ToLower(strings.TrimSpace(r.PostFormValue("thinkingLevel")))
	if providerID == "" || modelID == "" {
		s.renderError(w, r, http.StatusBadRequest, "缺少模型参数",
			"需要 providerId 与 modelId。", "", safeReturnPath(r), "返回")
		return
	}
	params := map[string]any{
		"model": map[string]any{"providerId": providerID, "modelId": modelID, "thinkingLevel": level},
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

// modelOptions 是输入框旁模型选择器的数据（model.list 投影）；失败时返回空
// 投影不阻塞页面：SPA 会话页只显示「选择模型」占位，不隐藏发送按钮。
// label 与 HEAD 模板一致：displayName 与 modelId 不同时拼成 "displayName · modelId"。
func (s *Server) modelOptions(r *http.Request, threadID string) modelPickerState {
	raw, err := s.call(r, "model.list", map[string]any{"threadId": threadID})
	if err != nil {
		return modelPickerState{Models: []modelPickerOption{}, CurrentLabel: "选择模型"}
	}
	var res struct {
		Models []struct {
			Model struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
			} `json:"model"`
			Info struct {
				DisplayName    string   `json:"displayName"`
				ThinkingLevels []string `json:"thinkingLevels"`
			} `json:"info"`
		} `json:"models"`
		Current struct {
			ProviderID    string `json:"providerId"`
			ModelID       string `json:"modelId"`
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"current"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return modelPickerState{Models: []modelPickerOption{}, CurrentLabel: "选择模型"}
	}
	out := make([]modelPickerOption, 0, len(res.Models))
	state := modelPickerState{Models: out, CurrentLabel: "选择模型"}
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
		opt := modelPickerOption{
			Value:          m.Model.ProviderID + "|" + m.Model.ModelID,
			ProviderID:     m.Model.ProviderID,
			ModelID:        m.Model.ModelID,
			Label:          label,
			ShortLabel:     short,
			Current:        current,
			ThinkingLevels: append([]string(nil), m.Info.ThinkingLevels...),
			ThinkingLevel:  res.Current.ThinkingLevel,
		}
		if current {
			state.CurrentLabel = short
			state.CurrentValue = opt.Value
			state.CurrentLevels = append([]string(nil), m.Info.ThinkingLevels...)
			state.CurrentLevel = res.Current.ThinkingLevel
		}
		state.Models = append(state.Models, opt)
	}
	return state
}

// handleModelJSON 是模型选择器的 JSON 端点（HEAD 模板由服务端注入模型选项，
// SPA 改由这里按线程取：?threadId= 有值时线程级，否则工作区级）。
func (s *Server) handleModelJSON(w http.ResponseWriter, r *http.Request) {
	threadID := sanitizeThreadID(r.URL.Query().Get("threadId"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	state := s.modelOptions(r, threadID)
	_ = json.NewEncoder(w).Encode(state)
}

// handleThreadsJSON 是侧栏/会话列表页的 JSON 端点：一次 thread.list 的展示
// 投影（HEAD 侧栏 renderThreadRow 的字段语义）。跨工作区查询用 ?path= 显式
// 指定（只接受网关已知的工作区，避免把任意目录拉起 Core）；缺省用当前请求
// 解析出的工作区。thread.list 的 Core 内存态投影自带 running 标记。
func (s *Server) handleThreadsJSON(w http.ResponseWriter, r *http.Request) {
	workspace, _ := s.resolveWorkspace(r)
	if requested := strings.TrimSpace(r.URL.Query().Get("path")); requested != "" {
		if index := workspaceIndexFolded(s.workspaceList(), requested); index >= 0 {
			workspace = s.workspaceList()[index]
		} else {
			writeJSONError(w, http.StatusBadRequest, "这个目录不是网关已知的工作区")
			return
		}
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	client, release, err := s.acquireWorkspace(r.Context(), workspace)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, publicReason(err))
		return
	}
	params := map[string]any{}
	if limit > 0 {
		params["limit"] = limit
	}
	raw, err := client.Call(r.Context(), "thread.list", params)
	release()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, publicReason(err))
		return
	}
	var list threadListResult
	if err := json.Unmarshal(raw, &list); err != nil {
		writeJSONError(w, http.StatusBadGateway, "Core 返回了无法识别的会话列表")
		return
	}
	items := make([]threadListItem, 0, len(list.Threads))
	for _, t := range list.Threads {
		items = append(items, threadListItem{
			ID:        t.ID,
			Title:     t.Title,
			UpdatedAt: t.UpdatedAt,
			Archived:  t.Archived,
			Running:   t.Running,
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(threadsPayload{
		Workspace:     workspace,
		WorkspaceName: workspaceName(workspace),
		Threads:       items,
		HasMore:       list.HasMore,
		NextCursor:    list.NextCursor,
	})
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
	list := s.workspaceList()
	rawPath := strings.TrimSpace(r.PostFormValue("path"))
	rawWorkspace := strings.TrimSpace(r.PostFormValue("workspace"))
	if rawPath == "" {
		rawPath = rawWorkspace
	}
	if rawPath != "" {
		index = workspaceIndex(list, rawPath)
		if index < 0 {
			// 允许相对/绝对路径差异：按规范化路径再匹配一次。
			index = workspaceIndexFolded(list, rawPath)
		}
		if index < 0 {
			s.renderError(w, r, http.StatusBadRequest, "这个目录不是当前工作区",
				"请从 FilePicker 或“磁盘与常用位置”里选择一个工作区。",
				"path="+rawPath, "/workspaces", "返回")
			return
		}
	} else {
		raw := strings.TrimSpace(r.PostFormValue("index"))
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed >= len(list) {
			s.renderError(w, r, http.StatusBadRequest, "没有这个工作区",
				"请求的工作区不在 Gateway 配置中。请返回后从列表中选择。",
				"index="+raw, "/workspaces", "返回")
			return
		}
		index = parsed
	}
	s.setWorkspaceCookie(w, index)
	redirect(w, "/")
}

// setWorkspaceCookie 记住当前工作区索引；页面、SSE 与 RPC 都靠它解析工作区。
func (s *Server) setWorkspaceCookie(w http.ResponseWriter, index int) {
	http.SetCookie(w, &http.Cookie{
		Name:     WorkspaceCookieName,
		Value:    strconv.Itoa(index),
		Path:     "/",
		MaxAge:   WorkspaceCookieMaxAge,
		SameSite: http.SameSiteLaxMode,
		HttpOnly: false,
		Secure:   s.cfg.CookieSecure,
	})
}

// handleWorkspaceOpen 是 FilePicker 的“选中即记住”：登记目录并写工作区
// cookie，不创建 Core、不跳页。这样后续选模型触发的整页 reload 不会把
// 未提交的工作区选择丢掉。
func (s *Server) handleWorkspaceOpen(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		writeJSONError(w, http.StatusBadRequest, "无法读取表单")
		return
	}
	path := strings.TrimSpace(r.PostFormValue("path"))
	if path == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少工作区路径")
		return
	}
	index, ok := s.registerWorkspace(path)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "这个目录不能作为工作区")
		return
	}
	list := s.workspaceList()
	if index >= len(list) {
		writeJSONError(w, http.StatusInternalServerError, "工作区登记失败")
		return
	}
	s.setWorkspaceCookie(w, index)
	writeJSON(w, http.StatusOK, map[string]any{"workspace": list[index], "index": index})
}

// ---- 工作区列表 API ---------------------------------------------------------

// workspaceAPIItem 是 /api/workspaces 里的一项。
type workspaceAPIItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// workspaceAPIResponse 供插件页读取工作区列表（定时任务选择目标工作区）。
// Default 是没选工作区时实际使用的工作区（列表第一个，即“综合工作区”）。
type workspaceAPIResponse struct {
	Default    string             `json:"default"`
	Workspaces []workspaceAPIItem `json:"workspaces"`
}

// handleWorkspacesAPI 返回当前网关的工作区列表（与页面上的工作区选择器同源，
// 包含 FilePicker 本次会话新登记的工作区）。数据面走调度层，插件侧看到的是同一份。
func (s *Server) handleWorkspacesAPI(w http.ResponseWriter, r *http.Request) {
	list := s.dispatch.ListWorkspaces(r.Context(), dispatch.UserSender())
	items := make([]workspaceAPIItem, 0, len(list))
	for _, ws := range list {
		items = append(items, workspaceAPIItem{Name: ws.Name, Path: ws.Path})
	}
	writeJSON(w, http.StatusOK, workspaceAPIResponse{Default: s.dispatch.DefaultWorkspace(), Workspaces: items})
}

// primaryWorkspace 是没显式选择工作区时实际使用的工作区：列表第一个。
func (s *Server) primaryWorkspace() string {
	options := s.workspaceOptions()
	if len(options) == 0 {
		return ""
	}
	return options[0].Path
}

// ---- 文件系统 API（FilePicker） -------------------------------------------

// fsListRoot 是一个可选的工作区根。
type fsListRoot struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// fsListEntry 是目录下的一项（只列目录）。
type fsListEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Selectable bool   `json:"selectable"`
}

// fsListResponse 是 FilePicker 的目录快照。
type fsListResponse struct {
	Path       string        `json:"path"`
	Parent     string        `json:"parent"`
	Roots      []fsListRoot  `json:"roots"`      // Windows 盘符 / Unix 根与主目录
	Workspaces []fsListRoot  `json:"workspaces"` // 已打开/已配置的工作区，快捷入口
	Entries    []fsListEntry `json:"entries"`
	Selectable bool          `json:"selectable"`
	Truncated  bool          `json:"truncated"`
	Omitted    int           `json:"omitted"`
}

// handleFSList 是 FilePicker 的只读目录 API。
//
// Windows 上不带 path 时返回盘符列表（A:–Z:），让用户先选盘；Unix 返回
// 根目录与主目录。带 path 时浏览该目录。只列目录，不列文件，也不返回
// 文件内容。路径统一 EvalSymlinks，页面拿到的是真实落点。
func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimSpace(r.URL.Query().Get("path"))
	current := ""
	entries := []fsListEntry{}
	truncated := false
	omitted := 0
	if requested != "" {
		real, err := realPath(requested)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "目录不存在或无法访问")
			return
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			writeJSONError(w, http.StatusNotFound, "这个路径不是目录")
			return
		}
		current = real
		var listed []workspaceEntry
		listed, truncated, omitted = listDirectories(current, 500)
		entries = make([]fsListEntry, 0, len(listed))
		for _, entry := range listed {
			child, err := realPath(entry.Path)
			if err != nil {
				continue
			}
			entries = append(entries, fsListEntry{Name: entry.Name, Path: child, Selectable: true})
		}
	}

	parent := ""
	if current != "" {
		if dir := filepath.Dir(current); !samePath(dir, current) {
			parent = dir
		}
	}
	writeJSON(w, http.StatusOK, fsListResponse{
		Path:       current,
		Parent:     parent,
		Roots:      fsSystemRoots(),
		Workspaces: fsWorkspaceRoots(s.workspaceList()),
		Entries:    entries,
		Selectable: current != "",
		Truncated:  truncated,
		Omitted:    omitted,
	})
}

// fsSystemRoots 返回文件系统入口：Windows 是盘符，其他平台是 / 与主目录。
func fsSystemRoots() []fsListRoot {
	if os.PathSeparator == '\\' {
		out := make([]fsListRoot, 0, 8)
		// 从 C: 开始：A:/B: 是软驱字母，Stat 可能触发系统插入磁盘提示。
		for c := 'C'; c <= 'Z'; c++ {
			root := string(c) + ":\\"
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				out = append(out, fsListRoot{Label: string(c) + ":", Path: root})
			}
		}
		return out
	}
	out := []fsListRoot{{Label: "/", Path: "/"}}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, fsListRoot{Label: "主目录", Path: home})
	}
	return out
}

// fsWorkspaceRoots 把工作区列表转成 FilePicker 的快捷入口。
func fsWorkspaceRoots(workspaces []string) []fsListRoot {
	out := make([]fsListRoot, 0, len(workspaces))
	for _, ws := range workspaces {
		real, err := realPath(ws)
		if err != nil {
			continue
		}
		name := filepath.Base(real)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = real
		}
		out = append(out, fsListRoot{Label: name, Path: real})
	}
	return out
}

// realPath 把路径规范成真实落点（EvalSymlinks）；不存在时返回错误。
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// samePath 比较两个已规范化的路径；Windows 下大小写不敏感。
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// writeJSON 输出 no-store 的 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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

func (s *Server) overviewDashboard(r *http.Request, threads []threadSummaryDTO) dashboardState {
	workspace, _ := s.resolveWorkspace(r)
	recent := make([]overviewThread, 0, 8)
	for i, t := range threads {
		if i >= 8 {
			break
		}
		recent = append(recent, overviewThread{
			ID:           t.ID,
			Title:        t.Title,
			Workspace:    workspace,
			ItemCount:    0,
			UpdatedLabel: relativeTime(parseTimeMS(t.UpdatedAt)),
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
	pluginCount := len(s.collectPlugins(r, ""))
	stats := []overviewStat{
		{Label: "会话", Value: strconv.Itoa(len(threads)), Hint: "本地会话总数", Tone: "brand"},
		{Label: "最近活动", Value: lastActivity, Hint: "最后一个会话的更新时间", Tone: "neutral"},
		{Label: "工作区", Value: strconv.Itoa(len(s.workspaceList())), Hint: "配置与本次打开的工作区", Tone: "neutral"},
		{Label: "插件", Value: strconv.Itoa(pluginCount), Hint: "前端与后端插件总数", Tone: "brand"},
	}
	// 纯数字才用等宽字体；「1 天前」这类相对时间改用界面字重，避免中英混排出现大空隙。
	for i := range stats {
		stats[i].Numeric = numericLabel(stats[i].Value)
	}
	return dashboardState{Stats: stats, Recent: recent}
}

// numericLabel 只允许数字与千分位逗号。
func numericLabel(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && r != ',' {
			return false
		}
	}
	return true
}

// handleOverviewJSON 是概览岛的轮询接口；数据投影与 SSR 时代同源。
func (s *Server) handleOverviewJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	threads, err := s.listThreads(r, 200)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": publicReason(err)})
		return
	}
	_ = json.NewEncoder(w).Encode(s.overviewDashboard(r, threads))
}

// ---- 插件 ----------------------------------------------------------------

// extensionsState 合并插件列表与 CSRF，供 /partials/extensions.json 输出。
func (s *Server) extensionsState(r *http.Request) extensionsState {
	plugins := s.collectPlugins(r, "")
	return extensionsState{
		Plugins: plugins,
		Count:   len(plugins),
		CSRF:    s.csrf(r),
	}
}

// handleExtensionsJSON 是插件列表岛的刷新接口（只读）。
// ?detail=<id> 时按详情口径取那一个插件（清单文件、README 等只有详情请求
// 才投影），供插件详情页复用同一端点。
func (s *Server) handleExtensionsJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	state := s.extensionsState(r)
	if detail := strings.TrimSpace(r.URL.Query().Get("detail")); detail != "" {
		if p, ok := s.pluginByID(r, detail); ok {
			for i := range state.Plugins {
				if state.Plugins[i].ID == detail {
					state.Plugins[i] = p
					break
				}
			}
		}
	}
	_ = json.NewEncoder(w).Encode(state)
}

// handlePluginFile 返回插件包内单个文件的文本预览。
// 只允许详情请求里出现过的文件（相对路径），防止把插件目录之外的任意文件读给浏览器。
func (s *Server) handlePluginFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := s.pluginByID(r, id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "没有这个插件")
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少文件路径")
		return
	}
	view, err := s.readPluginFile(p, rel)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// pluginByID 用 plugin.list 的详情请求定位单个插件。
func (s *Server) pluginByID(r *http.Request, id string) (pluginRow, bool) {
	for _, p := range s.collectPlugins(r, id) {
		if p.ID == id {
			return p, true
		}
	}
	return pluginRow{}, false
}

const maxPluginFilePreview = 512 << 10

func pluginFileMeta(p pluginRow, filePath string) pluginFileView {
	for _, f := range p.Files {
		if f.Path == filePath {
			return pluginFileView{Path: f.Path, Size: f.Size, SizeLabel: f.SizeLabel}
		}
	}
	return pluginFileView{Path: filePath}
}

// readPluginFile 读取插件包内文件：只接受清单里的相对路径，大小有上限，
// 二进制文件返回标记而不把字节塞进 JSON。
func (s *Server) readPluginFile(p pluginRow, rel string) (pluginFileView, error) {
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "./")
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "" || rel == "." || path.IsAbs(rel) || strings.HasPrefix(rel, "../") {
		return pluginFileView{}, fmt.Errorf("文件路径不合法")
	}
	meta := pluginFileMeta(p, rel)
	found := false
	for _, f := range p.Files {
		if f.Path == rel && !f.Dir {
			meta = pluginFileView{Path: f.Path, Size: f.Size, SizeLabel: f.SizeLabel}
			found = true
			break
		}
	}
	if !found {
		return pluginFileView{}, fmt.Errorf("文件不在插件目录中")
	}
	if strings.TrimSpace(p.Path) == "" {
		return meta, fmt.Errorf("插件文件未落盘，无法预览")
	}
	root, err := realPath(p.Path)
	if err != nil {
		return meta, fmt.Errorf("插件目录不可读")
	}
	target, err := realPath(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return meta, fmt.Errorf("文件不存在或不可读")
	}
	if !withinRoot(root, target) {
		return meta, fmt.Errorf("文件路径越界")
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return meta, fmt.Errorf("文件不存在或不是普通文件")
	}
	f, err := os.Open(target)
	if err != nil {
		return meta, fmt.Errorf("无法打开文件")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxPluginFilePreview+1))
	if err != nil {
		return meta, fmt.Errorf("读取文件失败")
	}
	truncated := len(data) > maxPluginFilePreview
	if truncated {
		data = data[:maxPluginFilePreview]
	}
	meta.Size = info.Size()
	meta.SizeLabel = humanBytes(info.Size())
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		meta.Binary = true
		return meta, nil
	}
	meta.Content = string(data)
	// Markdown 文件只标记能力：预览渲染由前端做（源码高亮同理，SSR 已移除）。
	if isMarkdownFile(meta.Path) {
		meta.Markdown = true
	}
	meta.Truncated = truncated
	return meta, nil
}

// isMarkdownFile 判断预览文件是否按 Markdown 渲染（只看扩展名，不看内容嗅探：
// 扩展名是插件作者的选择，嗅探会把 README 之外的 .txt 也画成富文本）。
func isMarkdownFile(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	default:
		return false
	}
}

// collectPlugins 合并前端插件（渲染桥）与后端插件（Core plugin.list）。
// 同一个 id 同时具备两段能力时合并成一条；前端/后端标记只在详情展示。
func (s *Server) collectPlugins(r *http.Request, detailID string) []pluginRow {
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
		for _, p := range lister.List(r) {
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
			if p.Status != "" {
				row.Status = p.Status
			}
		}
	}
	listParams := map[string]any{}
	if detailID != "" {
		listParams["detailId"] = detailID
	}
	if raw, err := s.call(r, "plugin.list", listParams); err == nil {
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
				row.Path = p.Path
				row.Trust = p.Trust
				row.APIVersion = p.APIVersion
				row.Status = p.Status
				row.Enabled = p.Enabled
				row.Error = p.LastError
				row.Readme = p.Readme
				row.Files = make([]pluginFileDTO, 0, len(p.Files))
				for _, f := range p.Files {
					row.Files = append(row.Files, pluginFileDTO{
						Path: f.Path, Size: f.Size, Dir: f.Dir, Depth: f.Depth,
						SizeLabel:  humanBytes(f.Size),
						DepthClass: pluginDepthClass(f.Depth),
					})
				}
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
	path := actionReturnPath(r)
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
	path := actionReturnPath(r)
	if id == "" {
		redirect(w, flashRedirect(path, "error", "缺少插件 id"))
		return
	}
	if _, err := s.call(r, "plugin.setEnabled", map[string]any{"pluginId": id, "enabled": enabled}); err != nil {
		// 业务拒绝（例如插件与管理操作冲突）回到列表用提示条说明，不渲染 502。
		redirect(w, flashRedirect(path, "error", cleanRPCError(err)))
		return
	}
	// 前端插件是否渲染由渲染桥按 plugin.list 实时判断；让它的短缓存立刻失效。
	if inval, ok := s.cfg.PluginUI.(interface{ InvalidateEnabled(string) }); ok {
		ws, _ := s.resolveWorkspace(r)
		inval.InvalidateEnabled(ws)
	}
	action := "已启用"
	if !enabled {
		action = "已停用"
	}
	redirect(w, flashRedirect(path, "notice", action+"插件 "+id))
}

// actionReturnPath 决定表单动作后的回跳地址。
//
// 页面统一发 Referrer-Policy: no-referrer，浏览器不会带 Referer，单靠
// safeReturnPath 会退回首页——插件启停因此会“跳回会话界面”。表单显式
// 带 next 时优先用它（只接受站内绝对路径）。
func actionReturnPath(r *http.Request) string {
	if next := strings.TrimSpace(r.PostFormValue("next")); next != "" {
		if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, `\`) {
			if u, err := url.Parse(next); err == nil && u.Host == "" {
				return next
			}
		}
	}
	return safeReturnPath(r)
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

// humanBytes 给文件清单显示一个紧凑大小（插件详情页）。
func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "—"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// pluginDepthClass 把目录深度映射到固定的缩进 class（CSP 不允许内联 style）。
func pluginDepthClass(depth int) string {
	if depth < 0 {
		depth = 0
	}
	if depth > 4 {
		depth = 4
	}
	return fmt.Sprintf("ada-plugin-file--d%d", depth)
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
	targetReal, _ := realPath(p)
	for i, candidate := range list {
		if normalizePath(candidate) == target {
			return i
		}
		// FilePicker 返回的是 EvalSymlinks 后的真实路径；配置里的工作区
		// 可能本身是 symlink，必须按真实落点再比一次。
		if targetReal != "" {
			if candidateReal, err := realPath(candidate); err == nil && samePath(candidateReal, targetReal) {
				return i
			}
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

// maxMarkdownBytes 是实时 Markdown 渲染请求的上限；助手单条消息远小于此。
const maxMarkdownBytes = 512 << 10

// handleMarkdownAST 是实时 Markdown 的结构化版本：返回 VNode 数据，
// 由浏览器 ui-markdown 组件渲染，不再把 HTML 字符串交给前端。
func (s *Server) handleMarkdownAST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMarkdownBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	if len(body) > maxMarkdownBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "正文超过上限")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"nodes": json.RawMessage(s.markdownNodesJSONWithBlocks(r, req.Text, newBlockBudget()))})
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
	// 带 ?v= 指纹的资源可以长缓存；例外是 SPA 运行时的内部 ESM 模块图：
	// import "./sfc.mjs" 这类相对导入不带 query，必须 no-cache，否则重建网关后
	// 浏览器继续执行旧模块（2026-11-11 SPA 重建期间实际踩到过）。
	if r.URL.Query().Get("v") != "" || !isSpaModuleAsset(name) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if isSpaModuleAsset(name) {
		// SPA 运行时模块必须每次协商校验（不管有没有 ?v）：模块图里的相对
		// import 不带版本号，而浏览器已缓存的 max-age=3600 响应在 TTL 内不会
		// 重新验证——重建网关后会继续执行旧模块（2026-11-11 走查踩到）。
		w.Header().Set("Cache-Control", "no-cache")
	}
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
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".vue":
		return "text/plain; charset=utf-8"
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

// isSpaModuleAsset 判断名字是否属于 SPA 运行时的内部 ESM 模块图
// （static/ada/**/*.js|*.mjs）：这些文件由模块依赖图互相 import，URL 不带 ?v。
func isSpaModuleAsset(name string) bool {
	if !strings.HasPrefix(name, "ada/") {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs", ".vue":
		// .vue 也是运行时装载的文本组件（sfc.mjs fetch 后编译），不带 ?v 时
		// 同样要协商校验，否则重建后浏览器继续用旧组件。
		return true
	default:
		return false
	}
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

// handleNotFound 渲染 404 错误页：非 GET 的未匹配路径走这里
// （GET 一律由 handleFallback 返回 shell，由客户端路由给出 404 视图）。
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusNotFound, "页面不存在",
		"路径 "+r.URL.Path+" 没有对应的页面。", "", "/", "返回首页")
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
