package engine

// ensureToolResults 是请求前的最后一道兜底：历史里 assistant 声明的 toolCalls
// 必须逐个有回执，否则 OpenAI 兼容端点会整轮拒绝（"Tool result is missing …"）。
// 正常路径由 runTools 的 defer 保证；这里钉住「已经坏掉的历史」也能被补上。

import (
	"reflect"
	"testing"

	"github.com/adag/ada-go/internal/domain"
)

func toolResult(callID, text string) domain.Message {
	return domain.Message{
		Role:   domain.RoleTool,
		CallID: callID,
		Parts:  []domain.Part{{Type: domain.PartText, Text: text}},
	}
}

func userText(text string) domain.Message {
	return domain.Message{Role: domain.RoleUser, Parts: []domain.Part{{Type: domain.PartText, Text: text}}}
}

func TestEnsureToolResultsFillsMissingReceipts(t *testing.T) {
	assistant := domain.Message{
		Role: domain.RoleAssistant,
		ToolCalls: []domain.ToolCall{
			{CallID: "call_1", Tool: "run_command"},
			{CallID: "call_2", Tool: "run_command"},
		},
	}
	msgs := []domain.Message{
		userText("跑构建和测试"),
		assistant,
		toolResult("call_1", "exit 0"),
		userText("继续"),
	}

	got := ensureToolResults(msgs)
	if len(got) != 5 {
		t.Fatalf("补齐后消息数 = %d，期望 5：%+v", len(got), got)
	}
	// 合成的回执紧跟声明它的 assistant 消息。
	if got[2].Role != domain.RoleTool || got[2].CallID != "call_2" {
		t.Fatalf("第 3 条 = %+v，期望 call_2 的合成回执", got[2])
	}
	if len(got[2].Parts) == 0 || got[2].Parts[0].Type != domain.PartText {
		t.Fatalf("合成回执缺少文字结果：%+v", got[2])
	}
	// 原有回执与后续用户消息必须原样保留。
	if !reflect.DeepEqual(got[3], toolResult("call_1", "exit 0")) {
		t.Fatalf("call_1 的回执被改写：%+v", got[3])
	}
	if got[4].Role != domain.RoleUser {
		t.Fatalf("第 5 条 = %+v，期望用户消息", got[4])
	}

	// 幂等：对补齐后的序列再跑一次不能再加。
	if again := ensureToolResults(got); len(again) != len(got) {
		t.Fatalf("重复补齐改变了消息数：%d → %d", len(got), len(again))
	}
}

func TestEnsureToolResultsLeavesCompleteHistoryAlone(t *testing.T) {
	msgs := []domain.Message{
		userText("跑构建和测试"),
		{
			Role: domain.RoleAssistant,
			ToolCalls: []domain.ToolCall{
				{CallID: "call_1", Tool: "run_command"},
				{CallID: "call_2", Tool: "run_command"},
			},
		},
		toolResult("call_1", "exit 0"),
		toolResult("call_2", "exit 0"),
		userText("继续"),
	}
	got := ensureToolResults(msgs)
	if len(got) != len(msgs) {
		t.Fatalf("完整历史被改动：%d → %d", len(msgs), len(got))
	}
	for i := range msgs {
		if !reflect.DeepEqual(got[i], msgs[i]) {
			t.Fatalf("第 %d 条被改写：%+v", i, got[i])
		}
	}
}

// 没有工具调用的普通对话不应被插入任何消息。
func TestEnsureToolResultsNoopForPlainChat(t *testing.T) {
	msgs := []domain.Message{
		userText("你好"),
		{Role: domain.RoleAssistant, Parts: []domain.Part{{Type: domain.PartText, Text: "你好"}}},
		userText("继续"),
	}
	if got := ensureToolResults(msgs); len(got) != 3 {
		t.Fatalf("普通对话被改动：%+v", got)
	}
}
