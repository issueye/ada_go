package web

// Markdown 动态块（adag-ui fence）的解析与降级。
//
// 只在 Markdown 顶层识别 ```adag-ui fence；其余文本仍走原有
// goldmark + bluemonday + markdownVNodes 管线。块由前端插件桥渲染成
// **校验后的 VNode JSON**（经 VNode block profile 校验；HTML 渲染已随 SSR
// 移除，由客户端 ui-markdown 消费），Markdown 只负责把结果放回原位置。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxMarkdownBlocks / MaxMarkdownBlockBytes / MarkdownBlockRenderTimeoutMS
	// 与 spec/markdown-block.schema.json 的 x-limits 一致（adag verify markdown-block 核对）。
	MaxMarkdownBlocks            = 8
	MaxMarkdownBlockBytes        = 64 << 10
	MarkdownBlockRenderTimeoutMS = 300
	// MaxMarkdownBlocksPerRequest / MarkdownBlockRequestBudgetMS 是**一次请求**的
	// 总量闸门（一次线程页会渲染所有历史消息，单文档上限挡不住长会话叠加）。
	// 超预算的块按普通代码块降级，内容仍可见。
	MaxMarkdownBlocksPerRequest  = 64
	MarkdownBlockRequestBudgetMS = 2000
	defaultBlockFailureText      = "动态内容暂不可用"
)

// BlockRenderer 由前端插件桥可选实现：把 adag-ui block 渲染成校验后的
// VNode JSON（可能是字面量 null，表示该块没有内容可展示）。
type BlockRenderer interface {
	RenderBlock(r *http.Request, rendererID, blockID string, version int, data json.RawMessage) (json.RawMessage, error)
}

var (
	rendererIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}/[a-z][a-z0-9-]{1,63}$`)
	// blockIDRe 与 spec/markdown-block.schema.json 的 id.pattern 一致。
	blockIDRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,63}$`)
)

// blockBudget 是一次请求内所有动态块的共享预算：块数 + 墙钟。
// 线程页会为整份 transcript 渲染块（HTML 与 AST 两条路径各一次），
// 不设总量闸门时，慢渲染器会按消息数线性放大页面耗时。
type blockBudget struct {
	remaining int
	deadline  time.Time
}

func newBlockBudget() *blockBudget {
	return &blockBudget{
		remaining: MaxMarkdownBlocksPerRequest,
		deadline:  time.Now().Add(MarkdownBlockRequestBudgetMS * time.Millisecond),
	}
}

// take 报告还能否再执行一个块；nil 表示不限（测试/单块调用）。
func (b *blockBudget) take() bool {
	if b == nil {
		return true
	}
	if b.remaining <= 0 || time.Now().After(b.deadline) {
		return false
	}
	b.remaining--
	return true
}

// blockScanState 是一份 Markdown 文档内的块状态（数量与 id 唯一性）。
type blockScanState struct {
	count  int
	ids    map[string]bool
	budget *blockBudget
}

func newBlockScanState(budget *blockBudget) *blockScanState {
	return &blockScanState{ids: map[string]bool{}, budget: budget}
}

// markdownBlockSpan 是一个位于顶层、已闭合的 ```adag-ui 块在原文里的位置。
type markdownBlockSpan struct {
	start, end int    // 整段 fence 的范围（开行行首 → 闭行行尾，含换行）
	payload    string // fence 内的内容（不含开闭行）
}

// scanMarkdownBlocks 找出顶层的 ```adag-ui 块。
//
// 按行维护 fence 状态，而不是对全文跑正则：
//   - 只有列 0 的 ```adag-ui 才开动态块（文档承诺的「顶层行首」）；
//   - 处于其他 fenced code block 内时不识别（在 ```markdown 里演示语法不应执行）；
//   - 4 空格缩进属于 indented code block，不识别；
//   - 块引用/列表里的 fence 不以列 0 开头，不识别（MVP 边界）；
//   - 未闭合的 adag-ui fence 不执行，继续交给 goldmark 按普通代码块渲染。
func scanMarkdownBlocks(text string) []markdownBlockSpan {
	const (
		fenceNone = iota
		fencePlain
		fenceAdag
	)
	state := fenceNone
	fenceChar := byte(0)
	fenceLen := 0
	openStart := 0
	payloadStart := 0
	var spans []markdownBlockSpan

	for pos := 0; pos < len(text); {
		lineStart := pos
		var line string
		if nl := strings.IndexByte(text[pos:], '\n'); nl < 0 {
			line = text[lineStart:]
			pos = len(text)
		} else {
			line = text[lineStart : lineStart+nl]
			pos = lineStart + nl + 1
		}
		line = strings.TrimSuffix(line, "\r")

		switch state {
		case fenceNone:
			if n, ok := adagUIOpen(line); ok {
				state, fenceChar, fenceLen = fenceAdag, '`', n
				openStart, payloadStart = lineStart, pos
				continue
			}
			if ch, n, ok := plainFenceOpen(line); ok {
				state, fenceChar, fenceLen = fencePlain, ch, n
			}
		default: // fencePlain / fenceAdag
			if ch, n, ok := fenceClose(line); ok && ch == fenceChar && n >= fenceLen {
				if state == fenceAdag {
					spans = append(spans, markdownBlockSpan{
						start:   openStart,
						end:     pos,
						payload: strings.TrimRight(text[payloadStart:lineStart], "\r\n"),
					})
				}
				state = fenceNone
			}
		}
	}
	return spans
}

// adagUIOpen 识别列 0 的 ```adag-ui 开 fence，返回反引号个数。
func adagUIOpen(line string) (int, bool) {
	n := 0
	for n < len(line) && line[n] == '`' {
		n++
	}
	if n < 3 || n >= len(line) {
		return 0, false
	}
	if strings.TrimRight(line[n:], " \t") != "adag-ui" {
		return 0, false
	}
	return n, true
}

// plainFenceOpen 识别 CommonMark 的 fenced code block 开始（≤3 空格缩进）。
func plainFenceOpen(line string) (byte, int, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(line) {
		return 0, 0, false
	}
	rest := line[indent:]
	ch := rest[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(rest) && rest[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	// 反引号 fence 的 info string 不能含反引号（CommonMark）。
	if ch == '`' && strings.Contains(rest[n:], "`") {
		return 0, 0, false
	}
	return ch, n, true
}

// fenceClose 识别结束 fence：同字符、长度不小于开始、除缩进外没有其他内容。
func fenceClose(line string) (byte, int, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(line) {
		return 0, 0, false
	}
	rest := line[indent:]
	ch := rest[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(rest) && rest[n] == ch {
		n++
	}
	if n < 3 || strings.TrimSpace(rest[n:]) != "" {
		return 0, 0, false
	}
	return ch, n, true
}

type markdownBlockEnvelope struct {
	Renderer string          `json:"renderer"`
	Version  int             `json:"version"`
	ID       string          `json:"id"`
	Data     json.RawMessage `json:"data"`
	Fallback string          `json:"fallback"`
}

func (s *Server) blockRenderer() BlockRenderer {
	if s == nil || s.cfg.PluginUI == nil {
		return nil
	}
	br, ok := s.cfg.PluginUI.(BlockRenderer)
	if !ok {
		return nil
	}
	if gate, ok := br.(interface{ MarkdownBlocksEnabled() bool }); ok && !gate.MarkdownBlocksEnabled() {
		return nil
	}
	return br
}

// markdownNodesJSONWithBlocks 是 markdownNodesJSON 的动态块版本：AST 端点使用。
func (s *Server) markdownNodesJSONWithBlocks(r *http.Request, text string, budget *blockBudget) json.RawMessage {
	br := s.blockRenderer()
	if br == nil {
		return markdownNodesJSON(text)
	}
	spans := scanMarkdownBlocks(text)
	if len(spans) == 0 {
		return markdownNodesJSON(text)
	}
	nodes := make([]markdownVNode, 0, 32)
	last := 0
	state := newBlockScanState(budget)
	for _, sp := range spans {
		nodes = append(nodes, markdownVNodes(text[last:sp.start])...)
		last = sp.end
		// 失败路径（封套失败/预算耗尽/渲染错误）返回的是降级 VNode
		// （代码块或错误卡），直接进 AST；null 表示渲染器显式留空。
		raw, _ := s.renderMarkdownBlockNodes(r, br, sp.payload, state)
		if isNullVNode(raw) {
			continue
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			var node markdownVNode
			if err := json.Unmarshal(raw, &node); err == nil && node.T != "" {
				nodes = append(nodes, node)
				continue
			}
		}
		nodes = append(nodes, blockFallbackVNode(defaultBlockFailureText))
	}
	nodes = append(nodes, markdownVNodes(text[last:])...)
	raw, err := json.Marshal(nodes)
	if err != nil {
		return json.RawMessage("[]")
	}
	return raw
}

// renderMarkdownBlockNodes 渲染一个 adag-ui 块：返回 VNode JSON 与 ok 标记。
// 解析/预算失败时返回按普通代码块降级的 VNode（内容仍可见），ok=false。
func (s *Server) renderMarkdownBlockNodes(r *http.Request, br BlockRenderer, payload string, state *blockScanState) (json.RawMessage, bool) {
	if state == nil {
		state = newBlockScanState(nil)
	}
	state.count++
	if state.count > MaxMarkdownBlocks {
		return blockFallbackNodes("动态块数量超过上限")
	}
	if len(payload) > MaxMarkdownBlockBytes {
		return blockFallbackNodes("动态块数据超过上限")
	}
	env, err := parseBlockEnvelope(payload)
	if err != nil {
		// 封套 Schema 失败：保留为普通代码块，不执行。
		raw, _ := json.Marshal(blockSyntaxFallbackVNode(payload))
		return raw, false
	}
	// id 在同一 Markdown 内唯一：重复时降级当前块，而不是静默复用锚点。
	if env.ID != "" {
		if state.ids[env.ID] {
			return blockFallbackNodes("动态块 id 重复")
		}
		state.ids[env.ID] = true
	}
	if !state.budget.take() {
		// 本次请求的块渲染预算已用完：按普通代码块降级，内容仍然可见。
		raw, _ := json.Marshal(blockSyntaxFallbackVNode(payload))
		return raw, false
	}
	raw, err := br.RenderBlock(r, env.Renderer, env.ID, env.Version, env.Data)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		text := strings.TrimSpace(env.Fallback)
		if text == "" {
			text = defaultBlockFailureText
		}
		return blockFallbackNodes(text)
	}
	return raw, true
}

// parseBlockEnvelope 按 spec/markdown-block.schema.json 校验封套：
// 未知字段、尾随 JSON、renderer/id 形状、data 必填、fallback 长度、version 范围。
// 任何一项失败都按「封套 Schema 失败」处理（保留普通代码块）。
func parseBlockEnvelope(payload string) (markdownBlockEnvelope, error) {
	var env markdownBlockEnvelope
	dec := json.NewDecoder(bytes.NewReader([]byte(payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return env, err
	}
	// 封套必须是单个 JSON 对象：多段拼接会让「看到的」和「执行的」不一致。
	var tail json.RawMessage
	if err := dec.Decode(&tail); err != io.EOF {
		return env, fmt.Errorf("block envelope: 尾随内容")
	}
	env.Renderer = strings.TrimSpace(env.Renderer)
	if !rendererIDRe.MatchString(env.Renderer) {
		return env, fmt.Errorf("block envelope: renderer 形状非法")
	}
	if len(env.Data) == 0 {
		return env, fmt.Errorf("block envelope: 缺少 data")
	}
	if env.Version == 0 {
		env.Version = 1
	}
	if env.Version < 1 {
		return env, fmt.Errorf("block envelope: version 必须 >= 1")
	}
	env.ID = strings.TrimSpace(env.ID)
	if env.ID != "" && !blockIDRe.MatchString(env.ID) {
		return env, fmt.Errorf("block envelope: id 形状非法")
	}
	if utf8.RuneCountInString(env.Fallback) > 512 {
		return env, fmt.Errorf("block envelope: fallback 超过 512 字符")
	}
	return env, nil
}

// isNullVNode 报告渲染器是否显式返回空（JSON null）：空块不是失败。
func isNullVNode(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// blockFallbackNodes 产出降级文案的错误卡 VNode（ok=false）。
func blockFallbackNodes(text string) (json.RawMessage, bool) {
	raw, _ := json.Marshal(blockFallbackVNode(text))
	return raw, false
}

func blockSyntaxFallbackVNode(payload string) markdownVNode {
	return markdownVNode{
		T: "pre",
		C: []markdownVNode{{T: "code", P: map[string]any{"class": "language-adag-ui"}, C: []markdownVNode{{T: "#text", V: payload}}}},
	}
}

func blockFallbackVNode(text string) markdownVNode {
	if strings.TrimSpace(text) == "" {
		text = defaultBlockFailureText
	}
	return markdownVNode{
		T: "div",
		P: map[string]any{"class": "adag-ui-block adag-ui-block--error"},
		C: []markdownVNode{
			{T: "p", P: map[string]any{"class": "adag-ui-block__error"}, C: []markdownVNode{{T: "#text", V: text}}},
		},
	}
}
