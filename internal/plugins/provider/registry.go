// Package provider 把插件登记的 provider 桥接成基座唯一的 ports.ModelClient。
//
// 边界（03 §5.5）：
//   - 插件只知道 ProviderRequest → ctx.emit 的协议映射；
//   - 基座只认识归一化 Delta；HTTP/SSE、超时、取消与重试都在这一层或宿主出网桥；
//   - 第三个 provider 实现不需要改基座，只要按同一份 registerProvider 契约登记。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/ports"
)

// Registry 是 provider 目录，同时实现 host.ProviderRegistry 与 ports.ModelClient。
//
// 装配期先由宿主的贡献结算调用 RegisterProvider；插件实例加载完成后再 Bind。
// Stream 只在两者都就绪时建立，避免拿到半装配的目录。
type Registry struct {
	mu       sync.RWMutex
	byID     map[string]registration
	byPlugin map[string][]string
	bound    map[string]*host.Instance
	// BaseURL 是兼容端点（DeepSeek/Ollama/Moonshot 等）的覆盖；空时由插件用默认值。
	BaseURL string
	// HostContext 是给 provider 调用的宿主上下文（日志、密钥、HTTP 桥、设置）。
	HostContext host.Context
}

type registration struct {
	pluginID string
	def      host.ProviderDef
	trust    domain.Trust
}

var (
	_ host.ProviderRegistry = (*Registry)(nil)
	_ ports.ModelClient     = (*Registry)(nil)
)

// New 创建空目录。baseURL 可为空（使用插件默认端点）。
func New(baseURL string) *Registry {
	return &Registry{
		byID:     map[string]registration{},
		byPlugin: map[string][]string{},
		bound:    map[string]*host.Instance{},
		BaseURL:  baseURL,
	}
}

// AddProvider 实现 host.ProviderRegistry：只登记，不建立模型客户端。
func (r *Registry) AddProvider(reg host.ProviderRegistration) error {
	if reg.PluginID == "" {
		return errors.New("provider: 登记缺少 pluginId")
	}
	if reg.Def.ID == "" {
		return errors.New("provider: 登记缺少 provider id")
	}
	if reg.Def.Stream.Slot == 0 {
		return fmt.Errorf("provider: %s 没有 stream 句柄", reg.Def.ID)
	}
	if len(reg.Def.Models) == 0 {
		return fmt.Errorf("provider: %s 没有声明模型", reg.Def.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[reg.Def.ID]; exists {
		return fmt.Errorf("provider: provider id %q 已被登记", reg.Def.ID)
	}
	r.byID[reg.Def.ID] = registration{pluginID: reg.PluginID, def: reg.Def, trust: reg.Trust}
	r.byPlugin[reg.PluginID] = append(r.byPlugin[reg.PluginID], reg.Def.ID)
	return nil
}

// Bind 把已加载的宿主实例挂到它登记的 provider 上。
func (r *Registry) Bind(pluginID string, inst *host.Instance) error {
	if inst == nil {
		return fmt.Errorf("provider: 插件 %s 的实例为空", pluginID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bound[pluginID] = inst
	return nil
}

// Unbind 在插件卸载时摘掉实例。
func (r *Registry) Unbind(pluginID string) {
	r.mu.Lock()
	delete(r.bound, pluginID)
	r.mu.Unlock()
}

// Providers 返回已登记的 provider id（稳定排序）。
func (r *Registry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Models 返回某个 provider 的模型能力（诊断与 model.list 用）。
func (r *Registry) Models(providerID string) []host.ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byID[providerID]
	if !ok {
		return nil
	}
	return append([]host.ModelInfo(nil), reg.def.Models...)
}

// Stream 实现 ports.ModelClient：解析 provider → 启动插件流 → 等到首个增量或首错。
//
// 「等到首个增量」是重试语义的前提：只有建连阶段（首个增量之前）的错误才能被
// 基座包装层重试；一旦交付过内容，重试会造成重复文本（03 §5.5）。
func (r *Registry) Stream(ctx context.Context, req ports.Request) (ports.Stream, error) {
	reg, inst, model, err := r.resolve(req)
	if err != nil {
		return nil, err
	}
	if len(req.Tools) > 0 && !model.SupportsTools {
		return nil, domain.NewError(domain.CodeProviderError,
			fmt.Sprintf("模型 %s 不支持工具调用", model.ID))
	}

	payload, err := json.Marshal(providerRequest{
		Model:    req.Model.ModelID,
		System:   req.System,
		Messages: req.Messages,
		Tools:    req.Tools,
		Params:   req.Params,
		BaseURL:  r.BaseURL,
	})
	if err != nil {
		return nil, domain.WrapError(domain.NewError(domain.CodeProviderError, "provider 请求无法序列化"), err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	s := &providerStream{
		ctx: streamCtx, cancel: cancel,
		events: make(chan streamEvent, 128),
	}
	hc := inst.BaseContext()
	if hc.Workspace == "" {
		hc = r.HostContext
	}
	hc.PluginID = reg.pluginID
	hc.Signal = streamCtx
	hc.Now = time.Now().UTC()

	go func() {
		defer close(s.events)
		err := inst.CallProviderStream(streamCtx, reg.def.Stream, payload, hc, func(raw json.RawMessage) error {
			var d ports.Delta
			if err := json.Unmarshal(raw, &d); err != nil {
				return domain.WrapError(domain.NewError(domain.CodeProtocolError,
					"provider 发送的增量不是合法 Delta JSON"), err)
			}
			if err := validateDelta(d); err != nil {
				return err
			}
			select {
			case s.events <- streamEvent{delta: d}:
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
		if err != nil {
			s.events <- streamEvent{err: normalizeProviderErr(err)}
			return
		}
		s.events <- streamEvent{done: true}
	}()

	// 建连窗口：首个增量、首错或取消。
	select {
	case ev, ok := <-s.events:
		if !ok {
			cancel()
			return nil, domain.NewError(domain.CodeProviderError, "provider 流在建连阶段结束")
		}
		if ev.err != nil {
			cancel()
			return nil, ev.err
		}
		if ev.done {
			// 合法的空流交给引擎按「EOF 无 finish」判协议错误，不在桥里替它下结论。
			return s, nil
		}
		d := ev.delta
		s.pending = &d
		return s, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

func (r *Registry) resolve(req ports.Request) (registration, *host.Instance, host.ModelInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byID[req.Model.ProviderID]
	if !ok {
		// 允许产品配置直接写 provider 插件 id，而不是 provider def.id。
		if ids := r.byPlugin[req.Model.ProviderID]; len(ids) == 1 {
			reg, ok = r.byID[ids[0]]
		}
	}
	if !ok {
		return registration{}, nil, host.ModelInfo{}, domain.NewError(domain.CodeProviderError,
			"没有可用的 provider: "+req.Model.ProviderID)
	}
	inst := r.bound[reg.pluginID]
	if inst == nil {
		return registration{}, nil, host.ModelInfo{}, domain.NewError(domain.CodeCoreUnavailable,
			"provider 插件 "+reg.pluginID+" 未加载")
	}
	for _, m := range reg.def.Models {
		if m.ID == req.Model.ModelID {
			return reg, inst, m, nil
		}
	}
	return registration{}, nil, host.ModelInfo{}, domain.NewError(domain.CodeProviderError,
		fmt.Sprintf("provider %s 没有声明模型 %q", req.Model.ProviderID, req.Model.ModelID))
}

// providerRequest 是脚本侧的 ProviderRequest（03 §5.5）。
type providerRequest struct {
	Model    string             `json:"model"`
	System   string             `json:"system"`
	Messages []domain.Message   `json:"messages"`
	Tools    []ports.ToolSchema `json:"tools,omitempty"`
	Params   ports.Params       `json:"params,omitempty"`
	BaseURL  string             `json:"baseUrl,omitempty"`
}

type streamEvent struct {
	delta ports.Delta
	err   error
	done  bool
}

// providerStream 在插件流与引擎之间做增量转发。
type providerStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	events chan streamEvent
	// pending 是建连窗口里已经收到的首个增量。
	pending *ports.Delta
	closed  bool
	mu      sync.Mutex
}

func (s *providerStream) Recv() (ports.Delta, error) {
	s.mu.Lock()
	if s.pending != nil {
		d := *s.pending
		s.pending = nil
		s.mu.Unlock()
		return d, nil
	}
	if s.closed {
		s.mu.Unlock()
		return ports.Delta{}, io.EOF
	}
	s.mu.Unlock()
	select {
	case ev, ok := <-s.events:
		if !ok {
			return ports.Delta{}, io.EOF
		}
		if ev.err != nil {
			return ports.Delta{}, ev.err
		}
		if ev.done {
			return ports.Delta{}, io.EOF
		}
		return ev.delta, nil
	case <-s.ctx.Done():
		return ports.Delta{}, s.ctx.Err()
	}
}

func (s *providerStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	return nil
}

// validateDelta 钉住归一化增量的形状（03 §5.5）：未知 kind 是协议错误。
func validateDelta(d ports.Delta) error {
	switch d.Kind {
	case ports.DeltaTextStart, ports.DeltaTextDelta, ports.DeltaTextEnd,
		ports.DeltaThinkingStart, ports.DeltaThinkingDelta, ports.DeltaThinkingEnd,
		ports.DeltaToolCallStart, ports.DeltaToolCallDelta, ports.DeltaToolCallEnd,
		ports.DeltaUsage, ports.DeltaFinish:
		return nil
	default:
		return domain.NewError(domain.CodeProtocolError,
			fmt.Sprintf("provider 发送了未知增量类型 %q", d.Kind))
	}
}

// normalizeProviderErr 把宿主/HTTP 错误归一到业务错误，同时保留可重试信息。
func normalizeProviderErr(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		if de.Code == domain.CodeProviderError {
			return de
		}
		return de
	}
	var he *host.HTTPError
	if errors.As(err, &he) {
		return domain.WrapError(domain.NewError(domain.CodeProviderError, he.Error()), he)
	}
	return domain.WrapError(domain.NewError(domain.CodeProviderError, "provider 流失败: "+err.Error()), err)
}
