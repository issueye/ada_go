package provider

// 供应商配置的持久化（供应商管理页的后端数据面）。
//
// 与 provider 插件的关系：
//   - 插件仍然只负责「协议 → 归一化 Delta」；
//   - 这里保存的是用户配置的供应商实例（名称、端点、密钥、模型清单）；
//   - Registry 用 Protocol 找到对应适配器插件，把配置里的 BaseURL/APIKey/模型
//     注入一次流式请求。
//
// API Key 只落在本地数据目录（0600），不返回给浏览器/插件前端；宿主只给
// 宿主只给管理页后端返回 hasApiKey + 尾号提示。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/adag/ada-go/internal/plugins/host"
)

// Protocol 是适配器协议标识；与官方 provider 插件一一对应。
type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolAnthropic       Protocol = "anthropic"
)

// Valid 报告协议是否受支持。
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropic:
		return true
	default:
		return false
	}
}

// AdapterID 返回协议对应的 provider 插件登记的 provider def.id。
func (p Protocol) AdapterID() string {
	switch p {
	case ProtocolOpenAIResponses:
		return "openai-responses"
	case ProtocolAnthropic:
		return "anthropic"
	default:
		return "openai"
	}
}

// ProtocolForAdapter 是 AdapterID 的反向映射；未知适配器按 OpenAI Chat 处理。
func ProtocolForAdapter(adapterID string) Protocol {
	switch adapterID {
	case "openai-responses":
		return ProtocolOpenAIResponses
	case "anthropic":
		return ProtocolAnthropic
	default:
		return ProtocolOpenAIChat
	}
}

// Model 是配置里的一个模型；Disabled 为 true 时不参与 model.list / 模型选择。
type Model struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"displayName,omitempty"`
	ContextWindow    int      `json:"contextWindow,omitempty"`
	MaxOutput        int      `json:"maxOutput,omitempty"`
	Input            []string `json:"input,omitempty"`
	SupportsTools    bool     `json:"supportsTools"`
	SupportsThinking bool     `json:"supportsThinking,omitempty"`
	Disabled         bool     `json:"disabled,omitempty"`
}

// Info 转成宿主模型能力（不含 Disabled，调用方过滤）。
func (m Model) Info() host.ModelInfo {
	return host.ModelInfo{
		ID: m.ID, DisplayName: m.DisplayName,
		ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
		Input: m.Input, SupportsTools: m.SupportsTools,
		SupportsThinking: m.SupportsThinking,
	}
}

// Config 是一个供应商实例。
type Config struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol Protocol `json:"protocol"`
	BaseURL  string   `json:"baseUrl,omitempty"`
	APIKey   string   `json:"apiKey,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
	Models   []Model  `json:"models"`
}

// Clone 深拷贝配置（避免调用方修改 registry 内部状态）。
func (c Config) Clone() Config {
	out := c
	out.Models = append([]Model(nil), c.Models...)
	for i := range out.Models {
		out.Models[i].Input = append([]string(nil), c.Models[i].Input...)
	}
	return out
}

// Store 是 providers.json 的读写器。API 只在 Core 侧使用。
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore 创建存储；path 为空时禁用持久化（测试可只调 Registry）。
func NewStore(path string) *Store { return &Store{path: path} }

// Path 返回配置文件位置。
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

type fileData struct {
	Version   int      `json:"version"`
	Providers []Config `json:"providers"`
}

// Load 读取配置。exists=false 表示文件不存在（首次启动，可以播种默认供应商）。
func (s *Store) Load() ([]Config, bool, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("provider: 读取 %s 失败: %w", s.path, err)
	}
	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, true, fmt.Errorf("provider: 解析 %s 失败: %w", s.path, err)
	}
	out := make([]Config, 0, len(data.Providers))
	for _, c := range data.Providers {
		out = append(out, c.Clone())
	}
	return out, true, nil
}

// Save 原子写入配置（0600，同目录临时文件 + rename）。
func (s *Store) Save(configs []Config) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data := fileData{Version: 1, Providers: make([]Config, 0, len(configs))}
	for _, c := range configs {
		data.Providers = append(data.Providers, c.Clone())
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("provider: 配置无法序列化: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("provider: 创建配置目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".providers-*.json")
	if err != nil {
		return fmt.Errorf("provider: 创建临时配置失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("provider: 设置配置权限失败: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("provider: 写入配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("provider: 关闭配置失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("provider: 替换配置失败: %w", err)
	}
	return nil
}
