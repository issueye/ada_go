# 供应商管理（provider-admin）

模型设置页：集中管理供应商（端点、协议、API Key、模型清单）与当前模型。页面经本插件自己的 route 读写数据，API Key 不经过浏览器。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `provider-admin` |
| 名称 | 供应商管理 |
| 版本 | 0.3.1 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 前后端同包 |
| 后端能力 | `model.admin` |
| 前端能力 | `frontend.script` |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 插槽 | `composer.left`（`slot.tsx`，order 10）：模型选择器旁的「模型设置」入口 |
| 导航 / 页面 | 导航「供应商」→ 页面 `/ext/provider-admin/providers`（`page.tsx`） |
| 脚本 / 样式 | `provider-admin.js`、`provider-admin.css`（`static/`） |

页面首屏只渲染骨架，数据与交互全部由 `provider-admin.js` 调本插件 route 完成。禁用插件时入口、页面与脚本一起消失。

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
