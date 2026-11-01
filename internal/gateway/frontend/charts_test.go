package frontend_test

// 官方 ui-charts 的通用图表块：一个模板按 renderer 分出六种形态。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/plugins/official"
)

func TestOfficialChartBlocks(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	b, err := frontend.NewBridge(frontend.BridgeOptions{Scripts: scripts, MarkdownBlocks: true})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	cases := []struct {
		renderer string
		data     string
		want     []string
	}{
		{
			"ui-charts/bar",
			`{"title":"对比","unit":"★","items":[{"label":"a","value":10},{"label":"b","value":5}]}`,
			[]string{"adag-chart__bar--l10", "adag-chart__name"},
		},
		{
			"ui-charts/column",
			`{"title":"对比","items":[{"label":"a","value":10},{"label":"b","value":5}]}`,
			[]string{"adag-chart__cols", "adag-chart__colbar--h10"},
		},
		{
			"ui-charts/compare",
			`{"title":"对比","items":[{"label":"a","value":10,"value2":4}]}`,
			[]string{"adag-chart__pair", "adag-chart__bar--alt", "adag-chart__legend"},
		},
		{
			"ui-charts/stacked",
			`{"title":"占比","items":[{"label":"a","value":3},{"label":"b","value":1}]}`,
			[]string{"adag-chart__stack", "adag-chart__seg--a", "adag-chart__dot--b"},
		},
		{
			"ui-charts/heatmap",
			`{"title":"热力","items":[{"label":"a","value":1},{"label":"b","value":5}]}`,
			[]string{"adag-heat", "adag-heat__cell--l5"},
		},
		{
			"ui-charts/spark",
			`{"title":"趋势","values":[1,2,3,2,4]}`,
			[]string{"adag-spark"},
		},
		{
			// 全平序列画中位符号，不能把每条都压成最低点。
			"ui-charts/spark",
			`{"title":"持平","values":[5,5,5]}`,
			[]string{"▄▄▄"},
		},
	}
	for _, tc := range cases {
		raw, html, err := renderBlock(b, request(t, "/thread/th_1"), tc.renderer, "b1", json.RawMessage(tc.data))
		if err != nil {
			t.Fatalf("%s: %v", tc.renderer, err)
		}
		if len(raw) == 0 {
			t.Fatalf("%s: 空 VNode", tc.renderer)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(html), want) {
				t.Fatalf("%s: HTML 缺少 %q\n%s", tc.renderer, want, html)
			}
		}
	}
}

// TestOfficialChartBlocksRejectBadData 钉住通用 Schema：未知字段要降级而不是渲染。
func TestOfficialChartBlocksRejectBadData(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	var events []frontend.BlockAuditEvent
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: scripts, MarkdownBlocks: true,
		BlockAudit: func(e frontend.BlockAuditEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if _, _, err := renderBlock(b, request(t, "/thread/th_1"), "ui-charts/bar", "b1",
		json.RawMessage(`{"title":"t","items":[{"label":"a","value":1}],"bogus":true}`)); err == nil {
		t.Fatal("未知字段应被 Schema 拒绝")
	}
	if len(events) != 1 || events[0].Kind != "ui.block.invalid_data" || events[0].Error != "schema_validation" {
		t.Fatalf("审计 = %+v", events)
	}
}

// TestOfficialChartBlocksEmpty 钉住空数据是「空块」而不是错误卡：
// chart.tsx 在 items/values 为空时返回 null，宿主按合法空结果处理。
func TestOfficialChartBlocksEmpty(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	var events []frontend.BlockAuditEvent
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: scripts, MarkdownBlocks: true,
		BlockAudit: func(e frontend.BlockAuditEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	cases := []struct{ renderer, data string }{
		{"ui-charts/bar", `{"title":"t"}`},
		{"ui-charts/bar", `{"title":"t","items":[]}`},
		{"ui-charts/spark", `{"title":"t","values":[]}`},
	}
	for _, tc := range cases {
		raw, html, err := renderBlock(b, request(t, "/thread/th_1"), tc.renderer, "b1", json.RawMessage(tc.data))
		if err != nil {
			t.Fatalf("%s %s: 空数据不应失败: %v", tc.renderer, tc.data, err)
		}
		if strings.TrimSpace(string(raw)) != "null" || html != "" {
			t.Fatalf("%s %s: 应渲染为空块，得到 %q / %q", tc.renderer, tc.data, raw, html)
		}
	}
	if len(events) != 0 {
		t.Fatalf("空数据不应产生审计: %+v", events)
	}
}
