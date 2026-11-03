// handlers_cron.go —— 定时任务的机器端点（cron 二进制 → 网关 → core）。
//
// 独立 cron 二进制（mcp_projects/cron）到期后把 agent 动作 POST 到这里
// （Bearer 令牌，走 /api/* 的机器客户端鉴权，Bearer 免 CSRF，见 security.go）。
// 网关负责工作区 Core 的生命周期：Acquire → thread.create（首跑建专用线程）
// → turn.start。任务线程复用由调用方持有：首跑返回 threadId，之后调用方带回，
// 本端点直接复用。
//
// 无人值守轮次的审批交互按 Core 现有交互超时（默认 300s）自动过期，
// 不在这里引入任何新审批语义（见 AGENTS.md 审批暂缓约定）。
package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/gateway/relay"
)

// cronFireTimeout 是 Acquire + thread.create + turn.start 的整体预算：
// 三步都是轻量 RPC，超预算直接放弃本次触发（cron 二进制会记为失败）。
const cronFireTimeout = 2 * time.Minute

// cronFireRequest 是 cron 二进制的触发请求体（与 mcp_projects/cron 的
// internal/runner fireRequest 同构，两侧注释互指）。
type cronFireRequest struct {
	TaskID         string `json:"taskId"`
	TaskName       string `json:"taskName"`
	Text           string `json:"text"`
	ThreadID       string `json:"threadId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// cronFireResponse 是触发结果（与 mcp_projects/cron 的 fireResponse 同构）。
type cronFireResponse struct {
	Started  bool   `json:"started"`
	ThreadID string `json:"threadId,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleCronFire 处理 POST /api/cron/fire。
func (s *Server) handleCronFire(w http.ResponseWriter, r *http.Request) {
	var req cronFireRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCronFire(w, http.StatusBadRequest, cronFireResponse{Error: "请求体不是合法 JSON"})
		return
	}
	if strings.TrimSpace(req.TaskID) == "" || strings.TrimSpace(req.Text) == "" {
		writeCronFire(w, http.StatusBadRequest, cronFireResponse{Error: "需要 taskId 与 text"})
		return
	}

	// AI 任务落在主工作区（列表第一个）；HTTP 动作不经过本端点。
	workspace := s.primaryWorkspace()
	if workspace == "" {
		writeCronFire(w, http.StatusServiceUnavailable, cronFireResponse{Error: "没有可用的工作区"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cronFireTimeout)
	defer cancel()
	client, release, err := s.acquireWorkspace(ctx, workspace)
	if err != nil {
		writeCronFire(w, http.StatusBadGateway, cronFireResponse{Error: "拉起 Core 失败: " + err.Error()})
		return
	}
	defer release()

	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		title := "定时任务: " + strings.TrimSpace(req.TaskName)
		if title == "定时任务: " {
			title = "定时任务"
		}
		raw, err := client.Call(ctx, "thread.create", map[string]any{"title": title})
		if err != nil {
			writeCronFire(w, http.StatusBadGateway, cronFireResponse{Error: "thread.create 失败: " + err.Error()})
			return
		}
		var created struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(raw, &created); err != nil || created.Thread.ID == "" {
			writeCronFire(w, http.StatusBadGateway, cronFireResponse{Error: "thread.create 返回值缺少 thread.id"})
			return
		}
		threadID = created.Thread.ID
	}

	turnParams := map[string]any{
		"threadId": threadID,
		"text":     req.Text,
	}
	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		turnParams["idempotencyKey"] = key
	}
	if _, err := client.Call(ctx, "turn.start", turnParams); err != nil {
		var rpcErr *relay.RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == 2001 { // domain.CodeThreadBusy
			// 线程忙：不算失败，cron 二进制记 busy 后下个周期照常。
			writeCronFire(w, http.StatusOK, cronFireResponse{Started: false, Reason: "thread_busy", ThreadID: threadID})
			return
		}
		writeCronFire(w, http.StatusBadGateway, cronFireResponse{Started: false, ThreadID: threadID, Error: "turn.start 失败: " + err.Error()})
		return
	}
	writeCronFire(w, http.StatusOK, cronFireResponse{Started: true, ThreadID: threadID})
}

// primaryWorkspace 返回默认工作区：列表第一个，空列表回退单工作区配置。
func (s *Server) primaryWorkspace() string {
	if list := s.workspaceList(); len(list) > 0 {
		return list[0]
	}
	return s.cfg.Workspace
}

// writeCronFire 输出 JSON 响应。
func writeCronFire(w http.ResponseWriter, status int, body cronFireResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
