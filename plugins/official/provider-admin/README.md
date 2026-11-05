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
| 导航 / 页面 | 导航「供应商」→ 页面 `/ext/provider-admin/providers`（`providers-next.tsx`，SSR + hydrate） |
| 样式 | `provider-admin.css`（`static/`） |
| 浏览器 bundle | `static/client.js`（`pluginbuild` 从 `client.tsx` 生成） |

P2-4b-2b：`providers` 页已替换为组件化实现，支持新增供应商、设为当前、启用/停用、删除、拉取/添加模型、模型新增/删除/编辑与供应商信息编辑。拉取模型会打开勾选弹窗，只添加选中的模型；模型推理等级是动态标签，输入模态是复选框。legacy `provider-admin.js` / `page.tsx` 保留在仓库作历史参考，但清单不再加载脚本。页面通过 `backend.read` 动作 `state` 预取 `/state`，SSR/无 JS 也能看到供应商目录；关闭 `pluginIslands` 或设 `ADAG_DISABLE_ISLANDS=1` 时只保留 SSR。禁用插件时入口、页面与 bundle 一起消失。

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
