// plugin-helper 后端：插件助手——安装与健康检查。
//
//   - route POST /create：向导页面（表单），AI 不参与；
//   - 工具 create_plugin：模型在对话里被要求“创建一个 XXX 插件”时调用，
//     入口源码由模型生成后作为参数传入；
//   - 工具 check_plugin：检查任意插件的发现位置/启停/装配状态与错误详情，
//     id 留空时返回全部插件摘要。
//
// 生产环境的插件统一存放在用户目录（~/.adago/plugins），跨工作区共享；写入
// 走宿主 plugin.admin 能力桥（ctx.pluginAdmin.install / .check），id/路径/大小
// 校验与「不覆盖」由宿主收口——插件脚本拿不到任意文件写权限。第三方插件的
// 入口由宿主直接执行，不做 TypeScript 编译（05 §6.1），生成的 index.ts 内容是
// 纯 JavaScript（与 plugins/hello-echo 同形）。

interface PluginAdminBridge {
  install(spec: { id: string; files: { path: string; content: string }[] }): Promise<{
    id: string;
    dir: string;
    files: string[];
  }>;
  remove(pluginId: string): Promise<{ id: string; dir: string; source: string }>;
  check(pluginId: string): Promise<{
    plugins?: { id: string; source?: string; status: string; enabled: boolean; lastError?: string }[];
    report?: {
      id: string;
      found: boolean;
      name?: string;
      version?: string;
      trust?: string;
      source?: string;
      dir?: string;
      entry?: string;
      capabilities?: string[];
      enabled: boolean;
      status?: string;
      lastError?: string;
      tools?: string[];
      files?: string[];
      checks?: { name: string; ok: boolean; detail?: string }[];
    };
  }>;
}

interface RouteRequest {
  method: "GET" | "POST";
  path: string;
  query: Record<string, string>;
  body?: unknown;
}

interface CommonContext {
  workspace?: string;
  pluginAdmin?: PluginAdminBridge;
  log: (level: string, msg: string, fields?: object) => void;
}

type RouteContext = CommonContext;
type ToolContext = CommonContext;

interface AgentAPI {
  route(
    method: "GET" | "POST",
    path: string,
    handler: (req: RouteRequest, ctx: RouteContext) => unknown,
  ): void;
  registerTool(def: {
    name: string;
    description: string;
    parameters: object;
    access: "read" | "write" | "exec" | "network";
    execute: (args: any, ctx: ToolContext) => unknown;
  }): void;
}

interface ScaffoldFile {
  path: string;
  content: string;
}

interface ScaffoldInput {
  id: string;
  name: string;
  description: string;
  toolName: string;
  entry: string;
  // generated=true 表示入口是回显示例（AI 没有提供源码），README 措辞随之变化。
  generated: boolean;
  capabilities: string[];
}

const ID_RE = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;
const TOOL_RE = /^[a-z][a-z0-9_]*$/;
// 展示用的插件根：实际落盘由宿主决定（全局 ~/.adago/plugins）。
const PLUGIN_ROOT_LABEL = "~/.adago/plugins";
const MAX_ENTRY_BYTES = 256 << 10;
// 第三方信任级别可用的能力位；net.sse/exec 只给 builtin/verified，不接受。
const ALLOWED_CAPABILITIES = ["fs.read", "fs.write", "state", "net.fetch"];

function fail(message: string): never {
  const err = new Error(message);
  (err as any).code = "ERR_INVALID_ARG";
  throw err;
}

function text(value: unknown, max: number): string {
  return String(value == null ? "" : value).trim().slice(0, max);
}

// 名称/描述会进生成代码的注释；换行会把注释变成可执行代码，先压成一行。
function commentText(value: string): string {
  return value.replace(/[\r\n]+/g, " ");
}

function bodyOf(req: RouteRequest): Record<string, unknown> {
  const body = req.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    fail("请求体必须是 JSON 对象");
  }
  return body as Record<string, unknown>;
}

function requirePluginAdmin(ctx: CommonContext): PluginAdminBridge {
  if (!ctx.pluginAdmin) fail("安装插件需要 plugin.admin 能力（宿主未授予全局安装桥）");
  return ctx.pluginAdmin as PluginAdminBridge;
}

function normalizeID(raw: unknown): string {
  const id = String(raw == null ? "" : raw).trim().toLowerCase();
  if (id.length < 3 || id.length > 64 || !ID_RE.test(id)) {
    fail("插件 ID 需要 3-64 位小写字母、数字或连字符，且以字母开头");
  }
  return id;
}

function normalizeTool(raw: unknown, id: string): string {
  const fallback = `${id.replace(/-/g, "_")}_hello`;
  const name = text(raw, 64) || fallback;
  if (!TOOL_RE.test(name)) {
    fail("工具名需要小写字母、数字或下划线，且以字母开头");
  }
  return name;
}

function normalizeCapabilities(raw: unknown): string[] {
  if (raw == null) return [];
  if (!Array.isArray(raw)) fail("capabilities 必须是字符串数组");
  const out: string[] = [];
  for (const item of raw) {
    const cap = text(item, 32);
    if (!cap) continue;
    if (ALLOWED_CAPABILITIES.indexOf(cap) === -1) {
      fail(`能力 ${cap} 不受第三方插件支持；可用：${ALLOWED_CAPABILITIES.join(", ")}`);
    }
    if (out.indexOf(cap) === -1) out.push(cap);
  }
  return out;
}

// normalizeEntry 校验模型生成的入口源码：必须是带默认导出注册函数的 ESM。
// 语法/能力层面的约束由宿主在加载时执行，这里只挡明显不合法的输入，
// 并把最常见的生成错误（MCP 风格的 inputSchema/run 字段）用精确文案提前拦下。
function normalizeEntry(raw: unknown, toolName: string): { source: string; generated: boolean } {
  const source = String(raw == null ? "" : raw);
  if (!source.trim()) {
    return { source: "", generated: true };
  }
  if (source.length > MAX_ENTRY_BYTES) {
    fail(`入口源码 ${source.length} 字节，超过上限 ${MAX_ENTRY_BYTES}`);
  }
  if (source.indexOf("export default") === -1) {
    fail("入口源码必须包含 export default function register(agent)（宿主以默认导出为注册入口）");
  }
  if (source.indexOf("registerTool") !== -1 &&
      source.indexOf("execute") === -1 && source.indexOf("handler") === -1) {
    fail(
      "工具定义缺少可调用的 execute(args, ctx) 成员：宿主只认 parameters + execute，" +
      "不认 MCP 风格的 inputSchema/run。正确形状：" +
      'agent.registerTool({ name, description, parameters: {...}, access: "read", ' +
      "async execute(args, ctx) { return { content: [{ type: \"text\", text: \"...\" }] }; } })",
    );
  }
  if (source.indexOf(toolName) === -1) {
    fail(`入口源码没有注册声明的工具 ${toolName}（agent.registerTool 的 name 必须一致）`);
  }
  return { source, generated: false };
}

function manifestOf(id: string, name: string, description: string, capabilities: string[]): string {
  return (
    JSON.stringify(
      {
        id,
        name,
        version: "0.1.0",
        apiVersion: 1,
        description,
        trust: "third-party",
        backend: { entry: "index.ts", capabilities },
      },
      null,
      2,
    ) + "\n"
  );
}

function entryOf(name: string, description: string, toolName: string): string {
  const toolDesc = "示例工具：原样回显 text 参数；可以改成你自己的实现。";
  return `// ${commentText(name)} —— 由 plugin-helper 生成。
//
// 入口直接由宿主执行，不经过 TypeScript 编译；可以写 TS 类型注解，但不要使用
// 需要运行时的 TS 特性（enum / namespace / import =）。能力清单在 plugin.json。

export default function register(agent) {
  agent.registerTool({
    name: ${JSON.stringify(toolName)},
    description: ${JSON.stringify(toolDesc)},
    parameters: {
      type: "object",
      properties: {
        text: { type: "string", description: "要回显的文本" },
      },
      required: ["text"],
      additionalProperties: false,
    },
    access: "read",
    async execute(args, ctx) {
      const text = args && typeof args.text === "string" ? args.text : "";
      ctx.log("info", ${JSON.stringify(toolName + " 被调用")}, { length: text.length });
      return {
        content: [{ type: "text", text: text }],
      };
    },
  });
}

// ${commentText(description)}
`;
}

function readmeOf(
  id: string,
  name: string,
  description: string,
  toolName: string,
  generated: boolean,
  capabilities: string[],
): string {
  const toolIntro = generated
    ? `示例工具 \`${toolName}\` 会原样返回 \`text\` 参数。修改 \`index.ts\` 后，在插件页点\n“重载”或重新打开应用即可生效。`
    : `工具 \`${toolName}\` 的实现见 \`index.ts\`（由 AI 生成）；在插件页点“重载”或重新\n打开应用即可让改动生效。`;
  const capIntro =
    capabilities.length === 0
      ? "当前没有声明任何能力；需要文件、状态、网络时，在 `plugin.json` 的\n`backend.capabilities` 里声明。"
      : `已声明能力：${capabilities.map((cap) => `\`${cap}\``).join("、")}；按第三方信任级别受限。`;
  return `# ${name}

${description}

## 文件

- \`plugin.json\`：插件清单（id、信任级别、后端入口与能力）
- \`index.ts\`：后端入口，内容是可直接执行的 JavaScript
- \`README.md\`：本说明

## 工具

${toolIntro}

## 开发约束

- 第三方插件的入口必须是宿主可执行的 ESM JavaScript，不做运行时 TypeScript/npm 编译。
- ${capIntro}
- 插件默认未启用，请在插件列表或插件详情页手动启用。
- 前端段（页面/插槽）目前仅随包官方插件渲染；工作区插件只加载后端段。
- 平台插件 API 的完整规范见 plugin-helper 插件目录的 \`SPEC.md\`。
`;
}

function scaffold(input: ScaffoldInput): ScaffoldFile[] {
  const entry = input.generated
    ? entryOf(input.name, input.description, input.toolName)
    : input.entry;
  // 路径相对插件目录；落盘位置（全局 ~/.adago/plugins/<id>/）由宿主安装桥决定。
  return [
    {
      path: "plugin.json",
      content: manifestOf(input.id, input.name, input.description, input.capabilities),
    },
    { path: "index.ts", content: entry },
    {
      path: "README.md",
      content: readmeOf(
        input.id,
        input.name,
        input.description,
        input.toolName,
        input.generated,
        input.capabilities,
      ),
    },
  ];
}

async function createPlugin(ctx: CommonContext, raw: Record<string, unknown>) {
  const pluginAdmin = requirePluginAdmin(ctx);
  const id = normalizeID(raw.id);
  const name = text(raw.name, 80) || id;
  const description = text(raw.description, 500) || `${name} 插件`;
  // 工具参数用 tool_name（工具 schema 惯例）；向导 route 的表单用 toolName。
  const toolRaw = raw.tool_name !== undefined ? raw.tool_name : raw.toolName;
  const toolName = normalizeTool(toolRaw, id);
  const entry = normalizeEntry(raw.entry, toolName);
  const capabilities = normalizeCapabilities(raw.capabilities);
  const files = scaffold({
    id,
    name,
    description,
    toolName,
    entry: entry.source,
    generated: entry.generated,
    capabilities,
  });
  const result = await pluginAdmin.install({ id, files });
  ctx.log("info", "已安装插件骨架到全局插件根", {
    id, dir: result.dir, files: result.files.length, generated: entry.generated, capabilities,
  });
  return {
    id, name, toolName, dir: result.dir, files: result.files,
    generated: entry.generated, capabilities,
  };
}

function createdText(result: {
  id: string;
  name: string;
  toolName: string;
  dir: string;
  files: ScaffoldFile[];
  generated: boolean;
  capabilities: string[];
}): string {
  const lines = [
    `已创建插件 ${result.name}（id=${result.id}）：`,
    `- ${result.dir}`,
    ...result.files.map((file) => `- ${file.path}`),
  ];
  if (result.generated) {
    lines.push(`已生成回显示例工具 ${result.toolName}；可在 index.ts 里改成实际实现。`);
  } else {
    lines.push(`入口已按提供的源码写入；主工具为 ${result.toolName}。`);
  }
  lines.push(
    "",
    "下一步：",
    "1. 在插件列表或本插件详情页点“重载”，让 Core 发现并加载它；",
    "2. 新插件默认未启用，加载后在插件页点“启用”；",
  );
  if (result.capabilities.length > 0) {
    lines.push(`3. 已声明能力 ${result.capabilities.join(", ")}，按第三方信任级别受限；工具调用会经过审批策略。`);
  } else {
    lines.push("3. 当前未声明任何能力；需要文件/状态/网络时编辑 plugin.json 的 backend.capabilities 后重载。");
  }
  return lines.join("\n");
}

export default function (agent: AgentAPI): void {
  agent.route("GET", "/info", (_req, ctx) => {
    return {
      workspace: ctx.workspace || "/workspace",
      pluginRoot: PLUGIN_ROOT_LABEL,
      files: ["plugin.json", "index.ts", "README.md"],
    };
  });

  agent.route("POST", "/create", async (req, ctx) => {
    const result = await createPlugin(ctx, bodyOf(req));
    return {
      id: result.id,
      name: result.name,
      toolName: result.toolName,
      path: result.dir,
      files: result.files,
      reloadRequired: true,
    };
  });

  // 健康检查 route：向导页/测试用；AI 侧等价能力是 check_plugin 工具。
  // 注意：无 body 的调用里 req 是 undefined（callArgs 只在有载荷时给第一参）。
  agent.route("POST", "/check-plugin", async (req, ctx) => {
    if (!ctx.pluginAdmin) fail("检查插件需要 plugin.admin 能力（宿主未授予诊断桥）");
    const r = (req || {}) as { body?: unknown };
    const body = r.body && typeof r.body === "object" && !Array.isArray(r.body)
      ? (r.body as Record<string, unknown>)
      : {};
    return ctx.pluginAdmin.check(String(body.id || ""));
  });

  // 移除 route：与 remove_plugin 工具同义（页面/测试用）。
  agent.route("POST", "/remove-plugin", async (req, ctx) => {
    if (!ctx.pluginAdmin) fail("移除插件需要 plugin.admin 能力（宿主未授予安装桥）");
    const r = (req || {}) as { body?: unknown };
    const body = r.body && typeof r.body === "object" && !Array.isArray(r.body)
      ? (r.body as Record<string, unknown>)
      : {};
    return ctx.pluginAdmin.remove(String(body.id || ""));
  });

  // AI 创建插件：用户在对话里说“创建一个 XXX 插件”，模型生成入口源码后调用本工具。
  // access=write：落盘要过审批策略；生成代码的执行边界由第三方信任级别与能力清单约束。
  agent.registerTool({
    name: "create_plugin",
    description:
      "在工作区无关的全局插件根（~/.adago/plugins）创建一个可直接加载的第三方插件。" +
      "先与用户确认插件 id（小写字母/数字/连字符），再自己编写入口源码。入口是 ESM JavaScript，" +
      "必须 export default function register(agent)；每个工具的形状是 " +
      "agent.registerTool({ name, description, parameters: {...JSON Schema...}, access: \"read\"|\"write\", " +
      "async execute(args, ctx) { return { content: [{ type: \"text\", text: \"...\" }] }; } })。" +
      "字段名固定为 parameters 与 execute（不是 inputSchema / run / handler 风格），execute 返回 " +
      "{ content: [...] }。ctx.state.get/set 返回 Promise，要 async/await。入口由宿主直接执行，" +
      "不做 TypeScript/npm 编译。entry 留空时生成回显示例工具。",
    parameters: {
      type: "object",
      properties: {
        id: { type: "string", description: "插件 id：3-64 位小写字母、数字或连字符，以字母开头" },
        name: { type: "string", description: "显示名称；默认用 id" },
        description: { type: "string", description: "一句话描述插件用途" },
        entry: {
          type: "string",
          description:
            "index.ts 的 ESM JavaScript 源码；必须包含 export default function register(agent)，" +
            "并按 registerTool({ name, description, parameters, access, execute }) 的形状注册名为 tool_name 的工具；" +
            "execute 返回 { content: [{ type: \"text\", text }] }。留空生成回显示例。",
        },
        tool_name: { type: "string", description: "示例/主工具名；留空用 <id>_hello" },
        capabilities: {
          type: "array",
          items: { type: "string", enum: ALLOWED_CAPABILITIES },
          description: "backend.capabilities；默认空。第三方插件只支持这些能力位。",
        },
      },
      required: ["id"],
      additionalProperties: false,
    },
    access: "write",
    async execute(args, ctx) {
      const input = args && typeof args === "object" && !Array.isArray(args) ? args : {};
      const result = await createPlugin(ctx, input as Record<string, unknown>);
      return {
        content: [{ type: "text", text: createdText(result) }],
      };
    },
  });

  // 插件健康检查：发现位置/清单/启停决策来源/装配状态/入口预加载。
  // access=read：只读诊断；id 留空返回全部插件摘要。
  agent.registerTool({
    name: "check_plugin",
    description:
      "检查插件的健康状态并给出诊断报告：发现位置（随包/全局/工作区）、清单字段、" +
      "启停决策来源（全局 plugins.json / 工作区 .adag/plugins.json / 产品声明）、" +
      "装配状态与 last_error，并对未停用的落盘插件做入口预加载校验。" +
      "用户说「插件不工作/加载失败/检查一下插件」或报告插件异常时调用；" +
      "id 留空时返回全部插件的摘要列表。",
    parameters: {
      type: "object",
      properties: {
        id: { type: "string", description: "要检查的插件 id；留空返回全部插件摘要" },
      },
      required: [],
      additionalProperties: false,
    },
    access: "read",
    async execute(args, ctx) {
      const input = args && typeof args === "object" && !Array.isArray(args) ? args : {};
      const id = String((input as Record<string, unknown>).id || "").trim();
      const result = await requirePluginAdmin(ctx).check(id);
      return { content: [{ type: "text", text: checkText(result) }] };
    },
  });

  // 移除已安装的插件：删掉发现优先级最高的副本（工作区旧目录优先于全局根）。
  // access=write：删除要过审批策略；随包插件在宿主侧拒绝。移除后需重载生效。
  agent.registerTool({
    name: "remove_plugin",
    description:
      "移除一个已安装的第三方插件（删除其插件目录，工作区副本优先于全局副本）。" +
      "移除后在插件页点「重载」让它从工具目录消失。随包插件不能移除；" +
      "移除不会影响正在运行的轮次，已加载的代服务到重载为止。",
    parameters: {
      type: "object",
      properties: {
        id: { type: "string", description: "要移除的插件 id" },
      },
      required: ["id"],
      additionalProperties: false,
    },
    access: "write",
    async execute(args, ctx) {
      const input = args && typeof args === "object" && !Array.isArray(args) ? args : {};
      const id = String((input as Record<string, unknown>).id || "").trim();
      if (!id) fail("缺少插件 id");
      const result = await requirePluginAdmin(ctx).remove(id);
      return {
        content: [{
          type: "text",
          text: "已移除插件 " + result.id + "（来源 " + result.source + "：" + result.dir + "）。" +
            "下一步：在插件列表/详情页点「重载」，它会从工具目录消失。",
        }],
      };
    },
  });
}

function checkText(result: {
  plugins?: { id: string; source?: string; status: string; enabled: boolean; lastError?: string }[];
  report?: {
    id: string;
    found: boolean;
    name?: string;
    version?: string;
    trust?: string;
    source?: string;
    dir?: string;
    entry?: string;
    capabilities?: string[];
    enabled: boolean;
    status?: string;
    lastError?: string;
    tools?: string[];
    files?: string[];
    checks?: { name: string; ok: boolean; detail?: string }[];
  };
}): string {
  if (result.plugins) {
    const lines = ["已装配插件 " + result.plugins.length + " 个："];
    for (const p of result.plugins) {
      const state = p.enabled ? "启用" : "停用";
      const err = p.lastError ? "（" + p.lastError + "）" : "";
      lines.push("- " + p.id + " [" + state + "/" + p.status + "] " + err);
    }
    lines.push("", "要查看某个插件的完整诊断，带上 id 再调一次 check_plugin。");
    return lines.join("\n");
  }
  const r = result.report;
  if (!r) return "没有检查结果。";
  if (!r.found) {
    const fail = (r.checks || []).find((c) => !c.ok);
    return "插件 " + r.id + " 未发现。" + (fail && fail.detail ? "：" + fail.detail : "");
  }
  const lines = [
    "插件 " + (r.name || r.id) + "（id=" + r.id + "）诊断报告：",
    "- 位置：" + (r.source || "未知") + (r.dir ? "（" + r.dir + "）" : ""),
    "- 清单：v" + (r.version || "?") + " trust=" + (r.trust || "?") +
      " entry=" + (r.entry || "?") + " capabilities=[" + (r.capabilities || []).join(", ") + "]",
    "- 启停：" + (r.enabled ? "启用" : "停用"),
    "- 装配状态：" + (r.status || "?") + (r.lastError ? "（" + r.lastError + "）" : ""),
    "- 工具：" + ((r.tools || []).length ? (r.tools || []).join(", ") : "无"),
  ];
  if (r.checks && r.checks.length) {
    lines.push("", "检查项：");
    for (const c of r.checks) {
      lines.push((c.ok ? "[OK] " : "[失败] ") + c.name + (c.detail ? "：" + c.detail : ""));
    }
  }
  if (r.status !== "ready") {
    lines.push("", "建议：在插件列表/详情页点「重载」后再检查；仍失败时把上面的失败项发给用户。");
  }
  return lines.join("\n");
}
