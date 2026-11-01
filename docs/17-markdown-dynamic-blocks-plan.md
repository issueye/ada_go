# Markdown 动态块开发计划（adag-ui）

> 上级索引：[ada-go 设计文档](design.md)  
> 方案来源：[16-markdown-dynamic-blocks.md](16-markdown-dynamic-blocks.md)  
> 状态：开发计划（待评审）  
> 版本：0.1 · 2026-11-01  
> 分支：`docs/markdown-dynamic-blocks-plan`

## 1. 范围

本计划只覆盖 **Markdown 内嵌动态块**（`adag-ui` fence）的实现，不改变 Core、engine 与 RPC 协议。

目标交付：

- Markdown 中可用 `adag-ui` fenced block 引用插件渲染器；
- 插件通过 `plugin.json` 的 `frontend.blocks` 声明数据 JSON Schema 与 TSX 渲染器；
- 宿主完成封套校验、数据校验、TSX SSR、VNode 校验与限额；
- 浏览器 `ui-markdown` 与无 JS 的 `/markdown` 两条路径都能渲染；
- 安全边界、失败降级、缓存与门禁完整。

## 2. MVP 定义

MVP 是**只读展示型动态块**，可以独立发布，不依赖交互/流式增强。

### 2.1 MVP 必须包含

| 项 | 内容 |
|---|---|
| 语法 | 仅 `adag-ui` fenced block + JSON 封套 |
| 渲染器 | 插件 `frontend.blocks` + TSX；仅 `profile: display` |
| Schema | 封套 Schema、插件数据 Schema、VNode block profile |
| 渲染路径 | `POST /markdown.ast`（浏览器）与 `POST /markdown`（无 JS） |
| 参考插件 | `token-usage` 提供 `usage-card` 示例块 |
| 安全 | 禁止原始 HTML/JS/内联样式、宿主控制组件；限额/超时/降级 |
| 失败 | 未知 renderer、schema 失败、超时、禁用插件均降级且不影响其余 Markdown |
| 门禁 | unit/golden/e2e + `adag verify` 扩展 + docs |
| 产品开关 | 建议增加 `capabilities.markdownBlocks`，默认 false；`coding` 开启 |

### 2.2 MVP 明确不做

- `profile: interactive` 与块内表单/按钮；
- 块内实时查询（props 只带静态 `data` + app 信息）；
- fence 未闭合时的提前流式渲染；
- 跨进程/持久化渲染缓存；
- 跨插件 renderer 引用策略（v1 先按启用插件解析，后续可收紧）；
- 块目录/可视化编辑器。

### 2.3 当前实现状态（2026-11-01）

- MB-0 契约：`markdown-block.schema.json`、`frontend.blocks`、VNode block profile、产品开关 `capabilities.markdownBlocks` 已落地；
- MB-1 清单/加载：`frontend.blocks` 解析、模板/Schema 读取、renderer 注册已完成；
- MB-2 数据 Schema：受控 2020-12 子集校验器已落地（未知关键字编译期报错）；
- MB-3 Markdown：顶层 `adag-ui` fence 分段、降级、块数/字节限额已完成；
- MB-4 Render Runtime/VNode：block TSX、display profile、超时/错误隔离已完成；
- MB-5 浏览器/无 JS：`markdownVNode` 支持 `$C`/`n`；`/markdown.ast`、`/markdown` 与无 JS 模板已接入；
- MB-6 参考插件：`token-usage/usage-card` 已落地；
- MB-7 安全/限额/缓存：display profile、限额、进程内 bounded cache 已完成；审计事件（`ui.block.invalid_data` / `render_failed` / `invalid_vnode`，只带错误码不带 data）已接入；interactive profile 在运行期 fail-closed（`unsupported_profile`）；
- MB-8 测试/门禁：Bridge/Markdown 降级测试、审计事件测试、`verify vnode` block profile、`verify markdown-block`、token-usage golden HTML、`/markdown.ast` golden（`web/testdata/markdown-block/usage-card.ast.json`）、`$C` 组件渲染测试、AST→HTML 与 `/markdown` 一致、浏览器运行时结构一致（`ada-block-consistency.test.js`）、助手消息内块 e2e（含禁用插件降级与旧缓存不生效）、限额/超时测试均已通过；第三方前端 block 加载与真实浏览器冒烟仍待补。

**未做**：interactive 块、流式提前渲染、跨进程缓存、第三方前端插件加载（当前 Gateway 只加载官方前端脚本）、真实浏览器冒烟（`tools/render-demo` 手工验证；自动化用 Node 运行时替代）。

## 3. 工作分解与依赖

```text
MB-0 契约
  ├── MB-1 manifest/loader
  │     ├── MB-3 Markdown 解析/占位
  │     └── MB-4 Render Runtime/校验
  ├── MB-2 Schema 校验器
  │     ├── MB-3
  │     └── MB-4
  ├── MB-5 浏览器/无 JS
  └── MB-6 参考插件
        └── MB-7 安全/限额/缓存
              └── MB-8 测试/门禁/文档
```

### MB-0 契约与 Schema（1–2 人日）

产物：

- `spec/markdown-block.schema.json`；
- `spec/manifest.schema.json` 增加 `frontend.blocks`；
- `spec/vnode.schema.json` 增加 block profile（或独立 `block-vnode.schema.json` 引用公共 `$defs`）；
- 产品 Schema 增加 `capabilities.markdownBlocks`（建议）；
- 同步 `docs/16` 与 `docs/07/14/15` 的交叉引用。

验收：

- Schema 可解析，字段都有消费者；
- `adag verify manifest`、`vnode`、`products` 通过；
- 示例 fixture 可被门禁读取。

### MB-1 清单与插件加载（2–3 人日）

产物：

- `internal/plugins` manifest 结构解析 `frontend.blocks`；
- 插件加载时校验：
  - `name` 唯一、`schema`/`template` 路径存在且无 `..`；
  - `profile` 与 trust 匹配；
  - Schema JSON 合法、模板可编译；
- block 注册表：`rendererID = pluginID/name`；
- 非法 block 只禁用该 renderer，插件其余能力仍加载并记 warning。

验收：

- 缺 Schema/模板/重名/非法 profile 的插件被门禁拦截；
- 运行期禁用块不拖垮插件；
- 官方/第三方走同一条加载路径。

### MB-2 JSON Schema 校验器（2–3 人日）

产物：

- 选定并钉定一个纯 Go JSON Schema 2020-12 实现（或与 `tools/verify` 共用的受控子集）；
- 编译结果按 `(plugin, block, version)` 缓存；
- 封套校验 + 插件 `data` 校验；
- 限额：单块 64KB、深度 32、文档 8 块；
- 错误只包含路径与原因，不回显 data 值。

验收：

- 合法/非法 fixture 覆盖；
- 未知关键字在加载期报错而不是静默忽略；
- 校验器不依赖网络，可在离线构建中使用。

### MB-3 Markdown 解析与占位（3–4 人日）

产物：

- goldmark fenced code 扩展：识别 `adag-ui`；
- 解析 JSON 封套，失败时保留代码块；
- 解析 renderer、校验 data、调用 MB-4；
- 生成唯一 token 占位元素 `class="adag-ui-block adag-ui-block-<token>"`；
- `markdownVNodes` 支持 token → VNode 替换；
- 未闭合 fence、非法 JSON、未知 renderer 的降级路径。

验收：

- golden AST/HTML：正常块、非法 JSON、未知 renderer、超限；
- 占位不破坏其余 Markdown 的 goldmark/bluemonday 行为；
- 单文档多块顺序稳定。

### MB-4 Render Runtime 与 VNode 校验（3–4 人日）

产物：

- block 渲染入口，复用现有 `internal/gateway/frontend` Render Runtime；
- `BlockProps`：插件身份、block data、静态 app 信息；
- 300ms 超时、并发上限、失败隔离；
- VNode 校验：`vnode.schema.json` + block profile + 深度/节点/字符串/总输出限额；
- 包装 `div.adag-ui-block[data-adag-renderer]`；
- 错误降级为 `Notice`/`EmptyState`/`fallback`。

验收：

- 合法 TSX → 合法 VNode；
- 越权组件/标签/属性被拒绝；
- 抛错/超时/非法 VNode 只降级当前块；
- 渲染器不能访问 Node/fs/fetch/密钥。

### MB-5 浏览器与无 JS（2–3 人日）

产物：

- `markdownVNode` 增加 `n` 字段与 `$C` 节点输出；
- `ui-markdown` 确认/补齐 `$C` 节点透传；
- `POST /markdown.ast` 返回含块节点的 AST；
- `POST /markdown` 复用同一块渲染结果，输出 VNode→HTML；
- 样式：`.adag-ui-block` 容器、最小间距与错误态样式。

验收：

- 浏览器 `ui-markdown` 正常渲染 `$C`/HTML 块；
- 无 JS 的 `/markdown` 与浏览器内容一致；
- 两条路径共用缓存，不重复执行 TSX。

### MB-6 参考插件（2 人日）

产物：

- `token-usage` 的 `usage-card`：
  - `schemas/usage-card.json`；
  - `blocks/usage-card.tsx`；
  - `plugin.json` 的 `frontend.blocks`；
- 产品声明开启 `markdownBlocks`；
- 一条示例 Markdown + golden 输出。

验收：

- 示例消息在浏览器与 `/markdown` 都显示卡片；
- 数据不符合 Schema 时降级；
- 禁用 `token-usage` 后块降级且缓存失效。

### MB-7 安全、限额与缓存（2–3 人日）

产物：

- 每文档 8 块、单块 64KB/300ms/2000 节点/256KB 输出；
- 单文档最多 8 块 × 300ms；MVP 串行渲染（不并发执行），总耗时上限约 2.4s；
- block profile 拒绝 `Composer`/`InteractionTray`/`Slot`/`Form`/`Button`/`Input`/`Select`/`Textarea`；
- URL/属性/事件校验；
- 进程内 LRU 缓存：`(plugin, version, renderer, schemaVersion, dataHash, theme?, locale?)`；
- 插件 reload/enable/disable 时失效；
- 审计事件：`ui.block.render_failed` / `invalid_data` / `invalid_vnode`（不记 data）。

验收：

- XSS/越权/递归/超限 fixture 全部拒绝；
- 缓存命中/失效测试；
- 禁用插件后无残留渲染。

### MB-8 测试、门禁与文档（3–4 人日）

产物：

- unit：封套、插件 Schema、限额、profile；
- golden：`/markdown.ast` 与 `/markdown`；
- e2e：插件块出现在助手消息，禁用插件降级；
- `adag verify` 新增检查：
  - `manifest`：`frontend.blocks` 字段与消费者；
  - `vnode`：block profile；
  - 新增 `markdown-block`：封套 Schema 与 fixture；
- `docs/16` 与 `docs/07/14/15` 更新；
- 安全审查清单。

验收：

- `go build ./...`、`go vet ./...`、`go test ./...`、staticcheck、`adag verify` 全绿；
- `node plugins/official/build.mjs --check` 通过；
- MVP 验收清单全部满足。

## 4. 里程碑与工作量

| 阶段 | 内容 | 估算（人日） |
|---|---|---|
| MVP | MB-0…MB-8 | 20–28 |
| P1 | interactive profile、动作桥/CSRF、来源标识、块目录/调试 | 8–12 |
| P2 | 流式提前渲染、缓存指标、持久化缓存 | 5–8 |

估算按 1 名熟悉 Go/TS/moejs 的工程师计，包含测试与文档，不是承诺日期。MB-2/MB-3/MB-4/MB-5 可在契约冻结后并行。

## 5. MVP 验收清单

- [x] `adag-ui` fence 可被解析，非法内容保留为代码块（`TestMarkdownDynamicBlockFallbacks`）；
- [x] 插件 `frontend.blocks` 加载、校验、注册（`TestManifestBlockValidation`、`TestManifestInteractiveBlockRequiresTrust`、Bridge 注册表）；
- [x] 数据 Schema 校验通过/失败路径正确（`TestBridgeRenderBlock`、`TestBridgeBlockAudit`）；
- [x] TSX → VNode → HTML/AST 全链路可用（`TestBridgeRenderBlock`、`TestMarkdownTokenUsageBlockIntegration`）；
- [x] 浏览器与 `/markdown` 无 JS 结果一致：Go 侧 `AST → RenderHTML` 与 `/markdown` 逐字一致；`ada-render.js`（浏览器运行时）挂载同一份 AST 与 golden HTML 结构一致（`ada-block-consistency.test.js`）；
- [x] display profile 拒绝宿主控制组件与表单（`TestValidateBlockNodeProfile`、`TestBridgeBlockAudit` 的 profile_violation）；
- [x] 单块/单文档限额、超时、降级生效，块按文档顺序串行渲染（无并发执行）：`TestMarkdownBlockLimits`、`TestBridgeBlockRenderTimeout`；
- [x] 未知/禁用 renderer 降级且不影响其余 Markdown（`TestMarkdownDynamicBlockFallbacks`、`TestAssistantMessageDynamicBlockE2E`）；
- [x] 缓存命中与插件变更失效（`TestBlockCacheStoreAndKey`、`TestBlockCacheLimitResets`；禁用插件时先查启停、不吃旧缓存）；
- [x] token-usage 示例块通过 golden（HTML golden 与 `/markdown.ast` golden）；
- [x] `adag verify` 新增检查通过（`manifest`/`vnode`/`markdown-block`，共 13 项）；
- [x] 文档与 Schema 无漂移（`adag verify docs/manifest/vnode/markdown-block`）。

## 6. 测试矩阵

| 层 | 用例 |
|---|---|
| Schema | 合法/非法封套、额外字段、缺 renderer、data 类型错误、未知 version |
| 数据 | 插件 Schema 通过/失败、嵌套深度、超长字符串、超大数组 |
| Markdown | 正常块、多块、未闭合 fence、非法 JSON、代码块转义 |
| 渲染 | 合法 VNode、越权组件/标签/属性、事件/style/原始 HTML、超时、抛错 |
| 安全 | `javascript:`/`data:` URL、`on*`、`style`、`Slot`/`InteractionTray`、递归块 |
| 路径 | `/markdown.ast` 与 `/markdown` 一致性、无 JS |
| 生命周期 | 插件禁用/重载/版本变化、缓存失效、降级 |
| 性能 | 8 块上限、单块超时、串行渲染、重复请求缓存 |

## 7. 风险与对策

| 风险 | 对策 |
|---|---|
| JSON Schema 实现与插件作者预期不一致 | 钉定版本；在文档列出支持的 draft/keywords；加载期拒绝未知关键字 |
| goldmark 占位元素被 bluemonday 清洗掉 | 用现有 class 白名单；MB-3 先做尖刺，失败则改为 sanitizer 后 AST 级注入 |
| `ui-markdown` 不支持 `$C` 节点 | MB-5 最早验证；必要时只透传组件节点，不改整体 VNode 协议 |
| block profile 与现有 VNode Schema 冲突 | profile 作为附加约束，不修改 pages/slots 的既有允许集 |
| 模型生成恶意 data | Schema + block profile + 限额；不把 data 当指令；错误不回显 data |
| 重复 `/markdown.ast` 导致重复渲染 | 内容 hash 去重 + 缓存；流式只处理闭合 fence |
| 插件 Schema 文件缺失/非法 | 加载期禁用该 block，插件其余能力保留；`adag verify` 阻断打包 |
| 性能退化 | 单文档 8 块、300ms/块、串行渲染（上限约 2.4s）；并发与总预算留到 P2 |

## 8. 文件变更地图

| 区域 | 文件 |
|---|---|
| 契约 | `spec/markdown-block.schema.json`、`spec/manifest.schema.json`、`spec/vnode.schema.json` |
| 产品 | `spec/product.schema.json`（`capabilities.markdownBlocks`，建议） |
| 插件清单 | `internal/plugins/manifest.go`、discovery/loader 相关 |
| 前端渲染 | `internal/gateway/frontend/*`（block registry/Render Runtime） |
| Markdown | `internal/gateway/web/markdown_vnode.go`、新增 `markdown_block.go`、`ui.go` |
| 浏览器 | `internal/gateway/web/static/ada-ui.js`（Markdown `$C` 透传）、CSS |
| 官方插件 | `plugins/official/token-usage/{plugin.json,schemas,blocks}` |
| 门禁 | `tools/verify/*`、`spec/*` fixture |
| 文档 | `docs/16-markdown-dynamic-blocks.md`、`docs/07/14/15` |

## 9. 开放决策

1. `capabilities.markdownBlocks` 产品开关：**已采用**（默认 false，coding/headless true，skeleton false）。
2. JSON Schema 运行时实现：**已采用**受控 2020-12 子集（未知关键字编译期报错）。
3. 是否允许引用其他插件的 renderer（MVP 建议允许启用插件，但数据 Schema 由目标插件负责）。
4. 块上插件来源标识：**已采用** `data-adag-renderer` 包装属性，UI 可选展示。
5. 交互块的信任门槛（P1 决策，建议仅 builtin/verified + 产品显式开启）。

## 10. 相关文档

- [Markdown 动态块方案](16-markdown-dynamic-blocks.md)
- [SSR 前端](07-ssr-frontend.md)
- [VNode 渲染引擎](14-render-engine.md)
- [UI 组件层](15-ui-components.md)
- [插件模型](05-plugins.md)
