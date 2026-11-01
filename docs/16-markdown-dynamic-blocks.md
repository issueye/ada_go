# Markdown 动态块渲染（adag-ui）

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：扩展（复用 7 SSR、7.3 VNode、7.5 前端插件、14 Render Engine、15 UI Components）  
> 状态：设计提案（待评审）  
> 版本：0.1 · 2026-11-01

## 1. 目标与非目标

### 1.1 目标

在 Markdown 正文中嵌入由插件定义、由宿主校验并渲染的动态 UI 块：

````markdown
```adag-ui
{"renderer":"token-usage/usage-card","data":{"scope":"thread","title":"本轮用量"}}
```
````

要求：

- **同一套 Schema 技术体系**：JSON Schema 2020-12 + 现有 `spec/vnode.schema.json`；
- 插件自己声明数据 Schema 与 TSX 渲染器，宿主只负责解析、校验、限额与渲染；
- 复用现有 goldmark + bluemonday + VNode + Render Runtime，不引入 MDX、客户端模板执行或第二套渲染引擎；
- 产品开关：`capabilities.markdownBlocks` 是总开关；false 时 `adag-ui` fence 按普通代码块降级；
- 模型/工具输出只是数据，不能执行脚本、注入原始 HTML 或伪造宿主 UI；
- 无 JS 时仍能通过 `/markdown` HTML 端点看到块内容。

### 1.2 非目标

- 不允许模型输出任意 HTML/JS/MDX；
- 不允许客户端解析/执行 Schema；
- 不替代现有 `frontend.pages`（独立页面）与 `frontend.slots`（固定插槽）；
- v1 不开放动态块内的任意交互表单（见 §10 `profile`）。

## 2. 现状与复用点

| 现状 | 位置 | 本方案如何复用 |
|---|---|---|
| Markdown 解析/清洗 | `internal/gateway/web/ui.go` 的 `renderMarkdown`（goldmark + bluemonday） | 只增加 fenced block 处理，不改清洗边界 |
| Markdown → VNode | `internal/gateway/web/markdown_vnode.go` | 增加 fence 分段与块 VNode 合并、`$C` 组件节点 |
| 浏览器 Markdown 渲染 | `internal/gateway/web/static/ada-ui.js` 的 `Markdown` 组件 | 继续接收 VNode AST；补 `$C` 节点透传 |
| VNode 契约 | `spec/vnode.schema.json` | 复用元素/组件/文本节点、限额与校验器 |
| 前端插件 | `plugin.json` 的 `frontend`、`internal/gateway/frontend` Render Runtime | 新增 `frontend.blocks`，复用同一 TSX→VNode 运行时 |
| 插件清单 | `spec/manifest.schema.json` | 增加 `frontend.blocks` 字段与消费者 |

VNode 就是统一的 **UI Schema**；插件数据 Schema 只负责 `data` 的形状；TSX 只负责 `data → VNode` 的映射。

## 3. 总体架构

```text
Markdown
  → 扫描顶层 ```adag-ui fence，切成文本段 / 块段
  → 文本段：goldmark → bluemonday → Markdown VNode
  → 块段：校验封套 Schema → 按 renderer id 找插件 block
  → 用插件数据 Schema 校验 data → Render Runtime 执行 TSX
  → VNode Schema + block profile + 限额校验
  → 按原顺序合并文本 VNode 与块 VNode
  → ui-markdown（浏览器）/ VNode→HTML（无 JS/HTML 端点）
```

职责边界：

- **基座/Gateway**：识别 fence、分段、校验封套、解析 renderer、执行限额与 VNode 校验、合并/降级；
- **插件**：声明 `frontend.blocks`、提供数据 Schema 与 TSX 渲染器；
- **模型/工具**：只产生 Markdown 文本与块 `data`，不执行代码。

## 4. 三层 Schema

| 层 | Schema | 负责 |
|---|---|---|
| 封套 | `spec/markdown-block.schema.json`（新增） | `renderer`、`data`、`id`、`version`、`fallback` |
| 数据 | `plugins/<id>/schemas/<name>.json`（插件自带） | 该插件自定义的 `data` 格式 |
| 输出 | `spec/vnode.schema.json`（已有） | 渲染器产出的 VNode 标签/组件/属性/限额 |

### 4.1 封套 Schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://github.com/adag/ada-go/spec/markdown-block.schema.json",
  "title": "Markdown 动态块封套",
  "type": "object",
  "additionalProperties": false,
  "required": ["renderer", "data"],
  "properties": {
    "renderer": {
      "type": "string",
      "pattern": "^[a-z][a-z0-9-]{1,63}/[a-z][a-z0-9-]{1,63}$",
      "description": "插件 id / block name"
    },
    "version": {
      "type": "integer",
      "minimum": 1,
      "default": 1,
      "description": "插件 block Schema 的版本"
    },
    "id": {
      "type": "string",
      "pattern": "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$",
      "description": "可选的块锚点；同一 Markdown 内唯一"
    },
    "data": {
      "description": "插件自定义数据；由插件自带 Schema 校验",
      "type": ["object", "array", "string", "number", "boolean", "null"]
    },
    "fallback": {
      "type": "string",
      "maxLength": 512,
      "description": "渲染失败时的纯文本提示；始终由宿主转义"
    }
  },
  "x-limits": {
    "maxBytes": 65536,
    "maxDepth": 32,
    "maxBlocksPerDocument": 8,
    "renderTimeoutMs": 300
  }
}
```

要点：

- `additionalProperties: false`，未知字段拒绝；
- `data` 允许任意 JSON 类型，但总字节、深度与块数量由宿主强制（JSON Schema 无法表达树深度/总字节）；
- `renderer` 使用 `插件 id/块名`，与 `plugin.json` 的声明一一对应；
- `fallback` 只能作为纯文本降级提示，不参与 HTML 拼接。

## 5. Markdown 语法

使用 fenced code block，info string 固定为 `adag-ui`：

````markdown
```adag-ui
{"renderer":"token-usage/usage-card","data":{"scope":"thread","title":"本轮用量"}}
```
````

约定：

- fence 内容必须是单个 JSON 对象；
- 不支持 YAML/MDX/多段拼接；
- 未识别或解析失败时，**保留为普通代码块**（转义显示），不执行；
- 流式过程中 fence 未闭合时按普通代码/占位显示，不调用渲染器；
- 同一 Markdown 内块数量、总字节由 `x-limits` 限制。

选择 fenced block 的原因：

- 在不支持动态块的渲染器中自然降级为代码块；
- 不引入原始 HTML/注释魔法；
- 内容是可校验的 JSON，而不是可执行文本。

## 6. 插件声明

扩展 `plugin.json` 的 `frontend` 段：
```json
{
  "frontend": {
    "blocks": [
      {
        "name": "usage-card",
        "title": "Token 用量",
        "description": "按线程或工作区展示 token 用量",
        "schema": "schemas/usage-card.json",
        "template": "blocks/usage-card.tsx",
        "profile": "display"
      }
    ]
  }
}
```

> 插件作者与 AI 的操作说明（如何声明、如何在回答里写 fence）见
> `plugins/official/plugin-helper/SPEC.md` §8.1 / §8.2。

字段：

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | 是 | `^[a-z][a-z0-9-]{1,63}$`，插件内唯一 |
| `title` | 否 | 人类可读标题，用于错误/调试展示 |
| `description` | 否 | 用途说明 |
| `schema` | 是 | 相对插件目录的 JSON Schema 路径，禁止 `..` |
| `template` | 是 | 相对插件目录的 TSX 渲染器路径 |
| `profile` | 否 | `display`（默认）或 `interactive` |

加载规则：

1. 读取并编译数据 Schema；非法则**该 block 不可用**（插件其余能力仍可加载），记警告；
2. 编译 TSX 模板；失败同上；
3. 注册 `rendererID = <plugin.id>/<name>`；同名冲突装配失败；
4. `profile=interactive` 仅 builtin/verified 可声明，且产品必须显式开启；
5. `adag verify manifest` 校验文件存在、JSON 合法、`name` 唯一、`profile` 与信任级别匹配。

装配范围：随包官方插件加载完整前端段；全局/工作区安装的插件只加载
`frontend.blocks`（`frontend.LoadBlockScript` 会剥掉页面/插槽/样式/脚本），
工作区优先、官方同 id 遮蔽磁盘版本；单个插件的 block 非法只跳过该插件并记日志。
MVP 运行期只渲染 `profile: display`，`interactive` 即使在清单里声明也 fail-closed。

## 7. 插件数据 Schema

示例 `plugins/official/token-usage/schemas/usage-card.json`：

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["scope"],
  "properties": {
    "scope": { "enum": ["thread", "workspace"] },
    "title": { "type": "string", "maxLength": 80 },
    "highlight": { "enum": ["input", "output", "cache"] }
  }
}
```

宿主只做标准 JSON Schema 校验，不解释字段语义。建议使用固定版本的纯 Go JSON Schema 2020-12 实现（或与 `tools/verify` 共用的受控子集），编译结果按插件版本缓存。

## 8. TSX 渲染器

示例 `plugins/official/token-usage/blocks/usage-card.tsx`：

```tsx
type BlockProps = {
  plugin: { id: string; name: string; version: string };
  block: {
    id?: string;
    renderer: string;
    version: number;
    data: { scope: "thread" | "workspace"; title?: string; highlight?: string };
  };
  app: { workspace: string; product: string; route: string; theme: "light" | "dark" };
};

export default function UsageCard(props: BlockProps) {
  const { data } = props.block;
  return (
    <Panel title={data.title || "Token 用量"}>
      <Notice tone="neutral">
        统计范围：{data.scope === "thread" ? "当前线程" : "当前工作区"}
      </Notice>
    </Panel>
  );
}
```

约束：

- `props` 只包含块数据、静态 `app` 信息与插件身份；
- 不注入线程实时状态、文件、密钥、网络或任意查询；
- 需要实时数据的场景使用插件页面/动作桥，不在 Markdown 块里做查询；
- 渲染器必须是纯函数式 SSR：无状态、无副作用、无 Node API；
- 输出只能是 VNode，不能再包含 `adag-ui` 块。

## 9. 服务端渲染管线

### 9.1 解析与占位（MVP）

MVP 在 Markdown **顶层**按 ` ```adag-ui ` fence 分段，不修改 goldmark AST：

1. 扫描顶层 fence，得到「文本段 / 块段」序列；
2. 每个文本段继续走现有 `renderMarkdown` → bluemonday → `markdownVNodes`；
3. 每个块段解析封套、校验 `data`、调用 Render Runtime 得到块 VNode；
4. 按原顺序拼接文本段与块 VNode（HTML 端点拼接 `renderMarkdown` 输出与块 HTML）；
5. 失败时只替换该块为降级节点，不影响其余文本。

边界：fence 需位于顶层行首；列表/引用块内的嵌套 fence 不在 MVP 范围。后续可迁移为 goldmark AST 占位元素，以支持嵌套结构。

这样：

- Markdown 其余部分完全复用现有解析/清洗；
- 动态块不经过 HTML 字符串，不引入二次解析；
- 失败时只影响该块，不影响整篇 Markdown；
- 不需要改动 goldmark/bluemonday 的核心行为。

### 9.2 渲染与 VNode 校验

Render Runtime 调用成功后，宿主必须执行：

- `spec/vnode.schema.json` 校验；
- block profile 校验（§10）；
- 运行期限额：深度 ≤64、节点 ≤2000、单字符串 ≤32KB、输出 ≤256KB；
- URL 归一化后拒绝 `javascript:` / `data:` / `vbscript:`；
- 包装为：

```json
{
  "t": "div",
  "p": { "class": "adag-ui-block", "data-adag-renderer": "token-usage/usage-card" },
  "c": [ ...插件 VNode... ]
}
```

### 9.3 浏览器与无 JS

- `POST /markdown.ast`：返回 VNode 中动态块是标准 `$C`/HTML 节点；
- 现有 `ui-markdown` 把 nodes 交给 `h()`；`ada-render` 已支持 `$C` 节点；
- `internal/gateway/web/markdown_vnode.go` 目前只产出元素/文本节点，需要补 `n` 字段，并在转换器中生成 `$C`；
- `POST /markdown`（HTML 端点）复用同一份块渲染结果，再用 VNode→HTML 输出，保证无 JS 页面也能看到块；
- 两条路径必须共用同一份缓存/渲染结果，避免行为分叉。

## 10. VNode block profile（安全关键）

Markdown 可能由模型生成，不能等同于插件页面/插槽。动态块使用更窄的 **display profile**：

### 10.1 允许

- 展示型标签：`div`、`span`、`p`、`br`、`hr`、`h1`–`h6`、`ul`、`ol`、`li`、`strong`、`em`、`code`、`pre`、`small`、`blockquote`、`a`、`section`、`table`、`thead`、`tbody`、`tr`、`th`、`td`、`caption`、`details`、`summary`
- 展示型组件：`Icon`、`TodoList`（当前 Go 渲染器已实现的面；`Notice`/`Panel` 等后续扩展）
- `aria-*`、`data-*`、class

### 10.2 禁止

- `Composer`、`InteractionTray`、`Slot`、`Form`、`Button`、`Input`、`Select`、`Textarea`
- `form`、`input`、`button`、`select`、`textarea` 标签
- `on*` 事件属性、`style`、`dangerouslySetInnerHTML`
- 内联脚本/样式、`javascript:` / `data:` URL

### 10.3 理由

- 防止模型生成的 Markdown 伪造审批托盘、表单或系统操作；
- 防止块冒充宿主 UI 骗取点击；
- 交互仍走插件页面/动作桥（CSRF + 确认）；
- 确需交互时声明 `profile: interactive`，仅 builtin/verified，且产品显式开启；
- MVP 运行期只渲染 `profile: display`，其余 profile 一律 fail-closed（`render_failed` / `unsupported_profile`），避免清单声明交互却按展示块画出可提交表单。

## 11. 限额、缓存与流式

### 11.1 限额

| 项 | 上限 |
|---|---|
| 单块封套 | 64KB |
| 单块 `data` 深度 | 32 |
| 单文档块数 | 8 |
| 单块渲染超时 | 300ms |
| 单块 VNode 节点 | 2000 |
| 单块 VNode 输出 | 256KB |
| 单文档块渲染预算 | MVP 串行渲染，最长约 8 × 300ms；总预算与并发留到 P2 |

### 11.2 缓存

- Key：`(pluginID, pluginVersion, renderer, version, sha256(canonicalJSON(data)), theme?, locale?)`；
- Value：校验后的 VNode JSON（或 HTML 片段）；
- 存储：Gateway 进程内 bounded cache（当前 512 项，满时清空；后续可换 LRU）；
- 失效：插件 reload / enable / disable / 版本变化；
- 不缓存含实时投影的块；v1 的 block props 只放静态数据，保证可缓存。

### 11.3 流式

- 未闭合的 `adag-ui` fence 不执行，按普通代码/占位显示；
- fence 闭合后（或最终 AST 请求）才渲染；
- `/markdown.ast` 可能被多次调用：按内容 hash 去重，避免同一块重复执行 TSX；
- 同一条消息内块按文档顺序**串行**渲染，避免并发压垮单进程渲染器；
- 并发 2–4 与文档级总预算留到 P2，引入时需先加并发上限与总超时。

## 12. 失败与降级

| 情况 | 行为 |
|---|---|
| fence JSON 非法 | 保留为普通代码块（转义显示），不执行 |
| 封套 Schema 失败 | 同上 |
| renderer 不存在/插件禁用 | 渲染 `Notice`/`EmptyState` 降级，或按 `fallback` 文本；记录 `ui.block.render_failed`（`renderer_missing`/`plugin_disabled`） |
| 数据 Schema 校验失败 | 降级块，记录 `ui.block.invalid_data` |
| `profile` 非 `display`（MVP） | 降级块，记录 `ui.block.render_failed`（`unsupported_profile`） |
| TSX 抛错/超时 | 降级块，记录 `ui.block.render_failed`（生产不展示堆栈） |
| VNode 校验失败 | 降级块，记录 `ui.block.invalid_vnode` |
| 未知 `version` | 降级块，提示“插件版本不兼容” |
| 其他 Markdown 渲染器 | `adag-ui` 作为普通代码块显示，不执行 |

降级文本始终由宿主转义；`data` 不进入错误消息，避免泄露。

审计事件由 Bridge 的 `BlockAudit` 钩子输出，字段固定为
`kind`（`ui.block.invalid_data` / `ui.block.render_failed` / `ui.block.invalid_vnode`）、
`plugin`、`renderer`、`block id`、`error`（仅错误码，如 `invalid_json`、
`schema_validation`、`renderer_missing`、`plugin_disabled`、`props_error`、
`render_error`、`profile_violation`）。
**`data` 与其派生内容永远不进入事件**，错误码也不携带渲染器返回的原始消息。


## 13. 改动清单

| 位置 | 改动 |
|---|---|
| `spec/markdown-block.schema.json` | 新增封套 Schema |
| `spec/manifest.schema.json` | `frontend.blocks` 数组与字段消费者 |
| `spec/vnode.schema.json` | 增加 block profile（或独立 `block-vnode.schema.json` 引用公共 `$defs`） |
| `internal/plugins` manifest | 解析 `frontend.blocks`，加载 schema/template 路径 |
| `internal/gateway/frontend` | block 注册表 + Render Runtime 调用 |
| `internal/gateway/web` | fence 分段、块 VNode 合并、`markdownVNode` 支持 `$C`/`n` |
| `internal/gateway/web/static/ada-ui.js` | 确认/补齐 `Markdown` 组件对 `$C` 节点的透传 |
| `tools/verify` | manifest 字段、Schema 文件存在、block VNode profile、fixture |
| `docs/07`、`docs/14`、`docs/15` | 语法、Schema、限额、安全模型交叉引用 |

Core、engine、协议不变；这是纯 Gateway + 插件前端能力。

## 14. 完整示例

Markdown：

````markdown
## 本轮用量

```adag-ui
{"renderer":"token-usage/usage-card","data":{"scope":"thread","title":"本轮用量"}}
```
````

插件声明：

```json
{
  "id": "token-usage",
  "frontend": {
    "blocks": [{
      "name": "usage-card",
      "schema": "schemas/usage-card.json",
      "template": "blocks/usage-card.tsx",
      "profile": "display"
    }]
  }
}
```

宿主输出（VNode）：

```json
{
  "t": "div",
  "p": { "class": "adag-ui-block", "data-adag-renderer": "token-usage/usage-card" },
  "c": [
    {
      "t": "section",
      "p": { "class": "card tu-card" },
      "c": [
        { "t": "span", "p": { "class": "tu-card__title" }, "c": [{ "t": "#text", "v": "本轮用量" }] },
        { "t": "span", "p": { "class": "tu-card__total" }, "c": [{ "t": "#text", "v": "1,801" }] }
      ]
    }
  ]
}
```

## 15. 落地顺序

### MVP

1. `markdown-block.schema.json` + `frontend.blocks` manifest；
2. `adag-ui` fence 解析、封套校验；
3. display-only block profile；
4. 一个官方插件（建议 `token-usage`）提供 `usage-card`；
5. `ui-markdown` 的 `$C` 透传 + `/markdown` 无 JS 回退；
6. 限额、超时、降级卡、缓存；
7. `adag verify` 与 golden fixture。

### 第二阶段

- interactive profile（仅 builtin/verified）+ 动作桥/CSRF；
- 块目录/调试页面；
- 跨消息缓存与指标。

> 块渲染审计（`ui.block.invalid_data` / `render_failed` / `invalid_vnode`）已随 MVP 落地。

### 第三阶段

- 流式块（fence 闭合即提前渲染）；
- 主题/语言感知缓存；
- 服务重启后的持久化渲染缓存（可选）。

## 16. 待确认决策

1. fence 名固定为 `adag-ui`，是否还需要别名？
2. `frontend.blocks` 是否允许引用其他插件的 renderer？（建议 v1 允许引用启用插件，但数据 Schema 由目标插件负责。）
3. v1 是否只开放 `display` profile？（建议是。）
4. 是否在块上显示插件来源标识（防冒充）？
5. JSON Schema 运行时校验库的选择与版本钉定。

## 17. 相关文档

- [SSR 前端](07-ssr-frontend.md)
- [单文件 VNode 渲染引擎](14-render-engine.md)
- [UI 组件层](15-ui-components.md)
- [插件模型与后端 API](05-plugins.md)
