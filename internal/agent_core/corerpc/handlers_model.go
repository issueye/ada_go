package corerpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/common/proto"
)

// ---- model/provider/tool/plugin ----

func (s *Server) handleModelList(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = decode(raw, &p)
	s.sess.RefreshProviderConfigs()
	models := []map[string]any{}
	for _, pid := range s.sess.ProviderIDs() {
		for _, m := range s.sess.ProviderModels(pid) {
			models = append(models, map[string]any{
				"model": proto.ModelRef{ProviderID: pid, ModelID: m.ID},
				"info":  modelInfo(m),
			})
		}
	}
	cur := s.sess.ModelRef()
	if p.ThreadID != "" {
		cur = s.sess.ModelRefFor(p.ThreadID)
	}
	return map[string]any{
		"models":  models,
		"current": proto.ModelRef{ProviderID: cur.ProviderID, ModelID: cur.ModelID, ThinkingLevel: cur.ThinkingLevel},
	}, nil
}

func (s *Server) handleModelSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Model *struct {
			ProviderID    string `json:"providerId"`
			ModelID       string `json:"modelId"`
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"model"`
		ThreadID string `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("model.set: " + err.Error())
	}
	if p.Model == nil || p.Model.ProviderID == "" || p.Model.ModelID == "" {
		return nil, invalid("model.set: 需要 model.providerId 与 model.modelId")
	}
	s.sess.RefreshProviderConfigs()
	ref, ok := s.sess.ResolveModel(p.Model.ProviderID, p.Model.ModelID)
	if !ok {
		return nil, invalid(fmt.Sprintf(
			"model.set: provider %q/model %q 未在装配中声明", p.Model.ProviderID, p.Model.ModelID))
	}
	level := strings.ToLower(strings.TrimSpace(p.Model.ThinkingLevel))
	if level != "" {
		valid := false
		for _, m := range s.sess.ProviderModels(p.Model.ProviderID) {
			if m.ID != p.Model.ModelID {
				continue
			}
			for _, allowed := range m.ThinkingLevels {
				if strings.EqualFold(allowed, level) {
					valid = true
					break
				}
			}
		}
		if !valid {
			return nil, invalid(fmt.Sprintf("model.set: 模型 %q 不支持推理等级 %q", p.Model.ModelID, level))
		}
		ref.ThinkingLevel = level
	}
	scope := "workspace"
	if p.ThreadID != "" {
		if _, err := s.sess.GetThread(ctx, p.ThreadID); err != nil {
			return nil, rpcError(err)
		}
		if err := s.sess.SaveThreadModel(ctx, p.ThreadID, ref); err != nil {
			return nil, rpcError(err)
		}
		scope = "thread"
	} else {
		if err := s.sess.SaveWorkspaceModel(ctx, ref); err != nil {
			return nil, rpcError(err)
		}
	}
	return map[string]any{
		"model": proto.ModelRef{
			ProviderID: ref.ProviderID, ModelID: ref.ModelID, ThinkingLevel: ref.ThinkingLevel,
		},
		"scope": scope,
	}, nil
}

func (s *Server) handleProviderList(context.Context, json.RawMessage) (any, error) {
	cfgs := s.sess.ProviderConfigs()
	out := []map[string]any{}
	if len(cfgs) > 0 {
		cur := s.sess.ModelRef()
		for _, c := range cfgs {
			models := []proto.ModelInfo{}
			for _, m := range c.Models {
				if !m.Disabled {
					models = append(models, modelInfo(m.Info()))
				}
			}
			baseURL := c.BaseURL
			if baseURL == "" {
				baseURL = s.sess.BaseURL()
			}
			out = append(out, map[string]any{
				"id": c.ID, "displayName": c.Name, "baseUrl": baseURL,
				"active": cur.ProviderID == c.ID, "models": models,
			})
		}
	} else {
		for _, pid := range s.sess.ProviderIDs() {
			models := []proto.ModelInfo{}
			for _, m := range s.sess.ProviderModels(pid) {
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
	descs := s.sess.Tools(ctx)
	out := make([]proto.ToolDescriptor, 0, len(descs))
	for _, d := range descs {
		out = append(out, proto.ToolDescriptor{
			Name: d.Name, Description: d.Description, Parameters: d.Parameters,
			Access: string(d.Access), TimeoutMS: int(d.Timeout / time.Millisecond),
			ParallelSafe: d.ParallelSafe, Rollback: d.Rollback,
			TerminatesTurn: d.TerminatesTurn, Discovery: d.Discovery, PluginID: d.PluginID,
		})
	}
	return map[string]any{"tools": out}, nil
}

// handlePluginList 列出随包 + 第三方插件（M3）。
// 第三方行带 enabled/status/fingerprint/source(workspace|global)；includeDisabled
// 缺省 true（协议默认值），显式传 false 时跳过被用户禁用的插件。
func (s *Server) handlePluginList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		IncludeDisabled *bool  `json:"includeDisabled"`
		DetailID        string `json:"detailId"`
	}
	_ = decode(raw, &p)
	views := s.sess.PluginViews(ctx)
	if strings.TrimSpace(p.DetailID) != "" {
		views = s.sess.PluginViewsDetailed(ctx, strings.TrimSpace(p.DetailID))
	}
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
