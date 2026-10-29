// generated from index.ts (sha256:8dfe28200639ba67) by plugins/official/build.mjs — do not edit
// tools-git —— 官方插件：只读的 git 视图（05 §6.3 的 tools-git）。
//
// 三个工具都是 access=exec（跑子进程），但只执行 `git --no-pager <读子命令>`：
// 不提供任何会改动仓库状态的 action（commit/checkout/push 等让模型走 run_command，
// 由审批策略裁决）。边界在这一层只做「模型友好的形状」：
//   - cwd 可选，缺省工作区根；给定时必须在工作区内（本插件先判一次，宿主 fsRealPath 再判一次）；
//   - 每条命令 30s 超时、输出截断 50KB（截断在文本里可见）；
//   - 非零退出码不是脚本异常：git 的"not a repository"这类失败原样回给模型判断。
//
// 命令走 ctx.exec（宿主的 execFile 适配器，无 shell、有审计）。

                      
               
                 
                 
 

                       
                    
                                                                                                           
                                                             
 

                   
               
                      
                     
                                                
                                                    
 

                    
                                   
 

const TIMEOUT_MS = 30_000;
const MAX_BYTES = 50 * 1024;

function fail(code        , message        )        {
  const e = new Error(message);
  (e       ).code = code;
  throw e;
}

function requireExec(ctx             ) {
  if (!ctx.exec) {
    fail("ERR_ACCESS_DENIED", "tools-git 需要能力 exec（清单 backend.capabilities 未声明？）");
  }
  return ctx.exec ;
}

// normalizePath 只做显示层归一（与 tools-fs 同一份语义）；真正的越界拒绝在宿主。
function normalizePath(input         , workspace        )         {
  const raw = String(input ?? "").trim();
  const base = workspace && workspace.length > 0 ? workspace : "/workspace";
  if (raw.length === 0 || raw === "." || raw === "/" ) return base;
  const abs = raw.startsWith("/") ? raw : `${base}/${raw}`;
  const parts           = [];
  for (const seg of abs.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") {
      if (parts.length === 0) fail("ERR_ACCESS_DENIED", `cwd ${raw} 越出工作区`);
      parts.pop();
      continue;
    }
    parts.push(seg);
  }
  const out = "/" + parts.join("/");
  if (out !== base && !out.startsWith(base + "/")) {
    fail("ERR_ACCESS_DENIED", `cwd ${raw} 不在工作区 ${base} 内`);
  }
  return out;
}

// truncate 按字节上限截断（UTF-8 代理对不拆开），并给出可见的省略标记。
function truncate(text        )                                       {
  if (text.length <= MAX_BYTES) return { text, truncated: false };
  let out = text.slice(0, MAX_BYTES);
  // 回退掉截断产生的半个代理对。
  const last = out.charCodeAt(out.length - 1);
  if (last >= 0xd800 && last <= 0xdbff) out = out.slice(0, -1);
  return { text: out + "\n…（输出超过 50KB 已截断）", truncated: true };
}

// runGit 执行一条只读 git 命令并整理输出：非零退出码回文本，不抛错。
async function runGit(ctx             , cwd        , argv          )                                                              {
  const exec = requireExec(ctx);
  let res            ;
  try {
    res = await exec("git", argv, { cwd, timeoutMs: TIMEOUT_MS });
  } catch (e     ) {
    // 启动失败 / 超时 / 被策略拒绝：原样抛出，但点名是哪条命令。
    const code = e && e.code ? e.code : "ERR_CHILD_PROCESS_FAILED";
    const err = new Error(`执行 git ${argv.join(" ")} 失败：${e && e.message ? e.message : String(e)}`);
    (err       ).code = code;
    throw err;
  }
  const out = truncate(res.stdout ?? "");
  const errOut = truncate(res.stderr ?? "");
  const parts           = [];
  if (out.text.trim().length > 0) parts.push(out.text.replace(/\n$/, ""));
  if (errOut.text.trim().length > 0) parts.push("--- stderr ---", errOut.text.replace(/\n$/, ""));
  if (res.code !== 0) parts.push(`(git 退出码 ${res.code})`);
  const text = parts.length > 0 ? parts.join("\n") : "(无输出)";
  return { text, code: res.code, truncated: out.truncated || errOut.truncated };
}

// readCwd 解析并校验 cwd 参数（可选，缺省工作区根）。
function readCwd(args     , ctx             )         {
  if (args?.cwd === undefined || args.cwd === null) return ctx.workspace || "/workspace";
  if (typeof args.cwd !== "string") fail("ERR_INVALID_ARG_TYPE", "cwd 必须是字符串");
  return normalizePath(args.cwd, ctx.workspace);
}

export default function (agent          )       {
  agent.registerTool({
    name: "git_status",
    description:
      "查看工作目录的 git 状态（分支与变更文件）。cwd 可选，缺省工作区根，必须在工作区内。" +
      "不是 git 仓库时返回 git 的错误文本。",
    access: "exec",
    parameters: {
      type: "object",
      properties: {
        cwd: { type: "string", description: "仓库目录（虚拟路径，缺省工作区根）" },
      },
    },
    execute: async (args     , ctx             ) => {
      const cwd = readCwd(args, ctx);
      const res = await runGit(ctx, cwd, ["--no-pager", "status", "--short", "--branch"]);
      ctx.log("info", "git status", { cwd, code: res.code });
      return {
        content: [{ type: "text", text: res.text }],
        details: { subcommand: "status", cwd, code: res.code, truncated: res.truncated },
      };
    },
  });

  agent.registerTool({
    name: "git_diff",
    description:
      "查看未提交改动的 diff（staged 为 true 时看已暂存的改动）。输出上限 50KB；" +
      "先看 git_status 决定要不要读 diff。",
    access: "exec",
    parameters: {
      type: "object",
      properties: {
        cwd: { type: "string", description: "仓库目录（虚拟路径，缺省工作区根）" },
        staged: { type: "boolean", description: "true 时查看已暂存（--cached）的改动" },
      },
    },
    execute: async (args     , ctx             ) => {
      const cwd = readCwd(args, ctx);
      const argv = ["--no-pager", "diff"];
      if (args?.staged === true) argv.push("--cached");
      const res = await runGit(ctx, cwd, argv);
      ctx.log("info", "git diff", { cwd, staged: args?.staged === true, code: res.code });
      return {
        content: [{ type: "text", text: res.text }],
        details: { subcommand: "diff", cwd, staged: args?.staged === true, code: res.code, truncated: res.truncated },
      };
    },
  });

  agent.registerTool({
    name: "git_log",
    description:
      "查看提交历史（一行一条：哈希、作者、日期、标题）。maxCount 缺省 20，上限 200。",
    access: "exec",
    parameters: {
      type: "object",
      properties: {
        cwd: { type: "string", description: "仓库目录（虚拟路径，缺省工作区根）" },
        maxCount: { type: "number", description: "最多返回多少条（缺省 20，上限 200）" },
      },
    },
    execute: async (args     , ctx             ) => {
      const cwd = readCwd(args, ctx);
      let maxCount = args?.maxCount === undefined ? 20 : Math.trunc(Number(args.maxCount)) || 0;
      if (maxCount <= 0) maxCount = 20;
      if (maxCount > 200) maxCount = 200;
      const res = await runGit(ctx, cwd, [
        "--no-pager", "log", `--max-count=${maxCount}`,
        "--pretty=format:%h %an %ad %s", "--date=short",
      ]);
      ctx.log("info", "git log", { cwd, maxCount, code: res.code });
      return {
        content: [{ type: "text", text: res.text }],
        details: { subcommand: "log", cwd, maxCount, code: res.code, truncated: res.truncated },
      };
    },
  });
}
