package cli_test

import (
	"context"
	"testing"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/ports"
)

// 线程/工作区模型选择必须跨 Core 重启保留：桌面壳热更新、空闲回收和崩溃
// 恢复都会重新 Open 同一个 state.db。以前只存在 Session 内存里，重开即丢。
func TestModelSelectionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t, "")

	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	th, err := sess.NewThread(ctx, "模型持久化")
	if err != nil {
		t.Fatalf("NewThread: %v", err)
	}
	threadRef := ports.ModelRef{ProviderID: "fake", ModelID: "fake-1", ThinkingLevel: "high"}
	if err := sess.SaveThreadModel(ctx, th.ID, threadRef); err != nil {
		t.Fatalf("SaveThreadModel: %v", err)
	}
	workspaceRef := ports.ModelRef{ProviderID: "fake", ModelID: "fake-2", ThinkingLevel: "low"}
	if err := sess.SaveWorkspaceModel(ctx, workspaceRef); err != nil {
		t.Fatalf("SaveWorkspaceModel: %v", err)
	}
	if err := sess.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })

	got := reopened.ModelRefFor(th.ID)
	if got.ProviderID != threadRef.ProviderID || got.ModelID != threadRef.ModelID || got.ThinkingLevel != threadRef.ThinkingLevel {
		t.Fatalf("线程模型 = %+v，期望 %+v", got, threadRef)
	}
	gotWS := reopened.ModelRef()
	if gotWS.ProviderID != workspaceRef.ProviderID || gotWS.ModelID != workspaceRef.ModelID || gotWS.ThinkingLevel != workspaceRef.ThinkingLevel {
		t.Fatalf("工作区模型 = %+v，期望 %+v", gotWS, workspaceRef)
	}
}
