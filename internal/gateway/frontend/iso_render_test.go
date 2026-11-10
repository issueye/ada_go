package frontend

// P2-1 服务端同构组件：ada-ui-iso.js 的 render(h) 定义必须展开成原生标签，
// 并通过 Go 的 VNode 白名单/转义出口，而不是残留 $C 组件名。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins/host"
)

const isoCardSource = `import { h, defineComponent } from "ada:render";
import { Badge, Card, Button } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <Card title="同构卡片" subtitle="P2-1">
        <Badge tone="brand" label="3/5" />
        <Button variant="primary">保存</Button>
      </Card>
    );
  },
});
`

func TestIsoComponentsRenderThroughGo(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-render.js"))
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	iso, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := host.NewRenderer(
		host.WithRenderEngineSource(string(engine)),
		host.WithIsoComponentsSource(string(iso)),
	)
	vnode, err := r.RenderIsland(isoCardSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	html, err := RenderHTML(vnode)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text := string(html)
	for _, want := range []string{
		`<section class="card">`,
		`<span class="badge badge-brand">3/5</span>`,
		`<button class="ada-btn ada-btn--primary ada-btn--md" type="button">`,
		`<span class="ada-btn__label">保存</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR HTML 缺少 %s\n%s", want, text)
		}
	}
	if strings.Contains(text, "$C") {
		t.Fatalf("SSR HTML 不应包含 $C：%s", text)
	}
}

const isoIconsSource = `import { h, defineComponent } from "ada:render";
import { Icon, TodoList, Notice } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <div class="iso-icons">
        <Icon name="i-list" size={12} />
        <Notice tone="success">已完成</Notice>
        <TodoList notes="备注" steps={[
          { step: "第一步", status: "done" },
          { step: "第二步", status: "in_progress" },
          { step: "第三步", status: "pending" },
        ]} />
      </div>
    );
  },
});
`

const isoTableSource = `import { h, defineComponent } from "ada:render";
import { Table } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <Table
        caption="模型列表"
        rowKey="id"
        columns={[
          { key: "name", label: "名称" },
          { key: "size", label: "大小", class: "is-size" },
        ]}
        rows={[
          { id: "a", name: "甲", size: "1K" },
          { id: "b", name: "乙", size: "2K" },
        ]}
      />
    );
  },
});
`

func TestIsoTableRenderThroughGo(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-render.js"))
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	iso, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := host.NewRenderer(
		host.WithRenderEngineSource(string(engine)),
		host.WithIsoComponentsSource(string(iso)),
	)
	vnode, err := r.RenderIsland(isoTableSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	html, err := RenderHTML(vnode)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text := string(html)
	for _, want := range []string{
		`<div class="ada-table-wrap">`,
		`<table class="table ada-table--hover">`,
		`<caption>模型列表</caption>`,
		`<th scope="col">名称</th>`,
		`<th class="is-size" scope="col">大小</th>`,
		`<td class="is-size">1K</td>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR HTML 缺少 %s\n%s", want, text)
		}
	}
}

const isoSelectSource = `import { h, defineComponent } from "ada:render";
import { Select } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <Select
        options={["a", { value: "b", label: "乙" }]}
        value="b"
        name="preset"
        class="cm-input"
        aria-label="选择预设"
      />
    );
  },
});
`

func TestIsoSelectRenderThroughGo(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-render.js"))
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	iso, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := host.NewRenderer(
		host.WithRenderEngineSource(string(engine)),
		host.WithIsoComponentsSource(string(iso)),
	)
	vnode, err := r.RenderIsland(isoSelectSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	html, err := RenderHTML(vnode)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text := string(html)
	for _, want := range []string{
		`<ada-select class="ada-select">`,
		`<button class="ada-select__trigger cm-input" type="button" data-select-trigger aria-expanded="false" aria-label="选择预设">`,
		`<span class="ada-select__label" data-select-label>乙</span>`,
		`class="ada-select__option is-selected" type="button" data-value="b" aria-selected="true">乙</button>`,
		`<input type="hidden" data-select-input name="preset" value="b">`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR HTML 缺少 %s\n%s", want, text)
		}
	}
}

const isoTabsSource = `import { h, defineComponent } from "ada:render";
import { Tabs } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <Tabs
        id="demo-tabs"
        modelValue="b"
        tabs={[
          { key: "a", label: "甲" },
          { key: "b", label: "乙", count: 2 },
        ]}
      />
    );
  },
});
`

func TestIsoTabsRenderThroughGo(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-render.js"))
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	iso, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := host.NewRenderer(
		host.WithRenderEngineSource(string(engine)),
		host.WithIsoComponentsSource(string(iso)),
	)
	vnode, err := r.RenderIsland(isoTabsSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	html, err := RenderHTML(vnode)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text := string(html)
	for _, want := range []string{
		`<div class="ada-tabs">`,
		`<div class="ada-tabs__list" role="tablist">`,
		`id="demo-tabs-tab-a"`,
		`aria-selected="false"`,
		`tabindex="-1"`,
		`aria-selected="true"`,
		`tabindex="0"`,
		`<span class="ada-tabs__count">2</span>`,
		`<div aria-labelledby="demo-tabs-tab-a" class="ada-tabs__panel" hidden id="demo-tabs-panel-a" role="tabpanel">`,
		`<div aria-labelledby="demo-tabs-tab-b" class="ada-tabs__panel" id="demo-tabs-panel-b" role="tabpanel">`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR HTML 缺少 %s\n%s", want, text)
		}
	}
}

const isoModalSource = `import { h, defineComponent } from "ada:render";
import { Modal } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <Modal
        id="demo-modal"
        title="确认"
        body="确定继续吗？"
        confirmText="继续"
        cancelText="取消"
      />
    );
  },
});
`

func TestIsoModalRenderThroughGo(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-render.js"))
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	iso, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := host.NewRenderer(
		host.WithRenderEngineSource(string(engine)),
		host.WithIsoComponentsSource(string(iso)),
	)
	vnode, err := r.RenderIsland(isoModalSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	html, err := RenderHTML(vnode)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text := string(html)
	for _, want := range []string{
		`<ada-modal class="ada-modal" data-modal="demo-modal" hidden>`,
		`<div class="ada-modal__backdrop" data-modal-close></div>`,
		`<h2 class="ada-modal__title" id="demo-modal-title">确认</h2>`,
		`<div class="ada-modal__body">确定继续吗？</div>`,
		`<button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-modal-close>取消</button>`,
		`<button class="ada-btn ada-btn--primary ada-btn--sm" type="button" data-modal-confirm>继续</button>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR HTML 缺少 %s\n%s", want, text)
		}
	}
}

func TestIsoIconTodoNoticeRenderThroughGo(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-render.js"))
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	iso, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := host.NewRenderer(
		host.WithRenderEngineSource(string(engine)),
		host.WithIsoComponentsSource(string(iso)),
	)
	vnode, err := r.RenderIsland(isoIconsSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	html, err := RenderHTML(vnode)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text := string(html)
	for _, want := range []string{
		`<svg class="ada-icon" width="12" height="12"`,
		`<path d="m3 7 2 2 4-4"/>`,
		`<div class="ada-notice ada-notice--success">`,
		`<div class="ada-todo__list-wrap">`,
		`class="ada-todo__step is-done"`,
		`class="ada-todo__step is-running"`,
		`class="ada-todo__step is-pending"`,
		`备注：</span>备注`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR HTML 缺少 %s\n%s", want, text)
		}
	}
	if strings.Contains(text, "$C") {
		t.Fatalf("SSR HTML 不应包含 $C：%s", text)
	}
}
