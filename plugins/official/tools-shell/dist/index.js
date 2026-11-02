// generated from index.ts (sha256:595fa810a421b56d) by plugins/official/build.mjs — do not edit
// tools-shell —— 官方插件：执行命令（05 §6.3、12 §6.13）。
//
// 只有 execFile 语义：命令与参数分开传，永远不经过 shell（没有管道、通配、变量展开）。
// cwd 只能在工作区内、env 只注入清单白名单、超时与 maxBuffer 由宿主兜底；
// 本插件只负责「把模型给的结构化参数翻译成一次受控调用，并把输出整理成可读文本」。
//
// 后台任务走同一套宿主边界：run_background 启动，check_task 轮询，kill_task 终止。
// 任务在轮次结束后继续存在，插件 Runtime 关闭时宿主统一杀掉。
//
// 类型形状与 agent:api 一致，本地声明避免官方插件依赖 sdk 的类型包。

                      
               
                 
                 
 

                           
                 
              
 

                      
                 
              
                 
               
                 
                 
                     
                      
                      
                    
                   
                 
 

                      
                 
                 
                  
 

                       
                                                                                                           
                    
                
                   
                                                
                                
                                             
                                            
                                                             
 

                   
               
                      
                     
                                                
                       
                                                    
 

                    
                                   
 

const MAX_BYTES = 50 * 1024;
const MAX_LINES = 2000;
const MAX_TIMEOUT_MS = 600000;
// 与宿主 Limits.MaxBackgroundTimeout 一致：后台任务最长 30 分钟。
const MAX_BACKGROUND_TIMEOUT_MS = 30 * 60 * 1000;

// 宿主平台（win32/darwin/linux）：命令是直接 execFile 的，没有 shell；
// Windows 自带的 find/grep/ls 与 Unix 语义不同，需要提前告诉模型。
const PLATFORM = String((globalThis       ).process?.platform || "");
const UNIX_ONLY = new Set([
  "find", "grep", "ls", "cat", "sed", "awk", "rm", "cp", "mv", "chmod",
  "which", "head", "tail", "wc", "xargs", "touch", "pwd", "env", "test",
]);

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

function requireBackground(ctx             ) {
  if (!ctx.execBackground || !ctx.checkTask || !ctx.killTask) {
    fail("ERR_ACCESS_DENIED", "tools-shell 的后台任务需要能力 exec（清单 backend.capabilities 未声明？）");
  }
  return {
    start: ctx.execBackground ,
    check: ctx.checkTask ,
    kill: ctx.killTask ,
  };
}

// parseCommand 校验 command/args：命令与参数必须分开，且单条命令行不能被塞进 command。
function parseCommand(args     )                                      {
  const command = typeof args?.command === "string" ? args.command.trim() : "";
  if (command.length === 0) fail("ERR_INVALID_ARG_TYPE", "command 必须是非空字符串");

  const rawArgs = args?.args;
  if (rawArgs !== undefined && !Array.isArray(rawArgs)) {
    fail("ERR_INVALID_ARG_TYPE", "args 必须是字符串数组（本插件不经过 shell，不能传整条命令行）");
  }
  const argv = (rawArgs ?? [])             ;
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
  return { command, argv: argv             };
}

// readTimeout 读取并校验 timeoutMs；不同工具的上限不同。
function readTimeout(args     , max        )                     {
  if (args?.timeoutMs === undefined) return undefined;
  const n = Number(args.timeoutMs);
  if (!Number.isFinite(n) || n <= 0 || n > max) {
    fail("ERR_INVALID_ARG_VALUE", `timeoutMs 必须是 (0, ${max}] 内的数字`);
  }
  return Math.trunc(n);
}

function readCwd(args     )                     {
  return args?.cwd === undefined || args.cwd === null ? undefined : String(args.cwd);
}

function formatTask(st            )         {
  const parts = [`status: ${st.status}`, `taskId: ${st.taskId}`, `pid: ${st.pid}`];
  if (st.status !== "running") parts.push(`exit: ${st.code}`);
  if (st.error) parts.push(`error: ${st.error}`);
  if (st.stdout) parts.push("--- stdout ---", st.stdout.replace(/\n$/, ""));
  if (st.stderr) parts.push("--- stderr ---", st.stderr.replace(/\n$/, ""));
  if (st.truncated) {
    parts.push(
      `... (output truncated; stdout ${st.stdoutBytes} bytes, stderr ${st.stderrBytes} bytes)`,
    );
  }
  return parts.join("\n");
}

export default function (agent          )       {
  agent.registerTool({
    name: "run_command",
    description:
      "执行一条命令并返回退出码与输出。命令与参数必须分开传（不经过 shell：没有管道、通配符、变量展开）。" +
      `当前宿主平台：${PLATFORM || "未知"}；Windows 原生 find/grep/ls 与 Unix 语义不同，` +
      "查找文件优先用 list_files/search_files。" +
      "args 是 JSON 数组（不是序列化后的字符串）；带空格或引号的参数作为一个元素传入。" +
      "写文件优先用 write_file/edit_file，不要用 echo/Set-Content 拼接内容（引号与换行极易出错）。" +
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
      const { command, argv } = parseCommand(args);
      const timeoutMs = readTimeout(args, MAX_TIMEOUT_MS);
      const cwd = readCwd(args);

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
      // Windows 上最常见的误用：把 Unix find/grep/ls 的参数交给同名原生命令。
      if (res.code !== 0 && PLATFORM === "win32" && UNIX_ONLY.has(command)) {
        parts.push(
          "--- hint ---",
          `Windows 宿主没有 Unix ${command} 的语义；查找文件/目录请用 list_files/search_files，` +
            "或使用 PowerShell/cmd 的等价命令。",
        );
      }

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

  agent.registerTool({
    name: "run_background",
    description:
      "启动一条后台命令并立即返回 taskId；用 check_task 轮询输出/状态，用 kill_task 终止。" +
      "适用长任务（构建、测试、监听服务等）。命令与参数必须分开传，不经过 shell；" +
      "cwd 只能在工作区内；后台任务最长 30 分钟，超时由宿主杀整棵进程树。",
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
        timeoutMs: { type: "number", description: "生命周期毫秒数，缺省 30 分钟，上限 30 分钟" },
      },
      required: ["command"],
    },
    execute: async (args     , ctx             ) => {
      const { command, argv } = parseCommand(args);
      const timeoutMs = readTimeout(args, MAX_BACKGROUND_TIMEOUT_MS);
      const cwd = readCwd(args);
      const bg = requireBackground(ctx);

      let started                 ;
      try {
        started = await bg.start(command, argv, { cwd, timeoutMs });
      } catch (e     ) {
        const code = e && e.code ? e.code : "ERR_CHILD_PROCESS_FAILED";
        const err = new Error(`启动后台命令 ${command} 失败：${e && e.message ? e.message : String(e)}`);
        (err       ).code = code;
        throw err;
      }

      ctx.log("info", "background command started", {
        command,
        taskId: started.taskId,
        pid: started.pid,
      });
      return {
        content: [
          {
            type: "text",
            text:
              `后台任务已启动：taskId=${started.taskId} pid=${started.pid}\n` +
              "用 check_task 轮询状态；任务结束前不会阻塞当前轮次。",
          },
        ],
        details: { taskId: started.taskId, pid: started.pid, cwd: cwd ?? null },
      };
    },
  });

  agent.registerTool({
    name: "check_task",
    description:
      "查看 run_background 启动的后台任务：状态（running/exited/timeout/killed/failed）、" +
      "退出码、stdout/stderr 尾部与字节数。轮询类工具，不参与连续重复调用检测。",
    access: "read",
    repeatable: true,
    parameters: {
      type: "object",
      properties: {
        taskId: { type: "number", description: "run_background 返回的 taskId" },
      },
      required: ["taskId"],
    },
    execute: (args     , ctx             ) => {
      const taskId = Number(args?.taskId);
      if (!Number.isInteger(taskId) || taskId <= 0) {
        fail("ERR_INVALID_ARG_VALUE", "taskId 必须是正整数");
      }
      const st = requireBackground(ctx).check(taskId);
      return {
        content: [{ type: "text", text: formatTask(st) }],
        details: st,
      };
    },
  });

  agent.registerTool({
    name: "kill_task",
    description:
      "终止 run_background 启动的后台任务（只影响本插件启动的任务）；返回 found/killed。" +
      "任务已结束时 found=true、killed=false。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        taskId: { type: "number", description: "run_background 返回的 taskId" },
      },
      required: ["taskId"],
    },
    execute: (args     , ctx             ) => {
      const taskId = Number(args?.taskId);
      if (!Number.isInteger(taskId) || taskId <= 0) {
        fail("ERR_INVALID_ARG_VALUE", "taskId 必须是正整数");
      }
      const res = requireBackground(ctx).kill(taskId);
      const text = res.killed
        ? `已终止后台任务 ${taskId}`
        : res.found
          ? `后台任务 ${taskId} 已经结束，无需终止`
          : `没有找到后台任务 ${taskId}`;
      return {
        content: [{ type: "text", text }],
        details: res,
      };
    },
  });
}
