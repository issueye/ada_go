package corerpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/common/ports"
	"github.com/adag/ada-go/internal/common/proto"
	"github.com/adag/ada-go/internal/common/version"
)

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
	plugins := s.sess.Plugins()
	pluginsOut := make([]proto.PluginSummary, 0, len(plugins))
	for _, inst := range plugins {
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
		"runtimesClosed": len(s.sess.Plugins()),
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
	var up corecontracts.PolicyUpdate
	if p.Policies != nil {
		up = corecontracts.PolicyUpdate{
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
