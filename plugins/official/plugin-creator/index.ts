// plugin-creator 后端：在工作区 .adago/plugins/<id>/ 下生成第三方插件骨架。
//
// 第三方插件的入口由宿主直接执行，不做 TypeScript 编译（05 §6.1），所以生成的
// index.ts 内容是纯 JavaScript（与 plugins/hello-echo 同形）。写入只使用宿主
// fs 能力，路径固定在工作区插件根下，不接触插件目录之外的任意路径。

interface FsBridge {
  exists(path: string): Promise<boolean>;
  writeText(path: string, text: string): Promise<{ bytes: number }>;
}

interface RouteRequest {
  method: "GET" | "POST";
  path: string;
  query: Record<string, string>;
  body?: unknown;
}

interface RouteContext {
  workspace?: string;
  fs?: FsBridge;
  log: (level: string, msg: string, fields?: object) => void;
}

interface AgentAPI {
  route(
    method: "GET" | "POST",
    path: string,
    handler: (req: RouteRequest, ctx: RouteContext) => unknown,
  ): void;
}

interface ScaffoldFile {
  path: string;
  content: string;
}

const ID_RE = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;
const TOOL_RE = /^[a-z][a-z0-9_]*$/;
const PLUGIN_ROOT = ".adago/plugins";

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

function requireFs(ctx: RouteContext): FsBridge {
  if (!ctx.fs) fail("创建插件需要工作区文件写入能力");
  return ctx.fs as FsBridge;
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

function manifestOf(id: string, name: string, description: string): string {
  return (
    JSON.stringify(
      {
        id,
        name,
        version: "0.1.0",
        apiVersion: 1,
        description,
        trust: "third-party",
        backend: { entry: "index.ts", capabilities: [] },
      },
      null,
      2,
    ) + "\n"
  );
}

function entryOf(name: string, description: string, toolName: string): string {
  const toolDesc = "示例工具：原样回显 text 参数；可以改成你自己的实现。";
  return `// ${commentText(name)} —— 由 plugin-creator 生成。
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

function readmeOf(id: string, name: string, description: string, toolName: string): string {
  return `# ${name}

${description}

## 文件

- \`plugin.json\`：插件清单（id、信任级别、后端入口与能力）
- \`index.ts\`：后端入口，内容是可直接执行的 JavaScript
- \`README.md\`：本说明

## 工具

示例工具 \`${toolName}\` 会原样返回 \`text\` 参数。修改 \`index.ts\` 后，在插件页点
“重载”或重新打开应用即可生效。

## 开发约束

- 第三方插件的入口必须是宿主可执行的 ESM JavaScript，不做运行时 TypeScript/npm 编译。
- 需要文件、状态、网络等能力时，在 \`plugin.json\` 的 \`backend.capabilities\` 里声明；
  能力会按第三方信任级别受限。
- 插件默认未启用，请在插件列表或插件详情页手动启用。
`;
}

function scaffold(id: string, name: string, description: string, toolName: string): ScaffoldFile[] {
  return [
    { path: `${PLUGIN_ROOT}/${id}/plugin.json`, content: manifestOf(id, name, description) },
    { path: `${PLUGIN_ROOT}/${id}/index.ts`, content: entryOf(name, description, toolName) },
    { path: `${PLUGIN_ROOT}/${id}/README.md`, content: readmeOf(id, name, description, toolName) },
  ];
}

export default function (agent: AgentAPI): void {
  agent.route("GET", "/info", (_req, ctx) => {
    return {
      workspace: ctx.workspace || "/workspace",
      pluginRoot: PLUGIN_ROOT,
      files: ["plugin.json", "index.ts", "README.md"],
    };
  });

  agent.route("POST", "/create", async (req, ctx) => {
    const fs = requireFs(ctx);
    const body = bodyOf(req);
    const id = normalizeID(body.id);
    const name = text(body.name, 80) || id;
    const description = text(body.description, 500) || `${name} 插件`;
    const toolName = normalizeTool(body.toolName, id);
    const dir = `${PLUGIN_ROOT}/${id}`;
    if (await fs.exists(dir)) {
      fail(`插件 ${id} 已存在（${dir}）`);
    }
    const files = scaffold(id, name, description, toolName);
    for (const file of files) {
      await fs.writeText(file.path, file.content);
    }
    ctx.log("info", "已创建插件骨架", { id, dir, files: files.length });
    return {
      id,
      name,
      toolName,
      path: dir,
      files: files.map((file) => file.path.slice(dir.length + 1)),
      reloadRequired: true,
    };
  });
}
