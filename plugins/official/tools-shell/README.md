# 命令工具（tools-shell）

在工作区边界内执行外部命令。只有 `execFile` 语义：命令与参数分开传，永远不经过 shell（没有管道、通配、变量展开）。

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

## 行为与限制

- 命令与参数分开传递；`args` 为空且 `command` 含空白时直接拒绝，避免把整条命令行当成可执行文件。
- 不经过 shell，因此没有管道、通配符、重定向与变量展开。
- `cwd` 由宿主映射到工作区；越界拒绝。
- 环境变量只注入清单白名单（本插件未声明 `env`，因此只使用宿主默认白名单）。
- 输出按行数（2000）与字节数（50KB）双重截断，截断处有可见标记。
- 非零退出码**不抛错**，原样返回给模型判断；启动失败、超时或被策略拒绝才抛异常。
- 每条命令都有审计记录；执行权限仍受审批策略（如 `policy-default`）约束。

## 失败模式

- 清单未声明 `exec` 时以 `ERR_ACCESS_DENIED` 失败。
- 参数类型错误、`timeoutMs` 越界、命令启动失败、超时都会带错误码返回。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [Node 兼容与宿主边界](../../../docs/12-node-compat.md)
- [模型与审批](../../../docs/03-model-and-approval.md)
