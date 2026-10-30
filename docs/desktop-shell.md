# 桌面壳（Electron）

> 从 a_da 的 `electron/` 迁移（上游 MIT，commit `ae2f8ffe1679…`）。
> 上级索引：[design](design.md) · [release-m5](release-m5.md)

ada-go 的桌面壳是一个薄 Electron 宿主：拉起 `adag gateway`、解析它的 stdout
JSON 握手行，然后用无边框窗口加载网关的 SSR 界面。会话、审批、插件、技能等
一切业务能力都来自网关 + Core（单二进制），Electron 只做窗口/生命周期/托盘级
交互，不含任何业务逻辑。

## 运行

```sh
make desktop        # 构建 adag → desktop/bin，以冒烟模式启动一次（自动退出）
make desktop-dev    # 同上，交互模式（弹真实窗口）
make desktop-pack   # electron-builder --win dir，产物在 desktop/dist/
```

首次 `make desktop` 会经 `npx` 下载 Electron 33（约 100MB，之后有缓存）。

也可以直接在 `desktop/` 下 `npx electron .`：壳启动时会自动准备网关——开发模式下
**每次启动都重新构建**（不依赖 mtime 判断），执行 `GOWORK=off CGO_ENABLED=0 go build
-trimpath` 到 `desktop/bin/adag`，并注入 Revision/BuildTime；构建过程与结果写入
`~/.adago/launcher.log`。等待期间窗口显示「正在启动网关」页，就绪等待上限 180 秒。
重复执行 `npm run start`（单实例锁挡住新进程）会让已运行实例重新构建、重启网关并
刷新界面。设置 `ADAGO_GATEWAY_AUTOBUILD=0` 可关闭自动构建；显式指定外部网关
（`--connect` 或 `gateway_bin_path`）时不构建。打包形态只用随包
`resources/bin/adag`，不会构建。

开发模式还会启动文件监听（`ADAGO_DEV_WATCH=0` 可关闭）：`cmd`、`internal`、`app`、
`plugins`、`products` 以及根目录 `go.mod`/`go.sum` 变化后，700ms 防抖再走同一条
「刷新后端插件 dist → go build → 重启网关 → 清缓存 → 原地刷新当前页面」流程。
`desktop/`、`dist/`、`bin/`、`node_modules/` 与 `_test.go` 不触发。重启会中断正在
运行的轮次；页面地址不变（端口变化时才回首页）。

## 与 a_da 的差异（迁移适配点）

| 主题 | a_da | ada-go |
| --- | --- | --- |
| 后端进程 | `ada-gateway --port 52353`，stdout 打 `A_DA_GATEWAY_READY <json>` | `adag gateway --addr …`，stdout 打单行 JSON（endpoint/workspaces/token/…） |
| 端口策略 | 固定 52353（被占顺延由网关负责） | 先试 52353，被占用自动退 `127.0.0.1:0` 重试一次 |
| 登录 | SSR guard 校验 `?token=` 换发 cookie | 网关同款：`?token=` 匹配 → IssueSession + 303 去 query（`gateway.tokenLoginGate`） |
| 令牌来源 | CLI/配置，可空 | 本壳生成一次并持久化（`~/.adago/desktop_launcher.json`），经 `ADAG_GATEWAY_TOKEN` 注入网关——重启网关令牌不变 |
| Core 进程 | 网关之外还要 spawn `ada-coding` | 网关内置 Core 监督（supervisor 按 workspace 拉起 `adag core`），壳只管网关 |
| 系统托盘 | Tray + logo.ico | Tray + logo.ico（图标取自 a_da `assets/logo.ico`），菜单项同形；`desktop:open-web-ui` IPC 保留 |
| 构建工具 | bun + esbuild 打包 TS | 直接用 CJS（`main.cjs`/`preload.cjs`），零构建步骤 |
| 产品声明 | `products/` 随 extraResources | `desktop/products/headless.agent.json`（`make desktop` 从 `products/` 拷贝），壳显式传 `--product` |

不变的部分：单实例锁、无边框窗口 + preload 往 `.ada-topbar` 注入拖拽与
最小化/最大化/关闭（DOM/class 与 a_da 同形）、`desktop:*` IPC 通道名、
launcher.log 位置语义（`~/.adago/launcher.log`）、网关不可用时的内联提示页、
重启用 `taskkill /f /t`（Windows）。

## 系统托盘

启动后会在通知区创建托盘：单击把窗口叫到前台；右键菜单提供「显示/隐藏主窗口」
「打开网页界面」「重新唤醒网关…」「退出 ada-go」。图标在打包后位于
`resources/icon.ico`（构建时由 `build/app.ico` 映射），开发期直接读
`desktop/build/app.ico`；找不到图标只记 `launcher.log`，不影响启动。

## 安全与边界

- 窗口 `contextIsolation: true`、`nodeIntegration: false`；`webSecurity: false`
  与 a_da 保持一致（界面来自 `http://127.0.0.1`，CSP 由网关中间件下发）。
- 网关只绑回环；令牌经环境变量注入网关，不进命令行参数。
- `?token=` 登录只在 GET 生效，303 后 query 里的 token 即失效（cookie 是
  HttpOnly 的会话 cookie）。

## 打包

`make desktop-pack` 产出 `desktop/dist/win-unpacked/`：

- `adag-desktop.exe`（Electron 主程序）
- `resources/bin/adag.exe`（Go 二进制，来自 `make desktop` 拷贝）
- `resources/products/headless.agent.json`
- `resources/plugins/official/`（官方插件源目录与 `dist/`；Core 启动时优先从
  `resources/bin/../plugins/official` 加载；目录不存在时拒绝启动，二进制不含内嵌副本）

NSIS 安装包把 `--win dir` 换成 `--win nsis`（`npm run dist`）。注意
`mingw32-make` 的 recipe 字符串必须全 ASCII（Windows 代码页会把中文引号打碎，
见 release-m5.md 同款陷阱）。

## 已知限制

- 无自动更新（a_da 亦无）、无深度链接协议。
- 打包后的产品声明固定 headless；要换产品改 `desktop_launcher.json` 的
  `product_path` 或用 `--product` 传入。
- Electron 的下载/打包依赖网络；`make desktop` 首次运行需要可达 npm registry。
