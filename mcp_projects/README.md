# mcp_projects — 独立 MCP 侧程序

本目录下的模块都是**自包含 Go module**（各自有 `go.mod`，不参与根模块），
由 ada-go 以独立进程方式启动。构建统一走仓库根目录的脚本：

```powershell
# 只构建两个二进制
.\scripts\build-mcp.cmd

# 构建并把 computer-use 注册进当前工作区 .adago/mcp.json
.\scripts\build-mcp.cmd -Register

# 注册到用户级 ~/.adago/mcp.json（所有工作区可用）
.\scripts\build-mcp.cmd -Register -Scope global

# 额外把 browser-use 也注册成 MCP server（默认不注册：
# 官方 browser-use 插件会自动发现并启动它，工具更全）
.\scripts\build-mcp.cmd -Register -IncludeBrowserUseMcp
```

PowerShell 等价写法：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-mcp.ps1 [-Register] [-Scope global] [-IncludeBrowserUseMcp]
```

脚本会以 `GOWORK=off` / `CGO_ENABLED=0` 构建，并保证产物落在 ada-go 直接查找的位置。

## 产物与 ada-go 的消费方式

| 模块 | 产物 | ada-go 怎么用 |
|---|---|---|
| `browser_use` | `mcp_projects/browser_use/bin/browser-use.exe` | 官方插件 `browser-use` 自动按这个路径查找可执行文件；也可用 `BROWSER_USE_EXE` 覆盖。无需 MCP 配置 |
| `computer_use` | `mcp_projects/computer_use/bin/computer-use.exe` | 走 `mcp-client` 插件：`computer-use mcp --spawn`（stdio）。`-Register` 会写入 `.adago/mcp.json` 或 `~/.adago/mcp.json` |
| `cron` | `mcp_projects/cron/bin/cron.exe` | 网关按「可执行文件同目录」启动；桌面构建由 `desktop/main.cjs` 自动编译，单独构建用 `make cron` |

`computer_use` 的 `.adago/mcp.json` 条目：

```json
{
  "id": "computer_use",
  "transport": "stdio",
  "command": "E:\\codes\\go_projects\\ada_go\\mcp_projects\\computer_use\\bin\\computer-use.exe",
  "args": ["mcp", "--spawn"],
  "enabled": true
}
```

写入后重载插件（插件页「重载」或重启 ada-go），
`/ext/mcp-client/servers` 会出现这个 server，工具进入工具目录。

## 前置条件

- Go **1.27.3+**（两个模块的 `go.mod` 要求；脚本会触发工具链下载）。
- Windows：`browser-use` 用 `-H windowsgui` 链接；`computer-use` 依赖 Win32，仅 Windows 可运行。
- `computer_use` 依赖 `github.com/egoist/mygo v0.7.4`（已写入其 `go.mod` / `go.sum`）。

单独的模块构建（调试用）：

```powershell
cd mcp_projects/browser_use
GOWORK=off go build -o bin/browser-use.exe .
cd ../computer_use
GOWORK=off go build -o bin/computer-use.exe .
```
