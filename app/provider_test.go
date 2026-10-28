package app_test

// provider-openai-chat 的端到端验收：真实官方插件 × 真实宿主 × 真实 HTTP/SSE 桥。
//
// 用 httptest 冒充 OpenAI Chat Completions，断言归一化增量与显式终止信号；
// 另测 5xx 只重试建连阶段、以及非 2xx 在重试耗尽后上抛。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/plugins/provider"
	"github.com/adag/ada-go/internal/ports"
	"github.com/adag/ada-go/official"
)

func providerAssembly(t *testing.T, serverURL string) *app.Assembly {
	t.Helper()
	ws := tempDir(t)
	bridge, err := host.NewHTTPBridge(host.HTTPBridgeConfig{
		AllowAnyHost: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Close)
	hc := host.Context{
		Workspace: ws,
		HTTP:      bridge,
		Secret:    func(string) (string, bool) { return "sk-test", true },
		Log:       func(string, string, map[string]any) {},
	}
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	p := testProduct(t, "provider-openai-chat")
	asm, err := app.Assemble(context.Background(), p, scripts, app.Deps{Context: hc, BaseURL: serverURL})
	if err != nil {
		t.Fatalf("app.Assemble: %v", err)
	}
	t.Cleanup(func() { _ = asm.Close(context.Background()) })
	if asm.ProviderCount() != 1 {
		t.Fatalf("provider 数 = %d，期望 1", asm.ProviderCount())
	}
	return asm
}

func TestOpenAIChatProviderStream(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["stream"] != true {
			t.Errorf("请求没有 stream: %v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		send := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			if flusher != nil {
				flusher.Flush()
			}
		}
		send(`{"choices":[{"delta":{"content":"Hel"}}]}`)
		send(`{"choices":[{"delta":{"content":"lo"}}]}`)
		send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`)
		send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`)
		send(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		send(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2}}}`)
		send(`[DONE]`)
	}))
	defer srv.Close()

	asm := providerAssembly(t, srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model:  ports.ModelRef{ProviderID: "openai", ModelID: "gpt-4o", SupportsTools: true},
		System: "you are helpful",
		Messages: []domain.Message{{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Type: domain.PartText, Text: "你好"}},
		}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var kinds []string
	var text strings.Builder
	var fragment strings.Builder
	for {
		d, err := stream.Recv()
		if err != nil {
			break
		}
		kinds = append(kinds, string(d.Kind))
		switch d.Kind {
		case ports.DeltaTextDelta:
			text.WriteString(d.Text)
		case ports.DeltaToolCallDelta:
			fragment.WriteString(d.ToolCallFragment)
		}
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("请求路径 = %q", gotPath)
	}
	if text.String() != "Hello" {
		t.Fatalf("文本增量 = %q", text.String())
	}
	if fragment.String() != `{"path":"a.go"}` {
		t.Fatalf("工具参数片段 = %q", fragment.String())
	}
	want := "text.start,text.delta,text.delta,tool_call.start,tool_call.delta,tool_call.delta,usage,tool_call.end,text.end,finish"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("增量序列 =\n  %s\n期望\n  %s", strings.Join(kinds, ","), want)
	}
}

func TestOpenAIChatProviderRetriesBeforeFirstDelta(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	asm := providerAssembly(t, srv.URL)
	retry := provider.NewRetry(asm.Providers)
	retry.MaxAttempts = 3
	retry.BaseDelay = time.Millisecond
	stream, err := retry.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai", ModelID: "gpt-4o", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("重试后仍失败: %v", err)
	}
	defer stream.Close()
	var text strings.Builder
	for {
		d, rerr := stream.Recv()
		if rerr != nil {
			break
		}
		if d.Kind == ports.DeltaTextDelta {
			text.WriteString(d.Text)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("请求次数 = %d，期望 2（一次 502 + 一次成功）", calls.Load())
	}
	if text.String() != "ok" {
		t.Fatalf("文本 = %q", text.String())
	}
	if retry.Attempts() != 2 {
		t.Fatalf("Attempts = %d", retry.Attempts())
	}
}

func TestOpenAIChatProviderCancelReachesSSE(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	asm := providerAssembly(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := asm.Providers.Stream(ctx, ports.Request{
		Model: ports.ModelRef{ProviderID: "openai", ModelID: "gpt-4o", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	<-started
	cancel()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("取消没有贯穿到 provider SSE")
		default:
		}
		if _, err := stream.Recv(); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// host 侧可能先给出 turn_aborted 业务错误。
			var de *domain.Error
			if errors.As(err, &de) && de.Code == domain.CodeTurnAborted {
				return
			}
			t.Fatalf("取消后的错误 = %v", err)
		}
	}
}

func TestOpenAIChatProviderReplaysToolResults(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	asm := providerAssembly(t, srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai", ModelID: "gpt-4o", SupportsTools: true},
		Messages: []domain.Message{
			{Role: domain.RoleUser, Parts: []domain.Part{{Type: domain.PartText, Text: "read a.txt"}}},
			{
				Role:  domain.RoleAssistant,
				Parts: []domain.Part{{Type: domain.PartText, Text: "\n\n"}},
				ToolCalls: []domain.ToolCall{{
					CallID: "call_1", Tool: "read_file", Args: json.RawMessage(`{"path":"/workspace/a.txt"}`),
				}},
			},
			{
				Role:   domain.RoleTool,
				CallID: "call_1",
				Parts:  []domain.Part{{Type: domain.PartText, Text: "alpha\n"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}

	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("消息数 = %d: %#v", len(msgs), captured["messages"])
	}
	assistant, _ := msgs[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant.tool_calls = %#v", assistant["tool_calls"])
	}
	call, _ := calls[0].(map[string]any)
	fn, _ := call["function"].(map[string]any)
	if call["id"] != "call_1" || fn["name"] != "read_file" || fn["arguments"] != `{"path":"/workspace/a.txt"}` {
		t.Fatalf("tool_call 形状不对: %#v", call)
	}
	tool, _ := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["name"] != "read_file" || tool["content"] != "alpha\n" {
		t.Fatalf("tool 消息形状不对: %#v", tool)
	}
}

func TestOpenAIChatProviderRejectsNonRetryable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	asm := providerAssembly(t, srv.URL)
	retry := provider.NewRetry(asm.Providers)
	retry.MaxAttempts = 4
	retry.BaseDelay = time.Millisecond
	_, err := retry.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai", ModelID: "gpt-4o", SupportsTools: true},
	})
	if err == nil {
		t.Fatal("401 必须失败")
	}
	if calls.Load() != 1 {
		t.Fatalf("401 被重试了 %d 次", calls.Load())
	}
}
