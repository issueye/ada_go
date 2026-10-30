# Git 工具（tools-git）

只读的 git 视图：状态、diff、提交历史。所有命令都以 `git --no-pager <只读子命令>` 执行，不提供任何会改动仓库状态的 action（commit、checkout、push 等请走 `run_command`，由审批策略裁决）。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `tools-git` |
| 名称 | Git 工具 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | `fs.read`、`exec` |

## 工具

| 工具 | 作用 |
| --- | --- |
| `git_status` | `git status --short --branch`：分支与变更文件 |
| `git_diff` | `git diff`；`staged: true` 时看 `--cached` 的已暂存改动 |
| `git_log` | `git log`，一行一条：哈希、作者、日期、标题；`maxCount` 缺省 20、上限 200 |

## 行为与限制

- `cwd` 可选，缺省工作区根；给定时必须在工作区内（插件先判一次，宿主的路径校验再判一次）。
- 每条命令 30 秒超时、输出按 50KB 截断，截断处有可见标记。
- 非零退出码不是脚本异常：git 的 "not a repository" 等失败文本原样回给模型判断，末尾附带退出码。
- 命令走宿主的 `ctx.exec`（`execFile` 适配器，无 shell、有审计），同样受审批策略约束。
- 输出为空时返回 `(无输出)`。

## 失败模式

- 清单未声明 `exec` 时以 `ERR_ACCESS_DENIED` 失败。
- `cwd` 越界或参数类型错误直接报错；命令启动失败/超时带命令名返回。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [模型与审批](../../../docs/03-model-and-approval.md)
