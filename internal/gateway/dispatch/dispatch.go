// Package dispatch 是网关侧唯一的消息发送路径（设计 4）。
//
// 用户消息与（后续的）插件/定时任务消息都走这里的 Send：解析工作区 → 取 Core 客户端
// → 必要时建线程 → 按线程状态决定 turn.start 还是 turn.steer。调用方不再各自拼
// 「先 thread.create 再 turn.start」，否则「空闲/运行中/等待回答」这些分支会各写一份、
// 各自漂移。
//
// 边界：本包只做投递，不解释任务业务（cron 表达式、供应商、工具），也不做授权。
// sender 用于审计与幂等命名空间，不能当授权凭据；工具审批与能力位仍走原链路。
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/gateway/contracts"
)

// SenderKind 是发送者类别。
type SenderKind string

const (
	// SenderUser 是界面用户（当前是单用户的 web 会话）。
	SenderUser SenderKind = "user"
	// SenderPlugin 是插件（经宿主桥盖章，插件不能自填）。
	SenderPlugin SenderKind = "plugin"
	// SenderSystem 是网关内部发起（诊断、自检）。
	SenderSystem SenderKind = "system"
)

// Sender 是一次投递的身份。它只用于审计、限流、幂等命名空间与运行归属。
type Sender struct {
	Kind     SenderKind `json:"kind"`
	ID       string     `json:"id"`
	PluginID string     `json:"pluginId,omitempty"`
	TaskID   string     `json:"taskId,omitempty"`
}

// UserSender 是界面的标准身份。
func UserSender() Sender { return Sender{Kind: SenderUser, ID: "web"} }

// PluginSender 构造插件身份（宿主盖章处调用）。
func PluginSender(pluginID, taskID string) Sender {
	return Sender{Kind: SenderPlugin, ID: "plugin:" + pluginID, PluginID: pluginID, TaskID: taskID}
}

// key 是审计与日志里的稳定标识。
func (s Sender) key() string {
	if s.PluginID != "" && s.TaskID != "" {
		return s.PluginID + "/" + s.TaskID
	}
	if s.PluginID != "" {
		return s.PluginID
	}
	return string(s.Kind) + ":" + s.ID
}

// DeliveryMode 决定线程正在运行时的行为。
type DeliveryMode string

const (
	// DeliveryAuto 空闲时 start，运行中 steer（界面的默认行为）。
	DeliveryAuto DeliveryMode = "auto"
	// DeliveryStart 只接受 start：运行中返回 thread_busy（定时任务用）。
	DeliveryStart DeliveryMode = "start"
	// DeliverySteer 只接受 steer：空闲时返回 not_running。
	DeliverySteer DeliveryMode = "steer"
)

// Workspace 是调度层看到的工作区条目（名字只用于展示）。
type Workspace struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Workspaces 是工作区解析面：调度层不自己维护工作区列表。
//
// 生产实现是网关的 web 服务（列表会随 FilePicker 登记增长）；测试可以直接给静态表。
type Workspaces interface {
	// Default 返回没显式指定时使用的工作区。
	Default() string
	// List 返回可选工作区（顺序即界面顺序）。
	List() []Workspace
	// Resolve 把调用方给的工作区路径规范化；不可用返回 false。
	Resolve(path string) (string, bool)
}

// SendRequest 是一次投递。
type SendRequest struct {
	// Workspace 为空表示默认工作区。
	Workspace string
	// ThreadID 为空表示新建线程。
	ThreadID string
	// Title 只在新建线程时使用；空标题由 Core 决定。
	Title string
	Text  string
	// Images 是已解码的图片部件（与 turn.start 的 images 同形）。
	Images []domain.Part
	Mode   DeliveryMode
	// IdempotencyKey 为空时由调度层生成；同一 key 的重复投递由 Core 幂等处理。
	IdempotencyKey string
	// AllowEmpty 允许「只建线程、不投递」（新建会话页的空任务）。
	// 仅 ThreadID 为空时有意义：已有线程的空消息一律拒绝。
	AllowEmpty bool
}

// Receipt 是一次投递的回执。
type Receipt struct {
	Workspace string       `json:"workspace"`
	ThreadID  string       `json:"threadId"`
	RunID     string       `json:"runId,omitempty"`
	Mode      DeliveryMode `json:"mode"`
}

// 发送结果的具体原因：调用方据此决定提示文案，不解析错误字符串。
const (
	// CodeThreadBusy 线程已有运行中轮次，且调用方要求只 start。
	CodeThreadBusy = "thread_busy"
	// CodeWaitingInput 线程在等待用户回答交互。
	CodeWaitingInput = "waiting_input"
	// CodeFinishing 线程正在收尾。
	CodeFinishing = "finishing"
	// CodeNotRunning 调用方要求 steer，但线程没有运行中的轮次。
	CodeNotRunning = "not_running"
	// CodeWorkspaceMissing 工作区不存在或不可访问。
	CodeWorkspaceMissing = "workspace_missing"
	// CodeThreadMissing 指定了线程 id，但 Core 找不到它。
	CodeThreadMissing = "thread_not_found"
	// CodeEmptyMessage 既没有文本也没有图片。
	CodeEmptyMessage = "empty_message"
)

// SendError 是一次投递的业务失败（不是传输错误）。
type SendError struct {
	Code    string
	Message string
	Err     error
}

func (e *SendError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *SendError) Unwrap() error { return e.Err }

// CodeOf 取错误里的业务原因；非 SendError 返回空串。
func CodeOf(err error) string {
	var se *SendError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func sendErr(code, message string, err error) *SendError {
	return &SendError{Code: code, Message: message, Err: err}
}

// Config 装配 Dispatcher。
type Config struct {
	Supervisor contracts.Supervisor
	Workspaces Workspaces
	// Log 记录投递审计（sender / workspace / thread / 结果）；nil 时静默。
	Log func(sender Sender, req SendRequest, receipt Receipt, err error)
}

// Dispatcher 是唯一发送路径的实现。
type Dispatcher struct {
	supervisor contracts.Supervisor
	workspaces Workspaces
	log        func(sender Sender, req SendRequest, receipt Receipt, err error)
}

// New 创建调度层。缺件直接失败，不做隐式兜底（INV-2）。
func New(cfg Config) (*Dispatcher, error) {
	if cfg.Supervisor == nil {
		return nil, fmt.Errorf("dispatch: 需要 Supervisor（按工作区取 Core 客户端）")
	}
	if cfg.Workspaces == nil {
		return nil, fmt.Errorf("dispatch: 需要 Workspaces（工作区解析）")
	}
	logf := cfg.Log
	if logf == nil {
		logf = func(Sender, SendRequest, Receipt, error) {}
	}
	return &Dispatcher{supervisor: cfg.Supervisor, workspaces: cfg.Workspaces, log: logf}, nil
}

// ListWorkspaces 返回可选工作区（/api/workspaces 的数据面）。
func (d *Dispatcher) ListWorkspaces(context.Context, Sender) []Workspace {
	return d.workspaces.List()
}

// DefaultWorkspace 返回默认工作区。
func (d *Dispatcher) DefaultWorkspace() string { return d.workspaces.Default() }

// Send 投递一条消息（设计 4.2 的 1–6 步）。
func (d *Dispatcher) Send(ctx context.Context, sender Sender, req SendRequest) (Receipt, error) {
	receipt, err := d.send(ctx, sender, req)
	d.log(sender, req, receipt, err)
	return receipt, err
}

func (d *Dispatcher) send(ctx context.Context, sender Sender, req SendRequest) (Receipt, error) {
	empty := strings.TrimSpace(req.Text) == "" && len(req.Images) == 0
	if empty && (!req.AllowEmpty || strings.TrimSpace(req.ThreadID) != "") {
		return Receipt{}, sendErr(CodeEmptyMessage, "消息既没有文本也没有图片", nil)
	}
	workspace, err := d.resolveWorkspace(req.Workspace)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{Workspace: workspace}

	client, err := d.supervisor.Acquire(ctx, workspace)
	if err != nil {
		return receipt, err
	}
	defer d.supervisor.Release(workspace)

	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		threadID, err = createThread(ctx, client, req.Title)
		if err != nil {
			return receipt, err
		}
	}
	receipt.ThreadID = threadID
	if empty {
		// 只建线程：新建会话页允许「先开个空白会话」。
		return receipt, nil
	}

	mode := req.Mode
	if mode == "" {
		mode = DeliveryAuto
	}
	status := turnStatus(ctx, client, threadID)
	reached, err := d.deliver(ctx, client, sender, req, threadID, mode, status)
	receipt.Mode = reached
	if err != nil {
		return receipt, err
	}
	return receipt, nil
}

// deliver 按线程状态与投递模式决定动作。
func (d *Dispatcher) deliver(ctx context.Context, client contracts.CoreClient, sender Sender,
	req SendRequest, threadID string, mode DeliveryMode, status string) (DeliveryMode, error) {

	startParams := map[string]any{
		"threadId":       threadID,
		"idempotencyKey": idempotencyKey(req, sender, threadID),
		"text":           req.Text,
	}
	if len(req.Images) > 0 {
		startParams["images"] = imageParams(req.Images)
	}
	steerParams := map[string]any{"threadId": threadID, "text": req.Text}
	if len(req.Images) > 0 {
		steerParams["images"] = imageParams(req.Images)
	}

	switch status {
	case "running":
		if mode == DeliveryStart {
			return DeliveryStart, sendErr(CodeThreadBusy, "线程已有运行中轮次", nil)
		}
		if _, err := client.Call(ctx, "turn.steer", steerParams); err != nil {
			if mode == DeliverySteer {
				return DeliverySteer, err
			}
			// 轮次可能在 turn.get 与 turn.steer 之间结束：此时消息并没有进队，
			// 退回 turn.start 比把一条本来能发的消息变成错误更合理。
			if _, startErr := client.Call(ctx, "turn.start", startParams); startErr != nil {
				return DeliveryStart, startErr
			}
			return DeliveryStart, nil
		}
		return DeliverySteer, nil
	case "waiting_input":
		return "", sendErr(CodeWaitingInput, "线程在等待用户回答交互", nil)
	case "finishing":
		return "", sendErr(CodeFinishing, "线程正在收尾", nil)
	}

	if mode == DeliverySteer {
		return DeliverySteer, sendErr(CodeNotRunning, "线程没有运行中的轮次", nil)
	}
	if _, err := client.Call(ctx, "turn.start", startParams); err != nil {
		return DeliveryStart, err
	}
	return DeliveryStart, nil
}

// resolveWorkspace 校验并规范化工作区。
func (d *Dispatcher) resolveWorkspace(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		def := d.workspaces.Default()
		if def == "" {
			return "", sendErr(CodeWorkspaceMissing, "没有可用工作区", nil)
		}
		return def, nil
	}
	resolved, ok := d.workspaces.Resolve(raw)
	if !ok {
		return "", sendErr(CodeWorkspaceMissing, "工作区不存在或不可访问: "+raw, nil)
	}
	return resolved, nil
}

// createThread 建线程并返回 id。
func createThread(ctx context.Context, client contracts.CoreClient, title string) (string, error) {
	params := map[string]any{}
	if strings.TrimSpace(title) != "" {
		params["title"] = title
	}
	raw, err := client.Call(ctx, "thread.create", params)
	if err != nil {
		return "", err
	}
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", sendErr(CodeThreadMissing, "thread.create 返回不是合法 JSON", err)
	}
	if res.Thread.ID == "" {
		return "", sendErr(CodeThreadMissing, "thread.create 缺少 thread.id", nil)
	}
	return res.Thread.ID, nil
}

// turnStatus 读取线程当前轮次状态；读不到返回空串，让调用方按空闲处理
// （此时 turn.start 会给出真正的错误，而不是把 Core 故障伪装成排队）。
func turnStatus(ctx context.Context, client contracts.CoreClient, threadID string) string {
	raw, err := client.Call(ctx, "turn.get", map[string]any{"threadId": threadID})
	if err != nil {
		return ""
	}
	var res struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return ""
	}
	return res.Run.Status
}

// idempotencyKey 取调用方给的键；缺省生成一次性键。
//
// 刻意不用「文本 + 线程」推导：用户连着发两条相同的消息是两次真实意图，
// 推导出的键会把第二条当成重试吞掉。需要「重复投递只生效一次」的调用方
// （定时任务的「第几分钟」、表单的双击保护）必须自己传稳定的键。
func idempotencyKey(req SendRequest, sender Sender, threadID string) string {
	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		return key
	}
	return fmt.Sprintf("dispatch:%s:%s:%d", sender.key(), threadID, nextKeySeq())
}

var keySeq atomic.Uint64

func nextKeySeq() uint64 { return keySeq.Add(1) }

// imageParams 把图片部件转成 turn.start/turn.steer 的 images 形态。
func imageParams(parts []domain.Part) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		item := map[string]any{"type": string(p.Type)}
		if p.Mime != "" {
			item["mime"] = p.Mime
		}
		if p.Data != "" {
			item["data"] = p.Data
		}
		if p.Text != "" {
			item["text"] = p.Text
		}
		out = append(out, item)
	}
	return out
}
