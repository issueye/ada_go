// tools-fs —— 官方插件：工作区内的文件读写（05 §6.3、02 §5.4 的 tools-fs）。
//
// 边界不在这一层：真正的根、只读、symlink 落点、16MB/10k 上限与审计都由宿主的
// ctx.fs 与 node:fs 包装执行（12 §6.12.1）。本插件负责的是「模型友好的形状」：
// 行偏移、按行/字节截断、编辑的精确匹配、递归列目录与子串搜索。
//
// 类型形状与 agent:api 一致，本地声明避免官方插件依赖 sdk 的类型包。

interface FsEntry {
  name: string;
  dir: boolean;
  size: number;
}

interface FsBridge {
  readText(path: string, opts?: { maxBytes?: number }): Promise<string>;
  writeText(path: string, text: string): Promise<{ bytes: number }>;
  list(path: string): Promise<FsEntry[]>;
  stat(path: string): Promise<{ size: number; mtime: string; dir: boolean }>;
  exists(path: string): Promise<boolean>;
}

interface ToolContext {
  workspace: string;
  fs?: FsBridge;
  log: (level: string, msg: string, fields?: object) => void;
}

interface ToolDef {
  name: string;
  description: string;
  parameters: object;
  access: "read" | "write" | "exec" | "network";
  execute: (args: any, ctx: ToolContext) => unknown;
}

interface AgentAPI {
  registerTool(def: ToolDef): void;
}

const MAX_TEXT_BYTES = 50 * 1024;
const MAX_READ_LINES = 2000;
const MAX_LIST_ENTRIES = 2000;
const MAX_WALK_DEPTH = 5;
const MAX_SEARCH_FILES = 2000;
const MAX_SEARCH_FILE_BYTES = 256 * 1024;

function fail(code: string, message: string): never {
  const e = new Error(message);
  (e as any).code = code;
  throw e;
}

function requireFs(ctx: ToolContext): FsBridge {
  if (!ctx.fs) {
    fail("ERR_ACCESS_DENIED", "tools-fs 需要能力 fs.read/fs.write（清单 backend.capabilities 未声明？）");
  }
  return ctx.fs!;
}

function byteLength(s: string): number {
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

// normalizePath 只做显示层的归一（折叠重复斜杠与 ./），真正的 scope 校验在宿主。
function normalizePath(input: unknown, workspace: string): string {
  const raw = String(input ?? "").trim();
  if (raw.length === 0) fail("ERR_INVALID_ARG_TYPE", "path 必须是非空字符串");
  const base = workspace && workspace.length > 0 ? workspace : "/workspace";
  const abs = raw.startsWith("/") ? raw : `${base}/${raw}`;
  const parts: string[] = [];
  for (const seg of abs.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") {
      // 防御纵深：越出工作区一律拒绝（宿主还会再判一次）。
      if (parts.length === 0) fail("ERR_ACCESS_DENIED", `路径 ${raw} 越出工作区`);
      parts.pop();
      continue;
    }
    parts.push(seg);
  }
  const out = "/" + parts.join("/");
  if (out !== base && !out.startsWith(base + "/")) {
    fail("ERR_ACCESS_DENIED", `路径 ${raw} 不在工作区 ${base} 内`);
  }
  return out;
}

// clampText 按行数与字节数双重截断。
function clampText(text: string, maxBytes: number): { text: string; truncated: boolean } {
  let out = text;
  let truncated = false;
  if (out.length > maxBytes) {
    out = out.slice(0, maxBytes);
    truncated = true;
  }
  if (byteLength(out) > maxBytes) {
    // 逐字符回退到字节上限（多字节文本可能在上一步之后仍然超）。
    let n = 0;
    let i = 0;
    for (; i < out.length; i++) {
      const c = out.charCodeAt(i);
      const w = c < 0x80 ? 1 : c < 0x800 ? 2 : c >= 0xd800 && c <= 0xdbff ? 4 : 3;
      if (n + w > maxBytes) break;
      n += w;
    }
    out = out.slice(0, i);
    truncated = true;
  }
  return { text: out, truncated };
}

function looksBinary(sample: string): boolean {
  return sample.indexOf("\u0000") >= 0;
}

export default function (agent: AgentAPI): void {
  agent.registerTool({
    name: "read_file",
    description:
      "读取工作区内一个文本文件。可以按行偏移与行数分段读（大文件先读一段，再决定要不要继续）。" +
      "单次输出上限 50KB；被截断时返回文本里会有省略提示。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        path: { type: "string", description: "文件路径（虚拟路径或相对工作区的路径）" },
        offset: { type: "number", description: "起始行号（0 起，缺省 0）" },
        limit: { type: "number", description: `最多读多少行（缺省 ${MAX_READ_LINES}，上限 ${MAX_READ_LINES}）` },
      },
      required: ["path"],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const path = normalizePath(args?.path, ctx.workspace);
      const offset = args?.offset === undefined ? 0 : Math.max(0, Math.trunc(Number(args.offset)) || 0);
      let limit = args?.limit === undefined ? MAX_READ_LINES : Math.trunc(Number(args.limit)) || 0;
      if (limit <= 0 || limit > MAX_READ_LINES) limit = MAX_READ_LINES;

      const text = await requireFs(ctx).readText(path);
      const lines = text.split("\n");
      const slice = lines.slice(offset, offset + limit);
      const clamped = clampText(slice.join("\n"), MAX_TEXT_BYTES);
      const body = clamped.truncated ? clamped.text + "\n... (truncated)" : clamped.text;

      return {
        content: [{ type: "text", text: body }],
        details: {
          path,
          bytes: byteLength(text),
          lines: lines.length,
          shownFrom: offset,
          shown: slice.length,
          truncated: clamped.truncated || offset + limit < lines.length,
        },
      };
    },
  });

  agent.registerTool({
    name: "write_file",
    description: "把内容整体写入工作区内的一个文件（覆盖已有内容，父目录不存在会失败）。",
    access: "write",
    parameters: {
      type: "object",
      properties: {
        path: { type: "string", description: "文件路径" },
        content: { type: "string", description: "完整文件内容" },
      },
      required: ["path", "content"],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const path = normalizePath(args?.path, ctx.workspace);
      if (typeof args?.content !== "string") fail("ERR_INVALID_ARG_TYPE", "content 必须是字符串");
      const res = await requireFs(ctx).writeText(path, args.content);
      ctx.log("info", "file written", { path, bytes: res.bytes });
      return {
        content: [{ type: "text", text: `已写入 ${path}（${res.bytes} 字节）` }],
        details: { path, bytes: res.bytes },
      };
    },
  });

  agent.registerTool({
    name: "edit_file",
    description:
      "在文件里做一次精确文本替换。默认要求 old 只出现一次（0 次或多次都报错，避免改错地方）；" +
      "确实要全改时传 all: true。",
    access: "write",
    parameters: {
      type: "object",
      properties: {
        path: { type: "string", description: "文件路径" },
        old: { type: "string", description: "要被替换的原文（必须逐字匹配）" },
        new: { type: "string", description: "替换后的文本" },
        all: { type: "boolean", description: "允许替换全部出现（缺省 false）" },
      },
      required: ["path", "old", "new"],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const path = normalizePath(args?.path, ctx.workspace);
      const oldText = args?.old;
      const newText = args?.new;
      if (typeof oldText !== "string" || oldText.length === 0) {
        fail("ERR_INVALID_ARG_TYPE", "old 必须是非空字符串");
      }
      if (typeof newText !== "string") fail("ERR_INVALID_ARG_TYPE", "new 必须是字符串");
      const all = args?.all === true;

      const fs = requireFs(ctx);
      const text = await fs.readText(path);
      let count = 0;
      let idx = 0;
      while (true) {
        const found = text.indexOf(oldText, idx);
        if (found < 0) break;
        count++;
        idx = found + oldText.length;
      }
      if (count === 0) {
        fail("ERR_INVALID_ARG_VALUE", `在 ${path} 里找不到要替换的原文（old 必须逐字匹配）`);
      }
      if (count > 1 && !all) {
        fail(
          "ERR_INVALID_ARG_VALUE",
          `old 在 ${path} 里出现了 ${count} 次：请给出更长的上下文，或显式传 all: true`,
        );
      }
      const replaced = all ? text.split(oldText).join(newText) : text.replace(oldText, newText);
      await fs.writeText(path, replaced);
      ctx.log("info", "file edited", { path, replaced: all ? count : 1 });
      return {
        content: [{ type: "text", text: `已修改 ${path}（替换 ${all ? count : 1} 处）` }],
        details: { path, replaced: all ? count : 1, matches: count },
      };
    },
  });

  agent.registerTool({
    name: "list_files",
    description:
      "列出目录内容（dir/file、大小）。recursive 为 true 时递归，深度上限 " +
      MAX_WALK_DEPTH +
      " 层、条目上限 " +
      MAX_LIST_ENTRIES +
      " 条。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        path: { type: "string", description: "目录路径（缺省工作区根）" },
        recursive: { type: "boolean", description: "是否递归子目录（缺省 false）" },
      },
    },
    execute: async (args: any, ctx: ToolContext) => {
      const root = normalizePath(args?.path ?? ".", ctx.workspace);
      const recursive = args?.recursive === true;
      const fs = requireFs(ctx);

      const lines: string[] = [];
      let count = 0;
      let truncated = false;

      const walk = async (dir: string, depth: number): Promise<void> => {
        if (truncated) return;
        let entries: FsEntry[];
        try {
          entries = await fs.list(dir);
        } catch (e: any) {
          lines.push(`! ${dir}: ${e && e.message ? e.message : String(e)}`);
          return;
        }
        entries.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
        for (const entry of entries) {
          if (count >= MAX_LIST_ENTRIES) {
            truncated = true;
            return;
          }
          const full = `${dir === "/" ? "" : dir}/${entry.name}`;
          lines.push(`${entry.dir ? "dir " : "file"} ${full} ${entry.size}`);
          count++;
          if (recursive && entry.dir && depth + 1 < MAX_WALK_DEPTH) {
            await walk(full, depth + 1);
          }
        }
      };
      await walk(root, 0);

      const clamped = clampText(lines.join("\n"), MAX_TEXT_BYTES);
      const body = clamped.truncated || truncated ? clamped.text + "\n... (truncated)" : clamped.text;
      return {
        content: [{ type: "text", text: body.length > 0 ? body : "(空目录)" }],
        details: { path: root, count, truncated: clamped.truncated || truncated },
      };
    },
  });

  agent.registerTool({
    name: "search_files",
    description:
      "在工作区内按子串搜索文本文件，返回 `路径:行号: 内容`。跳过二进制文件与大于 256KB 的文件；" +
      "结果条数有上限。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        pattern: { type: "string", description: "要查找的子串（区分大小写）" },
        path: { type: "string", description: "搜索起点（缺省工作区根）" },
        maxResults: { type: "number", description: "最多返回多少条（缺省 100，上限 500）" },
      },
      required: ["pattern"],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const pattern = typeof args?.pattern === "string" ? args.pattern : "";
      if (pattern.length === 0) fail("ERR_INVALID_ARG_TYPE", "pattern 必须是非空字符串");
      const root = normalizePath(args?.path ?? ".", ctx.workspace);
      let maxResults = args?.maxResults === undefined ? 100 : Math.trunc(Number(args.maxResults)) || 0;
      if (maxResults <= 0 || maxResults > 500) maxResults = 500;

      const fs = requireFs(ctx);
      const hits: string[] = [];
      let files = 0;
      let truncated = false;

      const walk = async (dir: string, depth: number): Promise<void> => {
        if (truncated) return;
        let entries: FsEntry[];
        try {
          entries = await fs.list(dir);
        } catch {
          return;
        }
        entries.sort((a, b) => a.name.localeCompare(b.name));
        for (const entry of entries) {
          if (truncated) return;
          const full = `${dir === "/" ? "" : dir}/${entry.name}`;
          if (entry.dir) {
            if (depth + 1 < MAX_WALK_DEPTH) await walk(full, depth + 1);
            continue;
          }
          if (files >= MAX_SEARCH_FILES) {
            truncated = true;
            return;
          }
          if (entry.size > MAX_SEARCH_FILE_BYTES) continue;
          files++;
          let text: string;
          try {
            text = await fs.readText(full, { maxBytes: MAX_SEARCH_FILE_BYTES });
          } catch {
            continue;
          }
          if (looksBinary(text.slice(0, 1024))) continue;
          const lines = text.split("\n");
          for (let i = 0; i < lines.length; i++) {
            if (lines[i].indexOf(pattern) < 0) continue;
            hits.push(`${full}:${i + 1}: ${lines[i].trim().slice(0, 300)}`);
            if (hits.length >= maxResults) {
              truncated = true;
              return;
            }
          }
        }
      };
      await walk(root, 0);

      const clamped = clampText(hits.join("\n"), MAX_TEXT_BYTES);
      const body =
        hits.length === 0 ? `没有匹配 ${JSON.stringify(pattern)}` : clamped.truncated ? clamped.text + "\n... (truncated)" : clamped.text;
      ctx.log("info", "search finished", { pattern, files, hits: hits.length });
      return {
        content: [{ type: "text", text: body }],
        details: { pattern, path: root, files, matches: hits.length, truncated: clamped.truncated || truncated },
      };
    },
  });
}
