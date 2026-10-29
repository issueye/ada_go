// generated from index.ts (sha256:b7376a47cb2f49e4) by plugins/official/build.mjs — do not edit
// tools-shell —— 官方插件：执行命令（05 §6.3、12 §6.13）。
//
// 只有 execFile 语义：命令与参数分开传，永远不经过 shell（没有管道、通配、变量展开）。
// cwd 只能在工作区内、env 只注入清单白名单、超时与 maxBuffer 由宿主兜底；
// 本插件只负责「把模型给的结构化参数翻译成一次受控调用，并把输出整理成可读文本」。
//
// 类型形状与 agent:api 一致，本地声明避免官方插件依赖 sdk 的类型包。

                      
               
                 
                 
 

                       
                                                                                                           
                                                             
 

                   
               
                      
                     
                                                
                                                    
 

                    
                                   
 

const MAX_BYTES = 50 * 1024;
const MAX_LINES = 2000;
const MAX_TIMEOUT_MS = 600000;

function fail(code        , message        )        {
  const e = new Error(message);
  (e       ).code = code;
  throw e;
}

// truncate 按「行数」与「字节数」两个上限截断，并给出可读的省略说明。
function truncate(text        )                                       {
  const lines = text.split("\n");
  let truncated = false;
  let out = lines;
  if (lines.length > MAX_LINES) {
    out = lines.slice(0, MAX_LINES);
    truncated = true;
  }
  let joined = out.join("\n");
  if (joined.length > MAX_BYTES) {
    joined = joined.slice(0, MAX_BYTES);
    truncated = true;
  }
  return { text: joined, truncated };
}

function byteLength(s        )         {
  // 只用于回执统计：UTF-8 下代理对与多字节字符按实际长度算。
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff) {
      n += 4;
      i++;
    } else n += 3;
  }
  return n;
}

function requireExec(ctx             ) {
  if (!ctx.exec) {
    fail("ERR_ACCESS_DENIED", "tools-shell 需要能力 exec（清单 backend.capabilities 未声明？）");
  }
  return ctx.exec ;
}

export default function (agent          )       {
  agent.registerTool({
    name: "run_command",
    description:
      "执行一条命令并返回退出码与输出。命令与参数必须分开传（不经过 shell：没有管道、通配符、变量展开）。" +
      "cwd 只能是工作区内的路径；非零退出码不会抛错，会原样回给你判断。",
    access: "exec",
    parameters: {
      type: "object",
      properties: {
        command: { type: "string", description: "可执行文件（绝对路径或 PATH 中的名字）" },
        args: {
          type: "array",
          items: { type: "string" },
          description: "参数数组；带空格或 shell 元字符的参数直接作为一个元素传入",
        },
        cwd: { type: "string", description: "工作目录（虚拟路径，如 /workspace/sub）" },
        timeoutMs: { type: "number", description: "超时毫秒数，缺省由宿主决定（上限 120s）" },
      },
      required: ["command"],
    },
    execute: async (args     , ctx             ) => {
      const command = typeof args?.command === "string" ? args.command.trim() : "";
      if (command.length === 0) fail("ERR_INVALID_ARG_TYPE", "command 必须是非空字符串");

      const rawArgs = args?.args;
      if (rawArgs !== undefined && !Array.isArray(rawArgs)) {
        fail("ERR_INVALID_ARG_TYPE", "args 必须是字符串数组（本插件不经过 shell，不能传整条命令行）");
      }
      const argv           = (rawArgs ?? []).map((a         ) => String(a));
      for (const a of argv) {
        if (typeof a !== "string") fail("ERR_INVALID_ARG_TYPE", "args 的每个元素都必须是字符串");
      }

      // 常见误用：把整条命令行塞进 command。这会让「无 shell」变成一条沉默的假象。
      if (argv.length === 0 && /\s/.test(command)) {
        fail(
          "ERR_INVALID_ARG_VALUE",
          `command 里出现了空白（${JSON.stringify(command)}）：本插件不经过 shell，请把命令与参数分开传，` +
            `例如 { command: "git", args: ["status", "--short"] }`,
        );
      }

      let timeoutMs                    ;
      if (args?.timeoutMs !== undefined) {
        const n = Number(args.timeoutMs);
        if (!Number.isFinite(n) || n <= 0 || n >= MAX_TIMEOUT_MS) {
          fail("ERR_INVALID_ARG_VALUE", `timeoutMs 必须是 (0, ${MAX_TIMEOUT_MS}) 内的数字`);
        }
        timeoutMs = Math.trunc(n);
      }
      const cwd = args?.cwd === undefined || args.cwd === null ? undefined : String(args.cwd);

      const exec = requireExec(ctx);
      const started = Date.now();
      let res            ;
      try {
        res = await exec(command, argv, { cwd, timeoutMs });
      } catch (e     ) {
        // 启动失败 / 超时 / 被策略拒绝：原样抛出，但补一句「是哪条命令」。
        const code = e && e.code ? e.code : "ERR_CHILD_PROCESS_FAILED";
        const err = new Error(`执行 ${command} 失败：${e && e.message ? e.message : String(e)}`);
        (err       ).code = code;
        throw err;
      }

      const stdout = truncate(res.stdout ?? "");
      const stderr = truncate(res.stderr ?? "");
      const parts = [`exit ${res.code}`];
      if (stdout.text.length > 0) parts.push(stdout.text.replace(/\n$/, ""));
      if (stderr.text.length > 0) parts.push("--- stderr ---", stderr.text.replace(/\n$/, ""));
      if (stdout.truncated || stderr.truncated) parts.push("... (truncated)");

      ctx.log("info", "command finished", { command, code: res.code, ms: Date.now() - started });
      return {
        content: [{ type: "text", text: parts.join("\n") }],
        details: {
          code: res.code,
          stdoutBytes: byteLength(res.stdout ?? ""),
          stderrBytes: byteLength(res.stderr ?? ""),
          truncated: stdout.truncated || stderr.truncated,
          cwd: cwd ?? null,
        },
      };
    },
  });
}
