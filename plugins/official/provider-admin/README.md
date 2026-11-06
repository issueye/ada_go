# 供应商管理（provider-admin）

模型设置页：集中管理供应商（端点、协议、API Key、模型清单）与当前模型。页面经本插件自己的 route 读写数据，API Key 不经过浏览器。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `provider-admin` |
| 名称 | 供应商管理 |
| 版本 | 0.4.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 前后端同包 |
| 后端能力 | `model.admin` |
| 前端能力 | `frontend.script` |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 插槽 | `composer.left`（`slot.tsx`，order 10）：模型选择器旁的「模型设置」入口 |
| 导航 / 页面 | 导航「供应商」→ 页面 `/ext/provider-admin/providers`（`providers-next.tsx`，SSR + hydrate） |
| 样式 | `provider-admin.css`（`static/`） |
| 浏览器 bundle | `static/client.js`（`pluginbuild` 从 `client.tsx` 生成） |

P2-4b-2b：`providers` 页已替换为组件化实现，支持新增供应商、设为当前、启用/停用、删除、拉取/添加模型、模型新增/删除/编辑与供应商信息编辑。拉取模型会打开勾选弹窗，只添加选中的模型；模型的添加与编辑也通过弹窗完成；模型列表每行直接提供「删除」按钮（二次确认后保存），不必先打开编辑弹窗。模型推理等级是动态标签，输入模态是复选框。legacy `provider-admin.js` / `page.tsx` 保留在仓库作历史参考，但清单不再加载脚本。页面通过 `backend.read` 动作 `state` 预取 `/state`，SSR/无 JS 也能看到供应商目录；关闭 `pluginIslands` 或设 `ADAG_DISABLE_ISLANDS=1` 时只保留 SSR。禁用插件时入口、页面与 bundle 一起消失。

P2-4b-3（0.4.0）：按设计稿重排页面结构与控件形态。

| 区域 | 现状 |
| --- | --- |
| 左侧目录 | 搜索（名称/ID/协议/端点）＋ 状态筛选（全部/已启用/已停用）＋ 字标头像、协议·模型数、状态点；底部常驻「添加供应商」 |
| 详情头部 | 头像 + 名称 + 启停/密钥徽章 + 协议·模型数；操作只留「编辑配置 / 拉取模型」 |
| 供应商信息 | 定义列表（ID/协议/端点/代理/请求头/密钥，ID 与端点可复制）＋ 概览指标（模型/可用/支持工具）＋ 模型速览芯片 |
| 危险区 | 停用/启用与删除沉到详情底部，常驻可见；不再和主操作抢注意力 |
| 弹窗 | 供应商表单（协议分段控件）、拉取模型（上下文条 + 搜索 + 全选 + 已选计数）、模型表单（模型 ID 只读、上下文/最大输出两列、能力多选、推理级别动态标签、启用开关） |
| 说明文案 | 一律走「!」图标 + hover tooltip（`.pm-tip[data-tip]`），正文只留状态与数据 |
| 操作反馈 | 统一用底部居中 toast（2.4s 自动消失，失败 4.2s + 危险色）；读取失败这类长期错误就地放在列表里，不进 toast |

两个实现要点：

- **SSR 预取键是绑定名**：`data.backend["provider-admin"]["page_providers"]`（旧代码读 `state`，导致首屏总是空占位、要等客户端再拉一次）。组件先读 `page_providers`，再兼容 `state`。
- **VNode 白名单不含 `svg`**：图标不能用内联矢量，也不引入未登记组件；感叹号、加号、叉号、字标头像全部用文字 + CSS 实现，保证 SSR 与客户端一致。
- **`methods` 名不要和 `data` 字段同名**：运行时是单个 Proxy，`get` 里 `data` 优先级高于 `methods`，同名会让方法被字段遮住（`this.x is not a function`），表现为「点了没反应」。所以提示方法的字段叫 `toast`、方法叫 `showToast`。

## 构建

```sh
go run ./tools/pluginbuild -dir plugins/official/provider-admin -entry client.tsx -out static/client.js
go run ./tools/pluginbuild -dir plugins/official/provider-admin -entry client.tsx -out static/client.js -check
```

## 后端路由

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| `GET` | `/state` | 读取供应商目录与当前模型 |
| `POST` | `/save` | 新建/更新供应商与模型清单 |
| `POST` | `/remove` | 删除供应商 |
| `POST` | `/fetch-models` | 服务端用已保存端点/密钥拉取 `/models` |
| `POST` | `/select` | 切换当前模型 |

## 安全与数据边界

- API Key 只保存在后端；返回给页面的是 `hasApiKey` 与尾号 `apiKeyHint`。
- `save` 时 `apiKey` 留空表示保持原值；需要清除时显式传 `clearApiKey: true`。
- 「获取模型」由服务端发起请求，密钥不出现在页面或浏览器请求里。
- 数据面由宿主能力 `model.admin` 提供（`ctx.providers`），插件不直接读 Core RPC。

## 构建

- 后端：`index.ts` → `dist/index.js`，由 `node plugins/official/build.mjs` 生成。
- 前端：`slot.tsx` / `page.tsx` 在渲染时由宿主编译；`static/` 原样嵌入。
- 不要手改 `dist/` 或 `static/` 中的构建产物。

## 相关文档

- [模型与审批](../../../docs/03-model-and-approval.md)
- [SSR 前端](../../../docs/07-ssr-frontend.md)
- [插件模型与后端 API](../../../docs/05-plugins.md)
