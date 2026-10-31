package corerpc

// Session 是 Core RPC 对会话实现的依赖面。
//
// 这里刻意不 import internal/cli：RPC 传输层只依赖接口与共享类型，
// 由 cmd/adag 注入 cli.Session。这样 corerpc 的依赖闭包不会带进
// CLI/组合根（app → plugins/official）。

import (
	"context"
	"encoding/json"

	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/corecontracts"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/plugins/provider"
	"github.com/adag/ada-go/internal/ports"
	"github.com/adag/ada-go/internal/proto"
)

// Session 是 corerpc 需要的最小会话面。
type Session interface {
	Product() *coreapp.Product
	Plugins() []*host.Instance
	Tools(ctx context.Context) []domain.Descriptor
	ProviderIDs() []string
	ProviderModels(providerID string) []host.ModelInfo
	ProviderConfigs() []provider.Config

	Store() *sqlite.Store
	Workspace() string
	DataDir() string
	BaseURL() string
	Subscribe(fn func(domain.Event)) func()

	CreateThread(ctx context.Context, title string) (ports.Thread, error)
	GetThread(ctx context.Context, id string) (ports.Thread, error)
	ListThreads(ctx context.Context, req ports.ListThreadsRequest) (ports.ThreadPage, error)
	EntriesBefore(ctx context.Context, threadID string, fromSeq, limit int64) (ports.EntriesPage, error)
	PendingInteractions(ctx context.Context, threadID string) ([]ports.InteractionRequest, error)
	RespondInteraction(ctx context.Context, threadID, requestID string, status domain.InteractionStatus, value string) (ports.InteractionResolution, error)
	RunInThreadReady(ctx context.Context, threadID, runID, prompt string, images []domain.Part, ready chan<- struct{}) (domain.TerminalReason, error)
	EnqueueSteering(threadID, text string, images []domain.Part) (int, error)
	SteeringQueueLength(threadID string) int
	NewRunID(ctx context.Context) (string, error)

	ModelRef() ports.ModelRef
	ModelRefFor(threadID string) ports.ModelRef
	ResolveModel(providerID, modelID string) (ports.ModelRef, bool)
	ModelError() error
	SetWorkspaceModel(ref ports.ModelRef)
	SetThreadModel(threadID string, ref ports.ModelRef)
	ForgetThreadModel(threadID string)
	LastSeq(threadID string) int64
	RefreshProviderConfigs()

	CallAction(ctx context.Context, pluginID, action string, input json.RawMessage, threadID string) (json.RawMessage, error)
	CallHTTPAPI(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string) (json.RawMessage, error)
	CallHTTPAPIStream(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string, onEvent func(host.RouteEvent) error) error
	CheckHTTPAPIStream(pluginID, method, path string) error

	UpdatePolicies(up corecontracts.PolicyUpdate) (coreapp.Policies, error)
	Changes(threadID string, limit int, cursor string) (corecontracts.ChangePage, error)
	Revert(ctx context.Context, req corecontracts.RevertRequest) (proto.RevertResult, error)
	Audit(ctx context.Context, kind, threadID, pluginID string, detail any)

	PluginViews(ctx context.Context) []corecontracts.PluginView
	PluginViewsDetailed(ctx context.Context, detailID string) []corecontracts.PluginView
	ReloadPlugins(ctx context.Context, pluginID string) ([]corecontracts.PluginView, error)
	SetPluginEnabled(ctx context.Context, pluginID string, enabled bool) (corecontracts.PluginView, error)
	SkillSummaries() ([]domain.SkillMeta, []string)
}
