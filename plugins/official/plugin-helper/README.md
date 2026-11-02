# 插件助手

两种创建方式，共用同一套校验与落盘：

1. **对话创建**：在会话里说「创建一个 XXX 插件」。AI 编写入口源码后调用本插件的
   `create_plugin` 工具（`access: write`，会经过审批策略确认）落盘。
2. **向导创建**：在本页填写插件 ID、名称、描述与示例工具名，生成回显示例插件。

两者都把插件安装到全局目录 `~/.adago/plugins/<id>/`（生产统一路径，跨工作区共享）。

## 生成内容

- `plugin.json`：`trust: third-party`，后端入口 `index.ts`，能力清单按需声明
- `index.ts`：宿主可直接执行的 ESM JavaScript；AI 传入源码时原样写入（须包含
  `export default function register(agent)` 并注册声明的工具），否则生成回显示例
- `README.md`：插件说明与开发约束

## AI 创建的约束

- 入口不做运行时 TypeScript/npm 编译；需要运行时语义的 TS 特性（enum/namespace）不可用
- `capabilities` 只接受 `fs.read`、`fs.write`、`state`、`net.fetch`；
  `net.sse`/`exec` 仅 builtin/verified
- 已有目录不会被覆盖；写入经宿主 `plugin.admin` 能力桥，限定在全局插件根内

创建后需在插件列表或插件详情页点“重载”加载，再“启用”。

## 移除插件（remove_plugin）

对话里说「移除/卸载 XXX 插件」时，AI 调用 `remove_plugin`（`access: write`，
过审批）：删除发现优先级最高的插件目录（工作区旧目录优先于全局根），并清理
两份启停状态里的条目；随包插件不能移除。移除后需在插件页点「重载」生效。

## 健康检查（check_plugin）

对话里说「检查一下 XXX 插件 / 插件为什么不工作」时，AI 调用 `check_plugin`
（`access: read`）：返回发现位置（随包/全局/工作区）、清单字段、启停决策来源
（全局 plugins.json / 工作区 .adag/plugins.json / 产品声明）、装配状态与
last_error，并对未停用的落盘插件做入口预加载校验；`id` 留空返回全部插件摘要。
向导页同款能力走 `POST /check-plugin` route。

## 随包技能

`skills/plugin-manual/SKILL.md` 是插件手册，教 AI 如何使用本插件（对话创建流程、
入口约束、修改/移除已有插件），并给出 **Markdown 动态块（`adag-ui`）** 的使用说明：
语法、随包可用的 renderer（`token-usage/usage-card`、`ui-charts/*`）、data 规则、
限额与降级。产品声明 `capabilities.skills: true` 时，模型经系统提示看到技能的
名称、简介与绝对路径，并用 `read_file` 按需读取正文；headless / coding 产品均已开启。

## API 规范

`SPEC.md` 是随包的平台插件 API 规范：清单字段、`agent.*` 注册面（工具/动作/
提示词/供应商/route）、`ctx` 能力与信任矩阵、资源上限、生命周期、前端段契约
与当前渲染边界，以及本插件自身 route/工具的完整契约。AI 生成插件代码前按它
自检；工作区即仓库时可用 fs 工具直接读取。
