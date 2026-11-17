package web

import (
	"html/template"
)

// ---- 模板视图 ----

// baseData 是 shell/login/error 共用的最小外壳字段。整页 SSR 移除后，
// 页面内容由 /static/ada 运行时装载；这里只保留身份/工作区/CSRF 与
// bootstrap JSON 投影所需的数据。
type baseData struct {
	Title         string
	ProductName   string
	BrandName     string
	ProductBadge  string
	Locale        string
	Identity      string
	LoginPath     string
	CSRF          string
	Workspace     string
	WorkspaceName string
	// Workspaces 是工作区选择器的选项（bootstrap JSON 一并带给客户端）。
	Workspaces []workspaceOption
	// Gateway 是网关地址（bootstrap JSON 与「未连接」提示用）。
	Gateway string
	// Flash 是提示条 HTML（空 = 不渲染）；PRG 回跳的 ?error=/?notice= 仍由它表达。
	Flash template.HTML
	// BootstrapJSON 是 <script type="application/json" id="ada-bootstrap"> 的内容：
	// app/workspace/csrf 投影。类型是 template.JS（script 上下文原样插入；
	// json.Marshal 已把 < > & 转义成 \uXXXX，不会提前闭合 script 标签）。
	BootstrapJSON template.JS
}

// bootstrapData 是 ada-bootstrap JSON 的形状（dev-plan §F6/S1：shell 内联 props）。
type bootstrapData struct {
	Route          string            `json:"route"`
	Locale         string            `json:"locale"`
	Title          string            `json:"title"`
	ProductName    string            `json:"productName"`
	BrandName      string            `json:"brandName"`
	ProductBadge   string            `json:"productBadge,omitempty"`
	Identity       string            `json:"identity,omitempty"`
	LoginPath      string            `json:"loginPath"`
	CSRF           string            `json:"csrf,omitempty"`
	Workspace      string            `json:"workspace"`
	WorkspaceName  string            `json:"workspaceName"`
	WorkspaceIndex int               `json:"workspaceIndex"`
	Workspaces     []workspaceOption `json:"workspaces,omitempty"`
	Gateway        string            `json:"gateway"`
	// 版本投影供「设置 · 关于与更新」展示（2026-11-16 重设计）。
	Version         string `json:"version,omitempty"`
	Revision        string `json:"revision,omitempty"`
	BuildTime       string `json:"buildTime,omitempty"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
}

// workspaceOption 是工作区选择器的一项。
type workspaceOption struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Path  string `json:"path"`
}

type errorData struct {
	baseData
	Status     int
	Heading    string
	Message    string
	Detail     string
	RetryPath  string
	RetryLabel string
}

type loginData struct {
	baseData
}

// 拆分说明（纯搬移）：Core 侧最小 DTO 见 dto.go，JSON 端点响应形态见 payloads.go，
// 对话流中间形态与转换器见 transcript.go（展示层截断阈值也随转换器在那边）。
