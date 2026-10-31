// Package corecontracts 是 Core 会话层与 RPC 适配层共享的轻量类型。
//
// 这些类型原属于 internal/cli；下沉到独立包是为了让 internal/corerpc 不 import cli，
// 从而保持 Core 传输层与 CLI/组合根（app → plugins/official）解耦。
package corecontracts

import "github.com/adag/ada-go/internal/proto"

// PolicyUpdate 是策略参数的局部更新（nil 表示不变）。
type PolicyUpdate struct {
	ToolTimeoutMS   *int
	TurnTimeoutMS   *int
	MaxTurnTokens   *int
	LoopRepeatLimit *int
	LoopAbortLimit  *int
	ParallelTools   *int
}

// RevertRequest 是一次检查点回滚请求。
type RevertRequest struct {
	ThreadID string
	Scope    string
	// CheckpointID 是 scope=item 时的检查点 id（chk_...）；scope=all/file 时忽略。
	CheckpointID string
	// Path 是 scope=file 时的单文件路径（脚本视图或工作区相对均可）。
	Path string
}

// ChangePage 是 change.list 的分页结果。
type ChangePage struct {
	ChangeSets []proto.ChangeSet
	NextCursor string
	HasMore    bool
}

// PluginView 是插件列表/详情的一行（界面与 RPC 共用）。
type PluginView struct {
	ID          string
	Name        string
	Version     string
	APIVersion  int
	Trust       string
	Enabled     bool
	Status      string // ready | degraded | disabled | failed | unused
	Fingerprint string
	LastError   string
	// Source 是发现来源：""＝随包（官方）；workspace | global。
	Source string
	// Path 是插件所在的绝对目录（第三方或 Electron resources 里的官方插件）。
	Path  string
	Tools []string
	Hooks []string
	// Skills 是随包技能名（6.7 的 contributions.skills 计数来源）。
	Skills []string
	// Entry 是后端入口相对路径；ManifestCapabilities 是声明的能力位。
	// check_plugin 的诊断报告用。
	Entry                string
	ManifestCapabilities []string
	// Frontend / Backend 标明清单段；统一插件包可以两者都为真。
	Frontend bool
	Backend  bool
	// Readme 是插件 README 正文；没有 README 时回退为清单 description。
	Readme string
	// Files 是插件包内文件清单（详情页「插件目录」）。
	Files []PluginFile
}

// PluginFile 是插件包内一个文件（相对路径，不含宿主绝对路径）。
type PluginFile struct {
	Path  string
	Size  int64
	Dir   bool
	Depth int
}

// HTTPAPIError 是插件 HTTP 路由的业务状态错误（404/405），供 Core RPC 映射。
type HTTPAPIError struct {
	Status  int
	Message string
}

func (e *HTTPAPIError) Error() string { return e.Message }
