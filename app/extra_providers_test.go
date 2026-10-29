package app_test

// provider-anthropic-messages 与 provider-openai-responses 的端到端验收：
// 真实官方插件 × 真实宿主 × 真实 HTTP/SSE 桥（与 provider_test.go 的 openai-chat 同一模式）。
//
// 每个插件覆盖：多分片文本流、工具调用参数分片 + finish、usage、历史回放（消息形状）、
// thinking 流、缺失终止信号（EOF ≠ 完成）、错误帧、401 不可重试、502 只重试建连阶段。

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
	"github.com/adag/ada-go/plugins/official"
)

func streamAssembly(t *testing.T, pluginID, serverURL string) *app.Assembly {
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
	p := testProduct(t, pluginID)
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

// sseWriter 让夹具按「event: X\ndata: {...}\n\n」逐帧下发（vendor 两家都带 event 名）。
func sseWriter(w http.ResponseWriter) func(event, data string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	return func(event, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// streamTape 是一次流的收集结果：增量序列、文本/思考/参数片段、usage 与 finish。
type streamTape struct {
	kinds     []string
	text      strings.Builder
	thinking  strings.Builder
	fragments strings.Builder
	starts    []ports.Delta
	usage     *domain.Usage
	finish    *ports.Delta
}

// drain 消费整条流；io.EOF 是正常收尾（返回 nil 错误），其余错误原样返回。
func drain(t *testing.T, s ports.Stream) (streamTape, error) {
	t.Helper()
	var tape streamTape
	for {
		d, err := s.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return tape, nil
			}
			return tape, err
		}
		tape.kinds = append(tape.kinds, string(d.Kind))
		switch d.Kind {
		case ports.DeltaTextDelta:
			tape.text.WriteString(d.Text)
		case ports.DeltaThinkingDelta:
			tape.thinking.WriteString(d.Text)
		case ports.DeltaToolCallDelta:
			tape.fragments.WriteString(d.ToolCallFragment)
		case ports.DeltaToolCallStart:
			tape.starts = append(tape.starts, d)
		case ports.DeltaUsage:
			tape.usage = d.Usage
		case ports.DeltaFinish:
			fin := d
			tape.finish = &fin
		}
	}
}

func sequence(tape streamTape) string { return strings.Join(tape.kinds, ",") }

func assertFinish(t *testing.T, tape streamTape, reason, marker string) {
	t.Helper()
	if tape.finish == nil {
		t.Fatal("流没有 finish 增量")
	}
	if tape.finish.FinishReason != reason || tape.finish.StartMarker != marker {
		t.Fatalf("finish = {reason:%q marker:%q}，期望 {%q %q}",
			tape.finish.FinishReason, tape.finish.StartMarker, reason, marker)
	}
}

func toolSchema() []ports.ToolSchema {
	return []ports.ToolSchema{{
		Name:        "read_file",
		Description: "读文件",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}
}

// replayMessages 是工具调用回放的最小历史：用户问 → 助手带工具调用 → 工具回执。
func replayMessages(withText bool) []domain.Message {
	assistant := domain.Message{Role: domain.RoleAssistant}
	if withText {
		assistant.Parts = []domain.Part{{Type: domain.PartText, Text: "让我看看"}}
	}
	assistant.ToolCalls = []domain.ToolCall{{
		CallID: "call_1", Tool: "read_file", Args: json.RawMessage(`{"path":"/workspace/a.txt"}`),
	}}
	return []domain.Message{
		{Role: domain.RoleUser, Parts: []domain.Part{{Type: domain.PartText, Text: "read a.txt"}}},
		assistant,
		{Role: domain.RoleTool, CallID: "call_1", Parts: []domain.Part{{Type: domain.PartText, Text: "alpha\n"}}},
	}
}

// ---------------------------------------------------------------- anthropic

func TestAnthropicMessagesProviderStream(t *testing.T) {
	var gotPath string
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		send := sseWriter(w)
		send("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":12,"cache_read_input_tokens":3}}}`)
		send("ping", `{"type":"ping"}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pa"}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"th\":\"a.go\"}"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":1}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":7}}`)
		send("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-anthropic-messages", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model:  ports.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4.5", SupportsTools: true},
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
	tape, serr := drain(t, stream)
	if serr != nil {
		t.Fatalf("流错误: %v", serr)
	}
	if gotPath != "/messages" {
		t.Fatalf("请求路径 = %q", gotPath)
	}
	if tape.text.String() != "Hello" {
		t.Fatalf("文本增量 = %q", tape.text.String())
	}
	if tape.fragments.String() != `{"path":"a.go"}` {
		t.Fatalf("工具参数片段 = %q", tape.fragments.String())
	}
	if len(tape.starts) != 1 || tape.starts[0].Index != 1 ||
		tape.starts[0].CallID != "toolu_1" || tape.starts[0].ToolName != "read_file" {
		t.Fatalf("tool_call.start = %#v", tape.starts)
	}
	if tape.usage == nil || tape.usage.InputTokens != 12 || tape.usage.OutputTokens != 7 || tape.usage.CacheTokens != 3 {
		t.Fatalf("usage = %#v（期望 12/7/缓存 3）", tape.usage)
	}
	assertFinish(t, tape, "tool_use", "message_stop")
	want := "text.start,text.delta,text.delta,text.end,tool_call.start,tool_call.delta,tool_call.delta,tool_call.end,usage,finish"
	if got := sequence(tape); got != want {
		t.Fatalf("增量序列 =\n  %s\n期望\n  %s", got, want)
	}
	// system 是顶层字段；Anthropic 必填 max_tokens，缺省取模型声明的 maxOutput。
	if captured["system"] != "you are helpful" {
		t.Fatalf("system = %#v", captured["system"])
	}
	if captured["max_tokens"] != float64(64000) {
		t.Fatalf("max_tokens = %#v", captured["max_tokens"])
	}
	if captured["stream"] != true {
		t.Fatalf("请求没有 stream: %v", captured)
	}
}

func TestAnthropicMessagesProviderThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := sseWriter(w)
		send("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":5}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想一下"}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"再想一下"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"答案"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":1}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`)
		send("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-anthropic-messages", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4.5", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	tape, serr := drain(t, stream)
	if serr != nil {
		t.Fatalf("流错误: %v", serr)
	}
	if tape.thinking.String() != "想一下再想一下" {
		t.Fatalf("思考增量 = %q", tape.thinking.String())
	}
	if tape.text.String() != "答案" {
		t.Fatalf("文本增量 = %q", tape.text.String())
	}
	assertFinish(t, tape, "end_turn", "message_stop")
	want := "thinking.start,thinking.delta,thinking.delta,thinking.end,text.start,text.delta,text.end,usage,finish"
	if got := sequence(tape); got != want {
		t.Fatalf("增量序列 =\n  %s\n期望\n  %s", got, want)
	}
}

func TestAnthropicMessagesProviderMissingStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := sseWriter(w)
		send("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半截"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		// EOF：没有 message_delta/message_stop。
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-anthropic-messages", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4.5", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	tape, serr := drain(t, stream)
	if serr == nil {
		t.Fatal("EOF 没有 message_stop 必须报错（EOF 不等于完成）")
	}
	if !strings.Contains(serr.Error(), "message_stop") {
		t.Fatalf("错误应点名缺失的终止信号: %v", serr)
	}
	var de *domain.Error
	if !errors.As(serr, &de) {
		t.Fatalf("必须是业务错误: %T", serr)
	}
	if len(tape.kinds) == 0 {
		t.Fatal("错误前已到达的增量不应丢失")
	}
}

func TestAnthropicMessagesProviderErrorFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := sseWriter(w)
		send("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`)
		send("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-anthropic-messages", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4.5", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if _, serr := drain(t, stream); serr == nil {
		t.Fatal("错误帧必须上抛")
	} else if !strings.Contains(serr.Error(), "Overloaded") {
		t.Fatalf("错误应携带 provider 的说明: %v", serr)
	}
}

func TestAnthropicMessagesProviderRejectsNonRetryable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-anthropic-messages", srv.URL)
	retry := provider.NewRetry(asm.Providers)
	retry.MaxAttempts = 4
	retry.BaseDelay = time.Millisecond
	_, err := retry.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4.5", SupportsTools: true},
	})
	if err == nil {
		t.Fatal("401 必须失败")
	}
	if calls.Load() != 1 {
		t.Fatalf("401 被重试了 %d 次", calls.Load())
	}
}

func TestAnthropicMessagesProviderRetriesBeforeFirstDelta(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); n == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		send := sseWriter(w)
		send("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
		send("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-anthropic-messages", srv.URL)
	retry := provider.NewRetry(asm.Providers)
	retry.MaxAttempts = 3
	retry.BaseDelay = time.Millisecond
	stream, err := retry.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4.5", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("重试后仍失败: %v", err)
	}
	defer stream.Close()
	tape, serr := drain(t, stream)
	if serr != nil {
		t.Fatalf("重试后的流错误: %v", serr)
	}
	if calls.Load() != 2 {
		t.Fatalf("请求次数 = %d，期望 2（一次 502 + 一次成功）", calls.Load())
	}
	if tape.text.String() != "ok" {
		t.Fatalf("文本 = %q", tape.text.String())
	}
	if retry.Attempts() != 2 {
		t.Fatalf("Attempts = %d", retry.Attempts())
	}
}

func TestAnthropicMessagesProviderReplaysToolResults(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		send := sseWriter(w)
		send("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
		send("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-anthropic-messages", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model:    ports.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4.5", SupportsTools: true},
		System:   "you are helpful",
		Messages: replayMessages(true),
		Tools:    toolSchema(),
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if _, serr := drain(t, stream); serr != nil {
		t.Fatalf("流错误: %v", serr)
	}

	if captured["system"] != "you are helpful" {
		t.Fatalf("system 应该是顶层字段: %#v", captured["system"])
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("消息数 = %d: %#v", len(msgs), captured["messages"])
	}
	user, _ := msgs[0].(map[string]any)
	userBlocks, _ := user["content"].([]any)
	if user["role"] != "user" || len(userBlocks) != 1 {
		t.Fatalf("user 消息形状不对: %#v", user)
	}
	assistant, _ := msgs[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	if assistant["role"] != "assistant" || len(blocks) != 2 {
		t.Fatalf("assistant 消息形状不对: %#v", assistant)
	}
	textBlock, _ := blocks[0].(map[string]any)
	if textBlock["type"] != "text" || textBlock["text"] != "让我看看" {
		t.Fatalf("assistant 文本块不对: %#v", textBlock)
	}
	toolUse, _ := blocks[1].(map[string]any)
	input, _ := toolUse["input"].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "call_1" || toolUse["name"] != "read_file" ||
		input["path"] != "/workspace/a.txt" {
		t.Fatalf("tool_use 块形状不对: %#v", toolUse)
	}
	toolMsg, _ := msgs[2].(map[string]any)
	toolBlocks, _ := toolMsg["content"].([]any)
	if toolMsg["role"] != "user" || len(toolBlocks) != 1 {
		t.Fatalf("工具回执应在 user 轮: %#v", toolMsg)
	}
	result, _ := toolBlocks[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "call_1" || result["content"] != "alpha\n" {
		t.Fatalf("tool_result 块形状不对: %#v", result)
	}
	tools, _ := captured["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "read_file" || tool["description"] != "读文件" || tool["input_schema"] == nil {
		t.Fatalf("tools[0] 形状不对: %#v", tool)
	}
}

// ------------------------------------------------------------ openai responses

func TestOpenAIResponsesProviderStream(t *testing.T) {
	var gotPath string
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		send := sseWriter(w)
		send("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`)
		send("response.in_progress", `{"type":"response.in_progress","response":{"id":"resp_1"}}`)
		send("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`)
		send("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_0","output_index":0,"content_index":0,"delta":"Hel"}`)
		send("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_0","output_index":0,"content_index":0,"delta":"lo"}`)
		send("response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":""}}`)
		send("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"pa"}`)
		send("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"th\":\"a.go\"}"}`)
		send("response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}}`)
		send("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":9,"input_tokens_details":{"cached_tokens":4},"output_tokens":6}}}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-openai-responses", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model:  ports.ModelRef{ProviderID: "openai-responses", ModelID: "gpt-5.1", SupportsTools: true},
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
	tape, serr := drain(t, stream)
	if serr != nil {
		t.Fatalf("流错误: %v", serr)
	}
	if gotPath != "/responses" {
		t.Fatalf("请求路径 = %q", gotPath)
	}
	if tape.text.String() != "Hello" {
		t.Fatalf("文本增量 = %q", tape.text.String())
	}
	if tape.fragments.String() != `{"path":"a.go"}` {
		t.Fatalf("工具参数片段 = %q", tape.fragments.String())
	}
	if len(tape.starts) != 1 || tape.starts[0].Index != 1 ||
		tape.starts[0].CallID != "call_1" || tape.starts[0].ToolName != "read_file" {
		t.Fatalf("tool_call.start = %#v", tape.starts)
	}
	if tape.usage == nil || tape.usage.InputTokens != 9 || tape.usage.OutputTokens != 6 || tape.usage.CacheTokens != 4 {
		t.Fatalf("usage = %#v（期望 9/6/缓存 4）", tape.usage)
	}
	assertFinish(t, tape, "stop", "response.completed")
	want := "text.start,text.delta,text.delta,tool_call.start,tool_call.delta,tool_call.delta,usage,tool_call.end,text.end,finish"
	if got := sequence(tape); got != want {
		t.Fatalf("增量序列 =\n  %s\n期望\n  %s", got, want)
	}
	// system 走顶层 instructions；input 是 Responses 消息形状。
	if captured["instructions"] != "you are helpful" {
		t.Fatalf("instructions = %#v", captured["instructions"])
	}
	if captured["stream"] != true {
		t.Fatalf("请求没有 stream: %v", captured)
	}
	input, _ := captured["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %#v", captured["input"])
	}
}

func TestOpenAIResponsesProviderThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := sseWriter(w)
		send("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[]}}`)
		send("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","item_id":"rs_0","output_index":0,"delta":"思考A"}`)
		send("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","item_id":"rs_0","output_index":0,"delta":"思考B"}`)
		send("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_0","output_index":1,"content_index":0,"delta":"答案"}`)
		send("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":3,"output_tokens":5}}}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-openai-responses", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai-responses", ModelID: "o4-mini", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	tape, serr := drain(t, stream)
	if serr != nil {
		t.Fatalf("流错误: %v", serr)
	}
	if tape.thinking.String() != "思考A思考B" {
		t.Fatalf("思考增量 = %q", tape.thinking.String())
	}
	if tape.text.String() != "答案" {
		t.Fatalf("文本增量 = %q", tape.text.String())
	}
	assertFinish(t, tape, "stop", "response.completed")
	want := "thinking.start,thinking.delta,thinking.delta,text.start,text.delta,usage,text.end,thinking.end,finish"
	if got := sequence(tape); got != want {
		t.Fatalf("增量序列 =\n  %s\n期望\n  %s", got, want)
	}
}

func TestOpenAIResponsesProviderMissingCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := sseWriter(w)
		send("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_0","output_index":0,"delta":"半截"}`)
		// EOF：没有 response.completed。
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-openai-responses", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai-responses", ModelID: "gpt-5.1", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	tape, serr := drain(t, stream)
	if serr == nil {
		t.Fatal("EOF 没有 response.completed 必须报错（EOF 不等于完成）")
	}
	if !strings.Contains(serr.Error(), "response.completed") {
		t.Fatalf("错误应点名缺失的终止信号: %v", serr)
	}
	var de *domain.Error
	if !errors.As(serr, &de) {
		t.Fatalf("必须是业务错误: %T", serr)
	}
	if len(tape.kinds) == 0 {
		t.Fatal("错误前已到达的增量不应丢失")
	}
}

func TestOpenAIResponsesProviderFailedFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := sseWriter(w)
		send("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`)
		send("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_0","output_index":0,"delta":"Hi"}`)
		send("response.failed", `{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"boom"}}}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-openai-responses", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai-responses", ModelID: "gpt-5.1", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if _, serr := drain(t, stream); serr == nil {
		t.Fatal("response.failed 必须上抛")
	} else if !strings.Contains(serr.Error(), "boom") {
		t.Fatalf("错误应携带 provider 的说明: %v", serr)
	}
}

func TestOpenAIResponsesProviderRejectsNonRetryable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-openai-responses", srv.URL)
	retry := provider.NewRetry(asm.Providers)
	retry.MaxAttempts = 4
	retry.BaseDelay = time.Millisecond
	_, err := retry.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai-responses", ModelID: "gpt-5.1", SupportsTools: true},
	})
	if err == nil {
		t.Fatal("401 必须失败")
	}
	if calls.Load() != 1 {
		t.Fatalf("401 被重试了 %d 次", calls.Load())
	}
}

func TestOpenAIResponsesProviderRetriesBeforeFirstDelta(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); n == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		send := sseWriter(w)
		send("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_0","output_index":0,"delta":"ok"}`)
		send("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-openai-responses", srv.URL)
	retry := provider.NewRetry(asm.Providers)
	retry.MaxAttempts = 3
	retry.BaseDelay = time.Millisecond
	stream, err := retry.Stream(context.Background(), ports.Request{
		Model: ports.ModelRef{ProviderID: "openai-responses", ModelID: "gpt-5.1", SupportsTools: true},
	})
	if err != nil {
		t.Fatalf("重试后仍失败: %v", err)
	}
	defer stream.Close()
	tape, serr := drain(t, stream)
	if serr != nil {
		t.Fatalf("重试后的流错误: %v", serr)
	}
	if calls.Load() != 2 {
		t.Fatalf("请求次数 = %d，期望 2（一次 502 + 一次成功）", calls.Load())
	}
	if tape.text.String() != "ok" {
		t.Fatalf("文本 = %q", tape.text.String())
	}
	if retry.Attempts() != 2 {
		t.Fatalf("Attempts = %d", retry.Attempts())
	}
}

func TestOpenAIResponsesProviderReplaysToolResults(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		send := sseWriter(w)
		send("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_0","output_index":0,"delta":"done"}`)
		send("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)
	}))
	defer srv.Close()

	asm := streamAssembly(t, "provider-openai-responses", srv.URL)
	stream, err := asm.Providers.Stream(context.Background(), ports.Request{
		Model:    ports.ModelRef{ProviderID: "openai-responses", ModelID: "gpt-5.1", SupportsTools: true},
		System:   "you are helpful",
		Messages: replayMessages(true),
		Tools:    toolSchema(),
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if _, serr := drain(t, stream); serr != nil {
		t.Fatalf("流错误: %v", serr)
	}

	if captured["instructions"] != "you are helpful" {
		t.Fatalf("instructions 应承载 system: %#v", captured["instructions"])
	}
	input, _ := captured["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input 项数 = %d: %#v", len(input), captured["input"])
	}
	user, _ := input[0].(map[string]any)
	userContent, _ := user["content"].([]any)
	userText, _ := userContent[0].(map[string]any)
	if user["role"] != "user" || userText["type"] != "input_text" || userText["text"] != "read a.txt" {
		t.Fatalf("user 消息形状不对: %#v", user)
	}
	assistant, _ := input[1].(map[string]any)
	assistantContent, _ := assistant["content"].([]any)
	assistantText, _ := assistantContent[0].(map[string]any)
	if assistant["role"] != "assistant" || assistantText["type"] != "output_text" || assistantText["text"] != "让我看看" {
		t.Fatalf("assistant 消息形状不对: %#v", assistant)
	}
	call, _ := input[2].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "read_file" ||
		call["arguments"] != `{"path":"/workspace/a.txt"}` {
		t.Fatalf("function_call 形状不对: %#v", call)
	}
	output, _ := input[3].(map[string]any)
	if output["type"] != "function_call_output" || output["call_id"] != "call_1" || output["output"] != "alpha\n" {
		t.Fatalf("function_call_output 形状不对: %#v", output)
	}
	tools, _ := captured["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "read_file" || tool["description"] != "读文件" || tool["parameters"] == nil {
		t.Fatalf("tools[0] 应是扁平 function 声明: %#v", tool)
	}
}
