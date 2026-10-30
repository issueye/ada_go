# 文件工具（tools-fs）

在工作区内读写文件：读、写、精确编辑、列目录、子串搜索。插件只负责「模型友好的形状」——行偏移、截断、精确匹配、递归与搜索；真正的边界（根、只读、symlink 落点、大小上限、审计）由宿主的 `ctx.fs` 执行。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `tools-fs` |
| 名称 | 文件工具 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | `fs.read`、`fs.write` |

## 工具

| 工具 | access | 作用 |
| --- | --- | --- |
| `read_file` | `read` | 按行偏移/行数读取文本，单次输出上限 50KB |
| `write_file` | `write` | 整体覆盖写入（父目录不存在会失败） |
| `edit_file` | `write` | 精确文本替换；默认要求 `old` 只出现一次，`all: true` 才全改 |
| `list_files` | `read` | 列目录；`recursive` 时递归，深度上限 5 层、条目上限 2000 |
| `search_files` | `read` | 子串搜索，返回 `路径:行号: 内容` |

## 行为与限制

- 路径先做显示层归一（折叠 `./`、拒绝越出工作区的 `..`），宿主还会再判一次。
- `read_file` 缺省最多 2000 行；输出按 50KB 截断，截断处有可见标记。
- `edit_file` 找不到 `old` 或匹配多次且未传 `all` 时报错，避免改错位置。
- `list_files`/`search_files` 的 `path` 传空串与缺省同义，都指工作区根。
- `search_files` 区分大小写，跳过二进制文件和大于 256KB 的文件，最多扫描 2000 个文件；结果缺省 100 条、上限 500 条。
- 目录项排序：目录在前，同类按名称排序。

## 失败模式

- 清单未声明 `fs.read`/`fs.write` 时，工具以 `ERR_ACCESS_DENIED` 失败。
- 越界路径、非法参数、宿主 IO 错误都以带错误码的异常返回，模型可据此调整。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成并被 `go:embed` 进二进制。不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [Node 兼容与宿主边界](../../../docs/12-node-compat.md)
