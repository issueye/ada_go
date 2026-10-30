# 附录

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：附录 A 插件示例、附录 B 配置示例、附录 C 与参考实现的差异、附录 D 术语  
> 版本：0.1 · 2026-10-28

## 附录 A：插件示例

示例由两个插件组成：`tools-plan`（后端，builtin，TS）负责维护计划；`task-plan`（前端，third-party，TSX）只负责把计划渲染成悬浮卡。两者通过清单里的只读动作绑定连接，互不 import。官方包把同一思路收进了 `tools-plan` 自己的前端段（真实动作数据，无示例回退）；这里刻意用第三方版本，展示最小权限路径。

### A.1 `task-plan/plugin.json`（只有前端）

```json
{
  "id": "task-plan",
  "name": "任务卡片",
  "version": "0.1.0",
  "apiVersion": 1,
  "description": "把 tools-plan 的计划渲染成会话区右上角的悬浮卡",
  "trust": "third-party",
  "frontend": {
    "capabilities": ["backend.read"],
    "slots": [{ "slot": "chat.corner", "template": "slot.tsx", "order": 10 }],
    "styles": ["task-plan.css"],
    "assets": "static",
    "actions": {
      "plan": { "action": "plan", "read": true, "backend": "tools-plan" }
    }
  }
}
```

### A.2 `tools-plan`（后端，官方 TS）

官方插件与第三方使用同一个入口；区别只有 `trust=builtin` 与随包发布。构建期把 `index.ts` 编成 `dist/index.js`，桌面打包随 Electron `resources` 发布，运行时仍由 moejs 执行。

`plugin.json`：

```json
{
  "id": "tools-plan",
  "name": "任务计划工具",
  "version": "0.1.0",
  "apiVersion": 1,
  "trust": "builtin",
  "backend": {
    "entry": "index.ts",
    "capabilities": ["state"]
  }
}
```

`plugins/official/tools-plan/index.ts`（只保留计划相关部分）：

```ts
import type { AgentAPI } from "agent:api";

type Step = { text: string; status: "pending" | "in_progress" | "done" };
type Plan = { steps: Step[]; notes: string; done: number; total: number; updatedAt: string };

export default function (agent: AgentAPI) {
  agent.registerTool({
    name: "todo",
    description: "写入或全量替换当前线程的任务计划",
    parameters: {
      type: "object",
      properties: {
        steps: {
          type: "array",
          items: {
            type: "object",
            properties: {
              text: { type: "string" },
              status: { type: "string", enum: ["pending", "in_progress", "done"] }
            },
            required: ["text", "status"],
            additionalProperties: false
          }
        },
        notes: { type: "string" }
      },
      required: ["steps"],
      additionalProperties: false
    },
    access: "read", // 只写线程级 state，无外部副作用
    execute: async (args, ctx) => {
      const steps = args.steps as Step[];
      const plan: Plan = {
        steps,
        notes: (args.notes as string) ?? "",
        done: steps.filter((s) => s.status === "done").length,
        total: steps.length,
        updatedAt: ctx.now
      };
      await ctx.state!.set("plan:" + ctx.threadId, plan);
      return {
        content: [{ type: "text", text: `计划已更新：${plan.done}/${plan.total}` }],
        details: plan
      };
    }
  });

  agent.registerAction({
    name: "plan",
    label: "读取任务计划",
    mode: "read",
    execute: async (input, ctx) => ctx.state!.get<Plan>("plan:" + input.threadId)
  });
}
```

示例只展示计划部分；随包发布的 `tools-plan` 还会注册 `finish`，并补充参数校验、错误文案与状态清理，从略。

### A.3 `task-plan/slot.tsx`（前端插槽）

```tsx
import { Icon, Todo } from "ssr:components";

export function Slot(props: SlotProps) {
  const plan = props.data.backend["task-plan"]?.plan as any;
  if (!plan || !plan.total) return null;

  return (
    <details class="tp-card" data-state="collapsed">
      <summary class="tp-card__summary">
        <Icon name="i-list" size={12} />
        <span class="tp-card__title">任务规划</span>
        <span class="tp-card__count">{plan.done + "/" + plan.total}</span>
      </summary>
      <div class="tp-card__body">
        <Todo steps={plan.steps} notes={plan.notes} />
      </div>
    </details>
  );
}
```

前端拿到的全部业务数据都来自 `props.data.backend["task-plan"].plan`（由 `tools-plan` 的只读动作返回）；基座不知道 `plan`、`steps` 或 `total` 这些字段，只负责鉴权、预取、VNode 校验与转义。

---

## 附录 B：配置示例

`~/.adago/config.toml`：

```toml
[server]
host = "127.0.0.1"
port = 52353
token = ""                 # 非回环监听时必填
allow_plaintext = false    # 非回环 + 无 TLS 时必须显式开启

[agent]
product = "coding"
workspace = "E:/work/demo"
turn_timeout = "0"         # 0 = 不限制墙钟；步数/工具调用次数从不是上限
max_turn_tokens = 0        # 0 = 不限制 token；需要成本上限时显式配置
loop_repeat_limit = 3      # 连续相同调用达到该次数先回错误回执
loop_abort_limit = 5       # 仍未改道则按 error 结束
stream_idle_timeout = "90s"

[storage]
busy_timeout = "5s"
wal_autocheckpoint = 1000
journal_size_limit_mb = 64
# state.db 必须位于本地磁盘；网络盘会被 doctor 拒绝

[model]
provider = "openai"
model = "<model-id>"
base_url = ""              # 兼容端点可填
# API key 从环境变量或 secrets.toml 读取，不写在这里

[plugins]
workspace_dir = ".adago/plugins"
global_dir = "~/.adago/plugins"
memory_limit_mb = 64
call_timeout = "5s"
hook_timeout = "200ms"

# Node 兼容层的默认边界；能力位仍由各插件清单决定
[plugins.node]
env_allow = ["PATH", "HOME", "TMPDIR"]  # process.env 白名单；密钥不在此列
timer_max = 256                          # 每 Runtime 未完成定时器
io_max_mb = 16                           # 单次 fs 读写 / fetch 响应体
exec_max_kb = 4096                       # child_process 捕获上限

# 插件设置由插件自己声明与校验；基座只按名存储
[plugins.settings.policy-default]
mode = "ask"               # readonly | ask | auto | full
interaction_timeout = "300s"

[gateway]
max_cores = 8
idle_reap = "15m"
sse_per_thread = 64

[limits]
tool_output_kb = 50
plugin_route_kb = 64
```

优先级：环境变量 > 工作区 `.adago/config.toml` > 用户配置 > 默认值。密钥只从环境变量或 `~/.adago/secrets.toml`（0600）读取，任何日志与 UI 都不回显。

---

## 附录 C：与参考实现的关键差异

| 主题 | a_da | ada-go |
|---|---|---|
| 模板引擎 | Askama（编译期） | `html/template` + Go 组件函数 |
| TSX 运行时 | OXC + Boa | esbuild + moejs |
| 插件内存 | 无上限 | 每 Runtime 64MB + 超时重建 |
| 协议 | 多处手写、83 方法 | JSON 单源 + 生成常量、35 方法 |
| 会话 | 文件 | SQLite append-only DAG |
| 审批 | 当前实现移除 | 基座只提供交互桥；策略插件（`tool.beforeCall` + `ctx.ui`）+ UI 托盘，无策略则失败关闭 |
| UI 与设计系统 | Black Swan token、`ada-*` 组件、DOM 与交互 | **直接复用同一套** token/CSS/DOM/插槽与交互；只把 Askama/Rust 换成 `html/template`/Go，不另造视觉语言 |
| 前端耦合 | OOB DOM id 较深 | 事件带 target/seq，断序快照 |
| 进程模型 | 网关 + 节点 | 网关 + Core，单二进制两角色 |
| 插件贡献 | 装配期结算 | 同左，双代原子重载 |
| Node 库 | 无（Boa，无 Node 兼容层） | Node API 形状子集：Go 原生库、按能力安装；无原生扩展/libuv/运行时 npm |
| 工具实现 | 内置 Rust + TS | 基座零工具；官方与第三方都是 TS 插件，同一 moejs 引擎与注册契约 |
| 可观测 | 日志/诊断点 | 日志 + 审计 + 指标 + `adag doctor` |

---

## 附录 D：术语

- **Core**：Agent 内核进程，负责一轮对话的全部执行。
- **Gateway**：HTTP 入口进程，负责鉴权、SSR、SSE、工作区监督。
- **贡献**：插件在装配期登记的工具、提示词、动作、路由、钩子、插槽、页面。
- **快照/补丁**：Core 事件的两种形态；快照用于首次加载与断序，补丁用于增量。
- **回执**：工具执行的结构化结果，分 `content`（模型）与 `details`（界面）。
- **能力位**：插件清单声明、宿主安装的最小权限集合。
- **装配期**：插件加载并提交贡献的阶段；运行期不可再改变目录。
- **交互请求（Interaction）**：插件经 `ctx.ui` 发起、基座路由到 Gateway 托盘的 confirm/select/input；有 `pending/accepted/denied/cancelled/timeout/unavailable` 六种状态。
- **策略插件**：声明 `role=tool-policy`、订阅 `tool.beforeCall` 并自行决定放行规则的插件；基座不内置审批规则。
- **失败方向**：拿不到判定依据时安全判定往哪边倒；默认关闭。
- **Node 兼容层**：以 `node:*` 名字暴露给插件的 API 子集；不是 Node 运行时，无原生扩展、libuv 与运行时 npm 解析。
- **原生库**：用 Go 实现、按 Node 模块名暴露的内置模块；所有 IO 经宿主回调与能力位，不直接触达系统。
- **设计令牌（`--ph-*`）**：a_da 的 Black Swan 颜色与语义变量，定义在 `design/tokens.css`；ada-go 原样复用，组件只引用变量、不写死色值。
- **`ada-*` 组件库**：a_da 的服务端渲染组件 DOM/class 约定；ada-go 用 Go 组件输出同一结构，插件插槽与页面共享同一套样式。

---

**相邻文档**：[← moejs 与 Node 兼容](12-node-compat.md) · [索引](design.md)
