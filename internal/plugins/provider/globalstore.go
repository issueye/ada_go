package provider

// GlobalStore 是 provider.Store 的通用原语版本（P1 global.config 之后）：
// 配置写入 $ADAGO_HOME/config/provider-config.json（显式 DataDir 时为其 config/），
// 同时保留一次性的 providers.json 读取迁移与双写兼容。
//
// 双写是迁移期的临时策略：provider 管理插件接管 CRUD 后，legacy 写入会删除；
// 阅读方（Registry/CLI）始终优先全局配置命名空间。

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/adag/ada-go/internal/globalconfig"
)

// GlobalStore 用 global.config 承载供应商配置，并兼容旧的 providers.json。
type GlobalStore struct {
	legacy *Store
	cfg    *globalconfig.Store
	mu     sync.Mutex
}

// NewGlobalStore 创建全局配置存储；legacyPath 用于迁移与双写。
func NewGlobalStore(legacyPath string) (*GlobalStore, error) {
	if legacyPath == "" {
		return nil, fmt.Errorf("provider: GlobalStore 需要 legacy providers.json 路径")
	}
	cfg, err := globalconfig.OpenAt(filepath.Join(filepath.Dir(legacyPath), "config"), "provider-config")
	if err != nil {
		return nil, err
	}
	return &GlobalStore{legacy: NewStore(legacyPath), cfg: cfg}, nil
}

// Path 返回旧的 providers.json 路径（诊断与迁移使用）。
func (s *GlobalStore) Path() string {
	if s == nil || s.legacy == nil {
		return ""
	}
	return s.legacy.Path()
}

// Load 优先读 global.config；缺失时回退 legacy 文件并迁移。
func (s *GlobalStore) Load() ([]Config, bool, error) {
	if s == nil || s.cfg == nil {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if raw, found, err := s.cfg.Get("providers"); err != nil {
		return nil, false, err
	} else if found {
		cfgs, ok, derr := decodeGlobalProviders(raw)
		return cfgs, ok, derr
	} else {
	}
	cfgs, exists, err := s.legacy.Load()
	if err != nil || !exists {
		return cfgs, exists, err
	}
	if err := s.saveLocked(cfgs); err != nil {
		return nil, false, err
	}
	return cfgs, true, nil
}

// Save 写 global.config。legacy 文件只在首次加载时读取迁移，不再双写。
func (s *GlobalStore) Save(configs []Config) error {
	if s == nil || s.cfg == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(configs)
}

func (s *GlobalStore) saveLocked(configs []Config) error {
	raw, err := json.Marshal(configs)
	if err != nil {
		return fmt.Errorf("provider: 配置无法序列化: %w", err)
	}
	if err := s.cfg.Set("providers", raw); err != nil {
		return fmt.Errorf("provider: 写入全局配置失败: %w", err)
	}
	return nil
}

func decodeGlobalProviders(raw json.RawMessage) ([]Config, bool, error) {
	var list []Config
	if err := json.Unmarshal(raw, &list); err == nil {
		for i := range list {
			list[i] = list[i].Clone()
		}
		return list, true, nil
	}
	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, false, fmt.Errorf("provider: 全局配置 providers 不是合法供应商数组: %w", err)
	}
	out := make([]Config, 0, len(data.Providers))
	for _, c := range data.Providers {
		out = append(out, c.Clone())
	}
	return out, true, nil
}
