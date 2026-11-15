package corerpc

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/agent_core/coreproto"
	"github.com/adag/ada-go/internal/agent_core/plugins"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/diag"
	"github.com/adag/ada-go/internal/common/domain"
	pluginapi "github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/common/proto"
)

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
		ThinkingLevels: append([]string(nil), m.ThinkingLevels...),
	}
}

func pluginSummary(inst interface {
	ID() string
	Manifest() *pluginapi.Manifest
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

// pluginViewJSON 把 corecontracts.PluginView 编成 plugin.list 的行。
//
// Source 已进协议（spec PluginSummary.source，M3 安装/发现）。
func pluginViewJSON(v corecontracts.PluginView) proto.PluginSummary {
	files := make([]proto.PluginFile, 0, len(v.Files))
	for _, f := range v.Files {
		files = append(files, proto.PluginFile{Path: f.Path, Size: f.Size, Dir: f.Dir, Depth: f.Depth})
	}
	return proto.PluginSummary{
		ID: v.ID, Name: v.Name, Version: v.Version, APIVersion: v.APIVersion,
		Trust: v.Trust, Enabled: v.Enabled, Status: v.Status,
		Fingerprint: v.Fingerprint, LastError: v.LastError, Source: v.Source,
		Path: v.Path, Readme: v.Readme, Files: files,
		Contributions: map[string]any{
			"tools": v.Tools, "hooks": v.Hooks, "skills": v.Skills,
			"frontend": v.Frontend, "backend": v.Backend,
		},
	}
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
	// 与 sqlite 的 encodeSeqCursor 对齐：历史游标是 "seq:<n>"，
	// 同时容忍早期/手工输入的纯十进制。
	text := strings.TrimPrefix(string(raw), "seq:")
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("游标形状非法")
	}
	return n, nil
}
