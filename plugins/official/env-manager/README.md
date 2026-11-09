# 环境变量（env-manager）

管理注入插件子进程的环境变量：全局 `~/.adago/env.json`（`ADAGO_HOME` 优先）与工作区
`<workspace>/.adago/env.json`，工作区同名覆盖全局；旧工作区 `.adag/env.json` 只读兼容（新文件存在时忽略），写入落 `.adago/env.json`。变量注入到 `ctx.exec`、后台任务与
`ctx.process.start`（stdio MCP）启动的子进程，同名覆盖宿主环境；**不进入插件
`process.env`**（那里仍按清单 `backend.env` 白名单）。值不写日志。

## 页面

启用插件后，侧栏「环境变量」或 `/ext/env-manager/settings`。

0.2.0 起页面改为岛屿组件（`env-page.tsx`，SSR + hydrate），结构与交互：

| 区域 | 现状 |
| --- | --- |
| 页面头部 | 标题 + 「!」说明图标（两层覆盖关系收进 tooltip）+ 刷新 / 重载插件 |
| 环境变量卡片 | 头部：数量 + 范围筛选（全部/工作区/全局）；表单一行：范围分段 + 变量名 + 值 + 添加/更新；行列表：等宽变量名、打码值、范围徽章、被覆盖标记、行内 显示/隐藏·复制·删除；底注：打码与命名规则 |
| 服务程序目录 | 两个目录块（范围 + 已创建状态 + 等宽路径 + 复制路径）+ 程序清单（名称 / 范围 / 大小 / 修改时间） |

约定：

- 工作区同名项优先展示并标注「覆盖全局」，全局同名项弱化显示，避免看成一式两份；
- 值默认打码（保留尾 4 位），逐行「显示 / 隐藏」，「复制」取原文；
- **变量名在前端就按 `^[A-Za-z_][A-Za-z0-9_]*$` 校验**，非法或 `ADAG_` 前缀时禁用提交并给出原因，不用等后端 400；
- 一次性反馈统一走底部 toast（方法名 `showToast`，`data` 里同名字段叫 `toast`——两者同名会被 data 遮住）；
- 「重载插件」重新装配，新的子进程才会拿到新环境。
- 旧目录兼容、读取失败这类提示放在列表**下方**，用左侧色标的窄条（`.em-notes`），不是满宽告警横幅——
  它们解释的是「读写落在哪」，不是页面出错；用量少时窄条只占文字宽度。

legacy `page.tsx` 与 `static/env-manager.js` 保留在仓库作历史参考，清单不再加载它们，
新的 `env-manager.css` 也不包含旧页面所需的类。

## 数据面

页面经本插件 route 调用 `ctx.config`（`env` 命名空间）与 `ctx.fs`（服务程序目录）：

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
`dist/`。`env-page.tsx` 由 Gateway 渲染桥在运行时编译，`static/` 原样嵌入。

浏览器 bundle（岛屿）由 pluginbuild 生成，改完 `client.tsx` / `env-page.tsx` 要重新构建：

```sh
go run ./tools/pluginbuild -dir plugins/official/env-manager -entry client.tsx -out static/client.js
go run ./tools/pluginbuild -dir plugins/official/env-manager -entry client.tsx -out static/client.js -check
```

页面通过只读动作 state 预取 `/state`（落在 `props.data.backend["env-manager"].page_settings`），
SSR 与无 JS 时也能看到两层变量与服务程序目录。
