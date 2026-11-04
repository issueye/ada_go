// handlers_cron.go —— 定时任务的机器端点（cron 二进制 → 网关 → core）。
//
// 独立 cron 二进制（mcp_projects/cron）到期后把 agent 动作 POST 到这里
// （Bearer 令牌，走 /api/* 的机器客户端鉴权，Bearer 免 CSRF，见 security.go）。
// 任务可指定目标工作区（workspace）；为空时落在默认工作区（列表第一个，即
// “综合工作区”），指定了但不在列表里则明确失败。网关负责工作区 Core 的生命周期：
// Acquire → thread.create（首跑建专用线程）→ turn.start。任务线程复用由调用方
// 持有：首跑返回 threadId，之后调用方带回；带回的线程已被用户删除时，本次触发
// 会重建线程并照常启动轮次（不丢这一轮），响应里的 recreated 标记供回写。
//
// 无人值守轮次的审批交互按 Core 现有交互超时（默认 300s）自动过期，
// 不在这里引入任何新审批语义（见 AGENTS.md 审批暂缓约定）。
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Workspace      string `json:"workspace,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// cronFireResponse 是触发结果（与 mcp_projects/cron 的 fireResponse 同构）。
type cronFireResponse struct {
	Started  bool   `json:"started"`
	ThreadID string `json:"threadId,omitempty"`
	// Recreated 表示带来的任务线程已不存在（用户删了会话），本次已重建线程
	// 并在新线程里启动了轮次；Runner 据此把新的 threadId 回写到任务。
	Recreated bool   `json:"recreated,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
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

	// 任务指定工作区；没指定时落在默认工作区（网关列表第一个，即“综合工作区”）。
	workspace, err := s.resolveCronWorkspace(req.Workspace)
	if err != nil {
		writeCronFire(w, http.StatusBadRequest, cronFireResponse{Error: err.Error()})
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
	recreated := false
	if threadID != "" {
		// 复用线程前先确认它还在：用户可能删掉了任务专用线程。删除是软删
		// （archived=1），所以既要处理后端直接报「线程不存在」，也要检查
		// thread.get 返回的 archived。命中删除时本次就重建并在新线程里启动
		// （不能跳过这一轮，否则「立即运行」会静默丢失一次）。其它错误按失败
		// 处理，避免把仍然存在的线程误判成删除。
		raw, err := client.Call(ctx, "thread.get", map[string]any{"threadId": threadID})
		if err != nil {
			var rpcErr *relay.RPCError
			if errors.As(err, &rpcErr) && rpcErr.Code == -32602 { // coreproto.ErrInvalidParams：线程不存在
				threadID = ""
				recreated = true
			} else {
				writeCronFire(w, http.StatusBadGateway, cronFireResponse{
					Started: false, ThreadID: threadID,
					Error: "校验任务线程失败: " + err.Error(),
				})
				return
			}
		} else {
			var got struct {
				Thread struct {
					Archived bool `json:"archived"`
				} `json:"thread"`
			}
			if err := json.Unmarshal(raw, &got); err == nil && got.Thread.Archived {
				threadID = ""
				recreated = true
			}
		}
	}
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
	writeCronFire(w, http.StatusOK, cronFireResponse{Started: true, ThreadID: threadID, Recreated: recreated})
}

// resolveCronWorkspace 决定定时任务在哪个工作区执行：空表示默认工作区
// （列表第一个，即“综合工作区”）；显式指定时必须在网关的工作区列表里，
// 否则明确失败——不能悄悄换工作区执行，否则用户看到的是另一个项目的会话。
func (s *Server) resolveCronWorkspace(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		workspace := s.primaryWorkspace()
		if workspace == "" {
			return "", errors.New("没有可用的工作区")
		}
		return workspace, nil
	}
	list := s.workspaceList()
	if index := workspaceIndexFolded(list, requested); index >= 0 {
		return list[index], nil
	}
	// 单工作区兼容：列表为空时只有 cfg.Workspace。
	if cfgWorkspace := strings.TrimSpace(s.cfg.Workspace); cfgWorkspace != "" &&
		samePath(normalizePath(cfgWorkspace), normalizePath(requested)) {
		return cfgWorkspace, nil
	}
	return "", fmt.Errorf("工作区 %q 不在网关的工作区列表中", requested)
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
