# 环境变量（env-manager）

管理注入插件子进程的环境变量：全局 `~/.adago/env.json`（`ADAGO_HOME` 优先）与工作区
`<workspace>/.adag/env.json`，工作区同名覆盖全局。变量注入到 `ctx.exec`、后台任务与
`ctx.process.start`（stdio MCP）启动的子进程，同名覆盖宿主环境；**不进入插件
`process.env`**（那里仍按清单 `backend.env` 白名单）。值不写日志。

## 页面

启用插件后，侧栏「环境变量」或 `/ext/env-manager/settings`：

- 列表分「工作区 / 全局」两段；工作区同名项标注「覆盖全局」；
- 值默认打码，点「显示」展开；行内「编辑」输入新值后回车保存；
- 「删除」需确认；「添加 / 更新」按范围 + 变量名 + 值写入；
- 「重载插件」重新装配，新的子进程才会拿到新环境。

## 数据面

页面经本插件 route 调用 `ctx.env`（capability `env.admin`，仅 builtin）：

- `GET  /ext/env-manager/api/state` — 两层原文（含路径与错误）+ 合并视图；
- `POST /ext/env-manager/api/set` — `{scope: "global"|"workspace", key, value}`；
- `POST /ext/env-manager/api/remove` — `{scope, key}`。

宿主只接受固定路径（脚本不能传路径），键名匹配 `^[A-Za-z_][A-Za-z0-9_]*$`、`ADAG_`
前缀保留、单值 ≤32KB、单文件 ≤256KB；文件 0600、目录 0700、原子替换。写操作由插件
route 的 CSRF 校验保护；审计只记录键名与路径，不记录值。

## 与 CLI 的关系

`adag env list|set|unset` 操作的是同一份文件：页面改完可以用 CLI 查看，反之亦然。
修改在任何地方完成后，需要 `plugin.reload`（页面上的「重载插件」）让新变量进入
新启动的子进程。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成；不要手改
`dist/`。`page.tsx` 与 `static/` 由 Gateway 渲染桥在运行时加载，不进 `dist/`。
