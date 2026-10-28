// Package coreproto 是 Core 侧的 JSON-RPC 2.0 服务端、事件发射与状态投影（设计 5.1）。
//
// M0 阶段本包交付两件可门禁的东西：
//   - dispatch 表：34 个方法的注册集合必须与 spec/proto.json 完全一致（多一个、少一个都失败）；
//   - 事件发射的形状校验：只有协议声明的 12 个 evt.* 名字可以发出去。
//
// 真正的会话读写、投影缓存与策略编排属于 M1/M2；这里的处理函数一律返回失败关闭的
// notImplemented，绝不假装成功。
package coreproto

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/diag"
	"github.com/adag/ada-go/internal/proto"
)

// JSON-RPC 传输层保留码（8.5：业务错误用 4 位应用码，标准码只用于传输层）。
const (
	ErrParse          = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

// Handler 处理一个 RPC 调用。返回值由调用方编码为 JSON。
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server 持有一张方法表。它不持有工作区状态：状态属于某个 Core 实例（设计 3.3「无隐藏全局态」）。
type Server struct {
	handlers map[string]Handler
	// product 用于握手摘要；instanceID 形如 product::hash（设计 4.2）。
	product    string
	instanceID string
	pid        int
}

// NewServer 创建带默认（桩）dispatch 表的服务端。
func NewServer(product, instanceID string, pid int) *Server {
	s := &Server{
		handlers:   make(map[string]Handler, len(proto.Methods)),
		product:    product,
		instanceID: instanceID,
		pid:        pid,
	}
	for name, h := range stubHandlers() {
		s.handlers[name] = h
	}
	return s
}

// Register 替换某个方法的实现。只允许替换协议里已存在的方法：
// 插件不能新增 Core RPC 方法（设计 1.5.2「零协议扩展」）。
func (s *Server) Register(name string, h Handler) error {
	if _, ok := proto.LookupMethod(name); !ok {
		return fmt.Errorf("coreproto: %q 不在 spec/proto.json 里，拒绝注册", name)
	}
	if h == nil {
		return fmt.Errorf("coreproto: 方法 %q 的 handler 为 nil（INV-2 不允许 nil 端口/nil 处理）", name)
	}
	s.handlers[name] = h
	return nil
}

// Registered 返回已注册方法名（升序）。
func (s *Server) Registered() []string {
	out := make([]string, 0, len(s.handlers))
	for name := range s.handlers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Coverage 报告 dispatch 覆盖情况：missing 是协议声明但没实现的，extra 是实现里多出来的。
func (s *Server) Coverage() (missing, extra []string) {
	declared := map[string]bool{}
	for _, name := range proto.AllMethodNames() {
		declared[name] = true
		if _, ok := s.handlers[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range s.handlers {
		if !declared[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// CheckCoverage 是 adag verify dispatch 的判据：覆盖率必须 100%，且没有额外方法。
func (s *Server) CheckCoverage() error {
	missing, extra := s.Coverage()
	if len(missing) > 0 {
		return fmt.Errorf("coreproto: dispatch 缺少 %d 个协议方法：%s", len(missing), strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("coreproto: dispatch 注册了 %d 个协议外方法：%s", len(extra), strings.Join(extra, ", "))
	}
	return nil
}

// Handle 执行一次请求派发。它不 panic：未知方法与内部错误都转成错误响应。
func (s *Server) Handle(ctx context.Context, req proto.Request) proto.Response {
	resp := proto.Response{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" {
		resp.Error = &proto.RPCError{Code: ErrInvalidRequest, Message: "jsonrpc 字段必须是 \"2.0\""}
		return resp
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		if _, declared := proto.LookupMethod(req.Method); !declared {
			resp.Error = &proto.RPCError{Code: ErrMethodNotFound, Message: "未知方法 " + req.Method}
			return resp
		}
		resp.Error = notImplementedError(req.Method)
		return resp
	}
	result, err := h(ctx, req.Params)
	if err != nil {
		resp.Error = asRPCError(err)
		return resp
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		resp.Error = &proto.RPCError{Code: ErrInternal, Message: "结果编码失败: " + err.Error()}
		return resp
	}
	resp.Result = encoded
	return resp
}

// CheckHello 校验握手：协议主版本不一致必须拒绝，而不是尽力兼容（8.2、design 15.1「协议与实现漂移」）。
func CheckHello(res proto.HelloResult) error {
	if res.ProtoVersion != proto.ProtoVersion {
		return fmt.Errorf("coreproto: 协议版本不兼容：Core=%d，Gateway=%d", res.ProtoVersion, proto.ProtoVersion)
	}
	if strings.TrimSpace(res.ProductID) == "" {
		return fmt.Errorf("coreproto: 握手缺少 productId")
	}
	if res.PID <= 0 {
		return fmt.Errorf("coreproto: 握手缺少合法 pid（父进程心跳与孤儿防护依赖它，4.2）")
	}
	if strings.TrimSpace(res.Workspace.WorkspaceKey) == "" {
		return fmt.Errorf("coreproto: 握手缺少 workspaceKey（路径键归一化后的真实落点，4.2）")
	}
	return nil
}

// Hello 返回桩握手结果：产品与工作区摘要真实，能力计数按空基座（M1 起由 coreapp 填充）。
func (s *Server) Hello(gatewayProtoVersion int) (*proto.HelloResult, error) {
	if gatewayProtoVersion != proto.ProtoVersion {
		return nil, fmt.Errorf("coreproto: Gateway 协议版本 %d 与本 Core %d 不一致", gatewayProtoVersion, proto.ProtoVersion)
	}
	return &proto.HelloResult{
		ProtoVersion: proto.ProtoVersion,
		APIVersion:   proto.APIVersion,
		ProductID:    s.product,
		PID:          s.pid,
		Capabilities: proto.ProductCapabilities{},
		Workspace:    proto.WorkspaceInfo{InstanceID: s.instanceID},
	}, nil
}

// NewEvent 构造一个事件通知；名字不在协议里就拒绝（插件不能扩展协议）。
func NewEvent(name string, params any) (*proto.Notification, error) {
	if _, ok := proto.LookupEvent(name); !ok {
		return nil, fmt.Errorf("coreproto: %q 不是 spec/proto.json 声明的事件", name)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("coreproto: 事件 %s 载荷编码失败: %w", name, err)
	}
	if len(encoded) > proto.MaxMessageBytes {
		return nil, fmt.Errorf("coreproto: 事件 %s 载荷 %d 字节，超过单条消息上限 %d（8.1）", name, len(encoded), proto.MaxMessageBytes)
	}
	return &proto.Notification{JSONRPC: "2.0", Method: name, Params: encoded}, nil
}

// stubHandlers 是 34 个方法的初始实现：一律失败关闭，返回明确的未落地信息。
func stubHandlers() map[string]Handler {
	names := proto.AllMethodNames()
	out := make(map[string]Handler, len(names))
	for _, name := range names {
		out[name] = notImplemented(name)
	}
	return out
}

func notImplemented(name string) Handler {
	return func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, notImplementedError(name)
	}
}

// notImplementedError 把「契约已冻结、实现未落地」表达成 core_unavailable：
// 客户端据此重连或重启 Core，而不是当成业务失败静默吞掉。
func notImplementedError(name string) *proto.RPCError {
	entry, ok := diag.For(proto.CodeCoreUnavailable)
	if !ok {
		return &proto.RPCError{Code: ErrInternal, Message: name + " 尚未实现"}
	}
	meta, _ := proto.LookupError(proto.CodeCoreUnavailable)
	return &proto.RPCError{
		Code:    proto.CodeCoreUnavailable,
		Message: name + " 属于后续里程碑，M0 只冻结契约",
		Data: &proto.ErrorData{
			Name:       meta.Name,
			Diagnostic: entry.Diag.String(),
			NextStep:   entry.NextStep,
			Retryable:  meta.Retryable,
		},
	}
}

func asRPCError(err error) *proto.RPCError {
	if re, ok := err.(*proto.RPCError); ok {
		return re
	}
	return &proto.RPCError{Code: ErrInternal, Message: err.Error()}
}
