// env-manager 后端：环境变量管理页的数据面。
//
// 数据面：
//   - 变量存 global.config 的 env 命名空间：
//       ctx.config           机器级（$ADAGO_HOME/config/env.json）
//       ctx.config.workspace 工作区级（<workspace>/.adago/config/env.json）
//   - 服务程序目录由插件用 ctx.fs 投影；变量校验、读写、合并全部在本插件。
// 页面通过本插件 route 调用，写操作受 CSRF 约束。

function bodyOf(req) {
  const body = req.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw new Error("请求体必须是 JSON 对象");
  }
  return body;
}

function scopeOf(body) {
  const scope = String(body.scope || "").trim();
  if (scope !== "global" && scope !== "workspace") {
    throw new Error(`scope 只能是 global 或 workspace（收到 ${JSON.stringify(scope)}）`);
  }
  return scope;
}

function keyOf(body) {
  const key = String(body.key || "").trim();
  if (!key) throw new Error("需要变量名 key");
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
    throw new Error("变量名必须匹配 ^[A-Za-z_][A-Za-z0-9_]*$");
  }
  if (/^ADAG_/i.test(key)) throw new Error("ADAG_ 是宿主保留前缀");
  return key;
}

function valueOf(body) {
  const value = body.value === undefined || body.value === null ? "" : String(body.value);
  if (value.length > 32 * 1024) throw new Error("变量值超过 32KB");
  if (value.includes("\0")) throw new Error("变量值不能包含 NUL");
  return value;
}

async function varsOf(scope, ctx) {
  const store = scope === "workspace" ? ctx.config.workspace : ctx.config;
  const raw = await store.get("vars");
  return raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {};
}

async function saveVars(scope, ctx, vars) {
  const store = scope === "workspace" ? ctx.config.workspace : ctx.config;
  await store.set("vars", vars);
}


async function binsOf(ctx) {
  const globalConfig = ctx.config.path();
  const adagoHome = globalConfig.replace(/[\\/]config[\\/][^\\/]+$/, "");
  const wsConfig = String(ctx.config.workspace.path() || "");
  const wsRoot = wsConfig.replace(/[\\/]\.adago[\\/]config[\\/][^\\/]+$/, "");
  const sep = wsRoot.includes("\\") ? "\\" : "/";
  const wsBin = wsRoot.replace(/[\\/]+$/, "") + sep + ".adago" + sep + "bin";
  const gsep = adagoHome.includes("\\") ? "\\" : "/";
  const globalBin = adagoHome.replace(/[\\/]+$/, "") + gsep + "bin";
  const paths = [];
  const programs = [];
  for (const item of [
    { scope: "workspace", path: wsBin },
    { scope: "global", path: globalBin },
  ]) {
    let exists = false;
    try {
      const entries = await ctx.fs.list(item.path);
      exists = true;
      for (const e of entries || []) {
        if (e.dir) continue;
        programs.push({ scope: item.scope, name: e.name, size: e.size, modified: "" });
      }
    } catch {
      exists = false;
    }
    paths.push({ scope: item.scope, path: item.path, exists });
  }
  return { paths, programs };
}

export default function (agent) {
  agent.route("GET", "/state", async (_req, ctx) => {
    const g = await varsOf("global", ctx);
    const w = await varsOf("workspace", ctx);
    return {
      global: { path: ctx.config.path(), vars: g },
      workspace: { path: ctx.config.workspace.path(), vars: w },
      merged: Object.assign({}, g, w),
      warnings: [],
      bins: await binsOf(ctx),
    };
  });

  // 只读动作 state：供页面 SSR 预取，首屏/无 JS 也能看到两层变量与服务程序目录。
  agent.registerAction({
    name: "state",
    mode: "read",
    execute: async (_input, ctx) => {
      const g = await varsOf("global", ctx);
      const w = await varsOf("workspace", ctx);
      return {
        global: { path: ctx.config.path(), vars: g },
        workspace: { path: ctx.config.workspace.path(), vars: w },
        merged: Object.assign({}, g, w),
        warnings: [],
        bins: await binsOf(ctx),
      };
    },
  });

  agent.route("POST", "/set", async (req, ctx) => {
    const body = bodyOf(req);
    const scope = scopeOf(body);
    const key = keyOf(body);
    const value = valueOf(body);
    const vars = await varsOf(scope, ctx);
    vars[key] = value;
    await saveVars(scope, ctx, vars);
    const store = scope === "workspace" ? ctx.config.workspace : ctx.config;
    return { scope, path: store.path(), key, changed: true };
  });

  agent.route("POST", "/remove", async (req, ctx) => {
    const body = bodyOf(req);
    const scope = scopeOf(body);
    const key = keyOf(body);
    const vars = await varsOf(scope, ctx);
    const removed = Object.prototype.hasOwnProperty.call(vars, key);
    delete vars[key];
    await saveVars(scope, ctx, vars);
    const store = scope === "workspace" ? ctx.config.workspace : ctx.config;
    return { scope, path: store.path(), key, removed };
  });
}

