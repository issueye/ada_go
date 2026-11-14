package web

// 动态块的白盒测试：请求级预算与封套解析不对外暴露，用同包测试直接覆盖。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type budgetPlugUI struct{ calls int }

func (p *budgetPlugUI) RenderBlock(r *http.Request, rendererID, blockID string, version int, data json.RawMessage) (json.RawMessage, error) {
	p.calls++
	return json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block"},"c":[{"t":"#text","v":"block hi"}]}`), nil
}

// TestBlockBudgetTake 钉住请求级预算：块数上限、墙钟截止与 nil（不限）。
func TestBlockBudgetTake(t *testing.T) {
	b := newBlockBudget()
	for i := 0; i < MaxMarkdownBlocksPerRequest; i++ {
		if !b.take() {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if b.take() {
		t.Fatal("超过块数上限应拒绝")
	}

	expired := &blockBudget{remaining: 10, deadline: time.Now().Add(-time.Second)}
	if expired.take() {
		t.Fatal("超过墙钟预算应拒绝")
	}

	var nilBudget *blockBudget
	if !nilBudget.take() {
		t.Fatal("nil 预算应放行（单块/测试调用）")
	}
}

// TestBlockBudgetExhaustedFallsBack 钉住预算耗尽的降级方向：
// 不再执行渲染器，AST 里按普通代码块输出（内容仍可见）。
func TestBlockBudgetExhaustedFallsBack(t *testing.T) {
	ui := &budgetPlugUI{}
	srv := &Server{cfg: Config{PluginUI: ui}}
	text := "```adag-ui\n{\"renderer\":\"demo/card\",\"data\":{}}\n```"
	raw := srv.markdownNodesJSONWithBlocks(nil, text, &blockBudget{remaining: 0, deadline: time.Now().Add(time.Minute)})
	if strings.Contains(string(raw), "block hi") {
		t.Fatalf("预算耗尽后不应调用渲染器: %s", raw)
	}
	if !strings.Contains(string(raw), "language-adag-ui") {
		t.Fatalf("预算耗尽应退回普通代码块: %s", raw)
	}
	if ui.calls != 0 {
		t.Fatalf("渲染器调用了 %d 次，期望 0", ui.calls)
	}
}

// TestParseBlockEnvelope 钉住封套字段校验（类型/形状在 HTTP 层另有端到端用例）。
func TestParseBlockEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		version int
		wantErr bool
	}{
		{"缺省 version", `{"renderer":"demo/card","data":{}}`, 1, false},
		{"显式 version", `{"renderer":"demo/card","version":3,"data":{}}`, 3, false},
		{"未知字段", `{"renderer":"demo/card","data":{},"x":1}`, 0, true},
		{"尾随 JSON", `{"renderer":"demo/card","data":{}}[]`, 0, true},
		{"缺 data", `{"renderer":"demo/card"}`, 0, true},
		{"非法 renderer", `{"renderer":"Demo/Card","data":{}}`, 0, true},
		{"非法 id", `{"renderer":"demo/card","id":"has space","data":{}}`, 0, true},
		{"合法 id", `{"renderer":"demo/card","id":"a.b:c-1","data":{}}`, 1, false},
	} {
		env, err := parseBlockEnvelope(tc.payload)
		if gotErr := err != nil; gotErr != tc.wantErr {
			t.Fatalf("%s: err = %v，期望错误=%v", tc.name, err, tc.wantErr)
		}
		if !tc.wantErr && env.Version != tc.version {
			t.Fatalf("%s: version = %d，期望 %d", tc.name, env.Version, tc.version)
		}
	}
}
