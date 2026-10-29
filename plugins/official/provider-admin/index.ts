// provider-admin 后端：供应商配置的增删改查、模型拉取与当前模型切换。
//
// 数据面经宿主能力 model.admin 提供（ctx.providers）：
//   - list()         所有配置（含停用项），API Key 只返回 hasApiKey/尾号；
//   - save(input)    新建/更新供应商与模型清单；apiKey 留空=保持原值；
//   - remove(id)     删除供应商；
//   - fetchModels(id) 服务端用已保存端点/密钥拉取 /models；
//   - select()       切换当前模型。
// 页面只调用本插件的 route，不直接读 Core RPC；密钥不经过浏览器。

interface ProviderModel {
  id: string;
  displayName?: string;
  contextWindow?: number;
  maxOutput?: number;
  input?: string[];
  supportsTools: boolean;
  supportsThinking?: boolean;
  disabled?: boolean;
}

interface ProviderInfo {
  id: string;
  displayName?: string;
  protocol?: string;
  baseUrl?: string;
  disabled?: boolean;
  hasApiKey: boolean;
  apiKeyHint?: string;
  models: ProviderModel[];
}

interface ProviderCatalog {
  providers: ProviderInfo[];
  current: { providerId: string; modelId: string };
}

interface RouteRequest {
  method: "GET" | "POST";
  path: string;
  query: Record<string, string>;
  body?: unknown;
}

interface ProviderInput {
  id?: string;
  name?: string;
  protocol?: string;
  baseUrl?: string;
  apiKey?: string;
  clearApiKey?: boolean;
  disabled?: boolean;
  models?: ProviderModel[];
}

interface RouteContext {
  providers: {
    list(): Promise<ProviderCatalog>;
    save(input: ProviderInput): Promise<ProviderInfo>;
    remove(providerId: string): Promise<{ removed: boolean }>;
    fetchModels(providerId: string): Promise<ProviderModel[]>;
    select(providerId: string, modelId: string): Promise<{ providerId: string; modelId: string }>;
  };
}

interface AgentAPI {
  route(
    method: "GET" | "POST",
    path: string,
    handler: (req: RouteRequest, ctx: RouteContext) => unknown,
  ): void;
}

function bodyOf(req: RouteRequest): Record<string, unknown> {
  const body = req.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw new Error("请求体必须是 JSON 对象");
  }
  return body as Record<string, unknown>;
}

function idOf(body: Record<string, unknown>): string {
  const id = String(body.id || "").trim();
  if (!id) throw new Error("缺少供应商 id");
  return id;
}

export default function (agent: AgentAPI): void {
  agent.route("GET", "/state", (_req, ctx) => ctx.providers.list());

  agent.route("POST", "/save", (req, ctx) => {
    const body = bodyOf(req);
    const input: ProviderInput = {
      id: String(body.id || "").trim(),
      name: String(body.name || "").trim(),
      protocol: String(body.protocol || "").trim(),
      baseUrl: String(body.baseUrl || "").trim(),
      apiKey: body.apiKey === undefined ? "" : String(body.apiKey),
      clearApiKey: body.clearApiKey === true,
      disabled: body.disabled === true,
      models: Array.isArray(body.models) ? (body.models as ProviderModel[]) : [],
    };
    return ctx.providers.save(input);
  });

  agent.route("POST", "/remove", (req, ctx) => {
    return ctx.providers.remove(idOf(bodyOf(req)));
  });

  agent.route("POST", "/fetch-models", (req, ctx) => {
    return ctx.providers.fetchModels(idOf(bodyOf(req)));
  });

  agent.route("POST", "/select", (req, ctx) => {
    const body = bodyOf(req);
    const providerId = String(body.providerId || "").trim();
    const modelId = String(body.modelId || "").trim();
    if (!providerId || !modelId) {
      throw new Error("需要 providerId 与 modelId");
    }
    return ctx.providers.select(providerId, modelId);
  });
}
