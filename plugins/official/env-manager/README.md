# 环境变量（env-manager）

管理注入插件子进程的环境变量：全局 `~/.adago/env.json`（`ADAGO_HOME` 优先）与工作区
`<workspace>/.adago/env.json`，工作区同名覆盖全局；旧工作区 `.adag/env.json` 只读兼容（新文件存在时忽略），写入落 `.adago/env.json`。变量注入到 `ctx.exec`、后台任务与
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

## 服务程序

把可执行文件放进这两个目录，插件子进程可以直接按名字调用（不用写绝对路径）：

- `<workspace>/.adago/bin`（工作区，优先）
- `~/.adago/bin`（用户级；`ADAGO_HOME` 优先）

宿主启动 `ctx.exec`、后台任务与 `ctx.process.start`（stdio MCP）时，会把这两个目录
前置到 `PATH`（去重，工作区在前）；白名单里没有 PATH 时只用这两个目录填一个，不凭空
继承宿主 PATH。目录不存在时 PATH 里仍保留，用户新建目录并放入程序后，新启动的子进程
即可使用，不需要重载插件。

页面「服务程序」列出两个目录（已创建/未创建）与扫描到的可执行文件（Unix 看可执行位，
Windows 看 `.exe/.cmd/.bat/.com/.ps1`），非可执行文件不列出。页面不负责上传/安装程序。

## 与 CLI 的关系

`adag env list|set|unset` 操作的是同一份文件：页面改完可以用 CLI 查看，反之亦然。
修改在任何地方完成后，需要 `plugin.reload`（页面上的「重载插件」）让新变量进入
新启动的子进程。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成；不要手改
`dist/`。`page.tsx` 与 `static/` 由 Gateway 渲染桥在运行时加载，不进 `dist/`。


