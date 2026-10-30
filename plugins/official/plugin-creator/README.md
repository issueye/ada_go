# 创建插件

工作区插件脚手架向导：填写插件 ID、名称、描述与示例工具名，在工作区
`.adago/plugins/<id>/` 下生成可直接加载的第三方插件。

## 生成内容

- `plugin.json`：`trust: third-party`，后端入口 `index.ts`，能力清单为空
- `index.ts`：注册一个只读回显工具；内容是宿主可直接执行的 ESM JavaScript
- `README.md`：插件说明与开发约束

创建成功后会自动重载插件列表，并跳转到新插件详情页。新插件默认未启用。

## 能力

后端只使用 `fs.read` / `fs.write`，路径固定在工作区 `.adago/plugins` 下；
插件 ID 限小写字母、数字和连字符，已有目录不会被覆盖。

## 约束

第三方插件入口不做运行时 TypeScript/npm 编译；需要文件、状态、网络等能力时，
在生成的 `plugin.json` 的 `backend.capabilities` 里声明。
