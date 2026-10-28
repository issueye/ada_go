# 性能基线与采样方法（M5）

> 对应章节：[10-testing-performance.md §12](10-testing-performance.md#12-性能目标)（目标表）、
> [development-plan.md §4.6](development-plan.md#46-m5加固兼容与发布2535-人日)（性能门禁）。
> 本文档记录 **当前实测数字、采样方法与排查阈值**；目标表仍以 docs/10 为准。

## 1. 采样方法

所有数字来自 `go test`，本机单次采样（开发机：Windows 11 x64，SSD，Go 工具链默认优化，
`-count=1`），命令：

```sh
# Gateway E2E（真实进程：gateway 二进制 + Core 子进程 + HTTP + fake 模型）
go test ./internal/gateway -run TestGatewayE2E -v
# 输出行：
# e2e_test.go:401: 性能基线（本机、fake Core；仅记录，不做阈值断言）：
#   startup_ms=… first_page_ms=… turn_complete_ms=…

# Core 装配自检（产品声明 × 官方插件全量装配 + dispatch 覆盖检查）
go run ./cmd/adag core --workspace . --self-check
```

口径说明：

| 指标 | 测量点 |
|---|---|
| `startup_ms` | gateway 进程启动 → 握手行就绪（**不含** Core 拉起；Core 在首个页面请求时按需拉起） |
| `first_page_ms` | 首个带鉴权的 `GET /` 请求完成（**含** Core 拉起 + 握手 + 首次装配 + SSR 渲染） |
| `turn_complete_ms` | 一轮 fake 模型对话从 `turn.start` 到 `evt.run.status terminal=completed` |
| core 自检 | 产品加载 + 约 10 个官方后端插件装配 + dispatch 表覆盖校验 |

race 与门禁：`make race`（CI Linux 腿）、`make gate`（与 ci-gate.sh 完全一致）。
Windows 本机无 cgo 时 `-race` 降级为普通测试（M0 已知限制，见 Makefile 注释）。

## 2. 实测数字（2026-10，M5 RC 采样）

| 指标 | 实测（三次采样） | docs/10 目标 | 结论 |
|---|---|---|---|
| Gateway 冷启动（不含 Core） | 39–47 ms | < 200 ms | 达标 |
| 首页（含 Core 拉起与首次装配） | 42–44 ms | —（Core 拉起 + 握手目标 < 500ms + SSR） | 达标 |
| fake 轮次端到端 | 3 ms | < 200 ms（fake turn 阈值，本文新增） | 达标 |
| Core 装配自检（`core --self-check`） | 43–66 ms | 装配 < 1.2 s（docs/10） | 达标 |

采样噪声主要来自首次 `go build` 产物是否已在页缓存；连续采样取最小值更接近稳态。

## 3. 排查阈值（超标即排查，不是硬断言）

| 指标 | 阈值 | 超标时的第一排查点 |
|---|---|---|
| gateway startup | < 1 s | 端口绑定重试、嵌入资源解压、日志初始化 |
| first page | < 500 ms | Core 拉起握手（4.2 的 5s 上限内应完成）、插件装配（< 1.2s 目标）、模板缓存是否命中 |
| fake turn | < 200 ms | 事件合并窗口、SQLite 写入（entry 追加）、钩子链耗时 |
| core self-check | < 1.5 s | moejs Runtime 构建、清单解析、能力安装 |

超过阈值先跑 `-count=5` 取最小值排除页缓存噪声；仍然超标再用 `go test -bench` /
pprof 定位，并在 PR 里给出前后对比。本表是 **回归雷达**，不进入 CI 硬门禁
（docs/10 的目标表才是验收口径）。

## 4. 已知未测项

- 真实 provider 网络（sse 桥接 p95 < 5ms/事件批）需要录制夹具回放，M5 未做，残留 v1.1。
- 事件重放 10k 条 < 150ms、并发 64 SSE / 8 线程：压力口径未进 CI，`gateway` 包有
  单元级覆盖但无数值断言。
- 内存基线（Gateway ~40MB / Core ~60MB + 每插件 8–20MB）：未在 CI 采样，依赖 doctor
  的运行时观察。
