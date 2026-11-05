package cli

// 模型选择的持久化：thread/workspace 覆盖只存在 Session 内存里时，Core 重启
// （桌面壳热更新、空闲回收、崩溃恢复）会把用户刚选的模型重置回默认值。
//
// 这里复用 state.db 的 plugin_state 表，用保留命名空间 core.model 存：
//   - workspace：工作区级当前模型；
//   - thread/<threadID>：线程级覆盖。
// 值很小（ModelRef JSON），不新增 schema，也不影响插件自己的 state 命名空间。

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/adag/ada-go/internal/ports"
)

const (
	modelStateNamespace = "core.model"
	workspaceModelKey   = "workspace"
	threadModelPrefix   = "thread/"
)

func threadModelKey(threadID string) string {
	return threadModelPrefix + threadID
}

func modelStateKey(key string) ports.StateKey {
	return ports.StateKey{PluginID: modelStateNamespace, Key: key}
}

// SaveWorkspaceModel 更新内存并持久化工作区级模型选择。
func (s *Session) SaveWorkspaceModel(ctx context.Context, ref ports.ModelRef) error {
	if err := s.writeModelState(ctx, workspaceModelKey, ref); err != nil {
		return err
	}
	s.SetWorkspaceModel(ref)
	return nil
}

// SaveThreadModel 更新内存并持久化线程级模型选择。
func (s *Session) SaveThreadModel(ctx context.Context, threadID string, ref ports.ModelRef) error {
	if strings.TrimSpace(threadID) == "" {
		return nil
	}
	if err := s.writeModelState(ctx, threadModelKey(threadID), ref); err != nil {
		return err
	}
	s.SetThreadModel(threadID, ref)
	return nil
}

func (s *Session) writeModelState(ctx context.Context, key string, ref ports.ModelRef) error {
	if s.store == nil {
		return nil
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	return s.store.Set(ctx, modelStateKey(key), raw)
}

// restoreModelSelections 在装配完成后恢复持久化的模型选择。无效的旧配置
// （供应商/模型已被删除）直接忽略，交给 ensureCurrentModel 的默认回退。
func (s *Session) restoreModelSelections(ctx context.Context) {
	if s.store == nil {
		return
	}
	if raw, ok, err := s.store.Get(ctx, modelStateKey(workspaceModelKey)); err == nil && ok {
		if ref, ok := s.decodeStoredModel(raw); ok {
			s.SetWorkspaceModel(ref)
		}
	}

	keys, err := s.store.ListKeys(ctx, modelStateNamespace, threadModelPrefix)
	if err != nil {
		return
	}
	for _, key := range keys {
		id := strings.TrimPrefix(key, threadModelPrefix)
		if strings.TrimSpace(id) == "" {
			continue
		}
		raw, ok, err := s.store.Get(ctx, modelStateKey(key))
		if err != nil || !ok {
			continue
		}
		if ref, ok := s.decodeStoredModel(raw); ok {
			s.SetThreadModel(id, ref)
		}
	}
}

func (s *Session) decodeStoredModel(raw json.RawMessage) (ports.ModelRef, bool) {
	var ref ports.ModelRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return ports.ModelRef{}, false
	}
	if strings.TrimSpace(ref.ProviderID) == "" || strings.TrimSpace(ref.ModelID) == "" {
		return ports.ModelRef{}, false
	}
	if resolved, ok := s.ResolveModel(ref.ProviderID, ref.ModelID); ok {
		if ref.ThinkingLevel != "" {
			resolved.ThinkingLevel = ref.ThinkingLevel
		}
		return resolved, true
	}
	// --fake / 注入 ModelClient 的测试与离线路径没有 provider 目录，
	// 但仍应尊重已经选择的 fake 模型。
	if s.cfg.Fake || s.cfg.ModelClient != nil {
		return ref, true
	}
	return ports.ModelRef{}, false
}

func (s *Session) forgetThreadModelState(threadID string) {
	if s.store == nil || strings.TrimSpace(threadID) == "" {
		return
	}
	_ = s.store.Delete(context.Background(), modelStateKey(threadModelKey(threadID)))
}
