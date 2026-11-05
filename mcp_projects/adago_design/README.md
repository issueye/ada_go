# adago_design — 设计体系 MCP（浏览器无限画布）

一个「设计体系 + 浏览器无限画布」的 MCP 服务器：AI Agent 通过 MCP 工具读取
多套设计体系的 Design Token 与组件规范，把组件预览（真实 HTML+CSS 渲染）以
卡片形式摆到浏览器里的无限画布上，做多体系并排对比、组合页面、截图回传。

内置八套体系（Token 基于各体系公开设计规范整理，为近似实现）：

| id | 体系 | 主色 | 主题 |
|---|---|---|---|
| `apple` | Apple HIG (iOS) | systemBlue `#007AFF` | light / dark |
| `volcano` | 火山引擎 Arco Design | arcoblue-6 `#165DFF` | light / dark |
| `douyin` | 抖音设计体系 | 抖音红 `#FE2C55` + 电光青 `#25F4EE` | light / dark |
| `xiaomi` | 小米 MIUI / HyperOS | 小米橙 `#FF6900` | light / dark |
| `element` | Element Plus | primary `#409EFF` | light / dark |
| `ant` | Ant Design 5 | daybreak `#1677FF` | light / dark |
| `tdesign` | 腾讯 TDesign | brand `#0052D9` | light / dark |
| `material` | Material Design 3 | 基线紫 `#6750A4` | light / dark |

组件（一份 HTML 结构 × 每体系一份皮肤 CSS）：button / input / select / switch /
checkbox / radio / slider / card / tag / badge / avatar / alert / progress /
navbar / tabbar / list / dialog / toast。

## 构建

```powershell
# 推荐走仓库根脚本（GOWORK=off）
.\scripts\build-mcp.cmd

# 或手动
cd mcp_projects/adago_design
GOWORK=off go build -trimpath -o bin/adago-design.exe .
```

## 运行形态（一个二进制，两种模式）

```powershell
# 人工查看：只跑画布服务并自动打开浏览器
bin\adago-design.exe serve            # http://127.0.0.1:8788/

# Agent 接入：stdio MCP + 内嵌画布服务（不自动开浏览器）
bin\adago-design.exe mcp
```

公共参数：`-port 8788`（被占用自动 +1 探测）、`-data <目录>`（默认
`%APPDATA%\adago-design`，可被 `ADAGO_DESIGN_DATA` 覆盖）、`-open`、
`-theme dark|light`。

## MCP 工具

| 工具 | 用途 |
|---|---|
| `design_systems` | 列出全部设计体系 |
| `design_tokens` | 查询 Token（system/theme/category：color/type/spacing/radius/shadow/motion） |
| `design_components` | 组件清单；指定 component 返回详情与 HTML 片段 |
| `design_search` | 跨体系搜索体系/组件/Token |
| `design_preview` | 用某体系把组件渲染成真实预览页，默认自动摆上画布 |
| `design_screen` | 按模板（login/settings/feed/profile）生成整页真实预览，自动摆上画布 |
| `design_compare` | 两体系 Token 对比（结构化 diff）；可选生成并排对比板节点 |
| `canvas_add` | 添加任意 HTML 预览卡片（自动布局或指定坐标） |
| `canvas_list` / `canvas_move` / `canvas_remove` / `canvas_clear` | 画布节点管理 |
| `canvas_arrange` | 节点自动网格排版（体系分组优先） |
| `canvas_restore` | 恢复最近一次清空（内存快照） |
| `canvas_export` | 导出整块画布为独立 HTML 文件 |
| `canvas_screenshot` | 截图画布或单个节点，返回 PNG（需画布已在浏览器打开） |
| `canvas_url` | 获取画布访问地址 |

错误以 `isError` 结果返回并携带稳定错误码：`unknown_system` /
`unknown_component` / `capture_timeout` / `confirm_required` / `bad_request` /
`screen_failed` / `compare_failed` / `empty_canvas`。

## 接入 ada-go（stdio）

`.adago/mcp.json`（或 `~/.adago/mcp.json`）：

```json
{
  "id": "adago_design",
  "transport": "stdio",
  "command": "E:\\codes\\go_projects\\ada_go\\mcp_projects\\adago_design\\bin\\adago-design.exe",
  "args": ["mcp"],
  "enabled": true
}
```

写入后重载 mcp-client 插件即可。也可以用根脚本注册：

```powershell
.\scripts\build-mcp.cmd -Register        # 构建 + 注册 computer-use（原有行为）
.\scripts\build-mcp.cmd -Register -IncludeAdagoDesignMcp
```

## 截图原理（无 CDP）

`canvas_screenshot` 创建截图任务 → 已打开的画布页面长轮询领取 → 把目标
预览 HTML 放进离屏 iframe → `SVG foreignObject` 序列化 → `<canvas>` 光栅化
→ PNG 回传给 MCP。8 秒内没有页面响应则返回 `capture_timeout`，提示先打开
画布地址。预览内容是纯 CSS（无外链资源），渲染安全不失真。

## 测试与门禁

```powershell
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

- `internal/design`：五体系 Token 完备性、全部组件在全部体系下可渲染。
- `internal/canvas`：Store CRUD/持久化/并发、HTTP API 全路径、截图任务闭环。
- `internal/mcpserver`：内存 MCP 会话走 `tools/list` + `tools/call` 契约。

设计细节见 [DESIGN.md](DESIGN.md)。
