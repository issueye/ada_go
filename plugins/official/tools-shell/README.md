# 命令工具（tools-shell）

在工作区边界内执行外部命令。只有 `execFile` 语义：命令与参数分开传，永远不经过 shell（没有管道、通配、变量展开）。

前台用 `run_command`；长任务用 `run_background` 启动，`check_task` 轮询，`kill_task` 终止。后台任务在轮次结束后继续运行，插件 Runtime 关闭时由宿主统一终止。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `tools-shell` |
| 名称 | 命令工具 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | `exec` |

## 工具：`run_command`

| 参数 | 说明 |
| --- | --- |
| `command` | 可执行文件（绝对路径或 PATH 中的名字） |
| `args` | 字符串数组；带空格或 shell 元字符的参数作为一个元素传入 |
| `cwd` | 工作目录（虚拟路径，如 `/workspace/sub`），必须落在工作区内 |
| `timeoutMs` | 超时毫秒数，插件校验范围为 `(0, 600000)`；实际默认值与上限由宿主决定 |

调用示例：`{ "command": "git", "args": ["status", "--short"] }`。

## 后台任务

| 工具 | 说明 |
| --- | --- |
| `run_background` | 启动后台命令，立即返回 `taskId`/`pid`；参数与 `run_command` 相同，另有 `timeoutMs`（缺省/上限 30 分钟） |
| `check_task` | 按 `taskId` 查看 `running`/`exited`/`timeout`/`killed`/`failed`、退出码、stdout/stderr 尾部与字节数；标记 `repeatable` |
| `kill_task` | 按 `taskId` 终止本插件启动的后台任务；返回 `found`/`killed` |

后台任务同样经过程序策略、cwd scope、env 白名单与输出截断；每 Runtime 最多同时保留 8 个任务。

## 行为与限制

- 命令与参数分开传递；`args` 为空且 `command` 含空白时直接拒绝，避免把整条命令行当成可执行文件。
- 不经过 shell，因此没有管道、通配符、重定向与变量展开。
- `cwd` 由宿主映射到工作区；越界拒绝。
- 环境变量只注入清单白名单（本插件未声明 `env`，因此只使用宿主默认白名单）。
- 输出按行数（2000）与字节数（50KB）双重截断，截断处有可见标记。
- 非零退出码**不抛错**，原样返回给模型判断；启动失败、超时或被策略拒绝才抛异常。
- 每条命令都有审计记录；`run_command`/`run_background` 的执行权限仍受审批策略（如 `policy-default`）约束，`check_task`/`kill_task` 是只读控制。
- 后台任务在插件 Runtime 关闭、超时或 `kill_task` 时杀整棵进程树；`check_task` 单路输出最多返回尾部 64KB。

## 失败模式

- 清单未声明 `exec` 时以 `ERR_ACCESS_DENIED` 失败。
- 参数类型错误、`timeoutMs` 越界、命令启动失败、超时都会带错误码返回。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [Node 兼容与宿主边界](../../../docs/12-node-compat.md)
- [模型与审批](../../../docs/03-model-and-approval.md)
