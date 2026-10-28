// node:path —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 只用 ECMAScript 内建对象：没有 import，也没有 Buffer / process / console。
// resolve/relative 的基准是虚拟根 "/"（工作区），不读任何宿主 cwd（12 §6.12.2：
// 「path.resolve 以 process.cwd() 为基准，即虚拟根 /」）。
//
// 有意简化（写在文件头，便于对账）：
//   - 只实现 Node posix 算法的字符串语义；win32 视图是把分隔符翻译成 "/" 后复用
//     同一套算法的视图，驱动盘相对路径（"C:foo"）、UNC 归并、设备路径未实现；
//   - 不做 Node 的 Buffer/URL 形态入参校验（只接受字符串，类型错误带 ERR_INVALID_ARG_TYPE）；
//   - toNamespacedPath 是恒等（虚拟 FS 没有 Windows 命名空间）。

const SLASH = 47 /* / */;
const DOT = 46 /* . */;

function invalidArgType(name, expected, actual) {
  const err = new TypeError(
    'The "' + name + '" argument must be of type ' + expected + '. Received ' + actual,
  );
  err.code = 'ERR_INVALID_ARG_TYPE';
  return err;
}

function assertPath(p) {
  if (typeof p !== 'string') throw invalidArgType('path', 'string', typeof p);
}

// normalizeString 是 Node posix 的核心：消掉 "."、".." 与重复斜杠。
// allowAboveRoot 为 true 时允许结果以 ".." 开头（相对路径）。
function normalizeString(p, allowAboveRoot) {
  let res = '';
  let lastSegmentLength = 0;
  let lastSlash = -1;
  let dots = 0;
  let code = 0;
  for (let i = 0; i <= p.length; ++i) {
    if (i < p.length) code = p.charCodeAt(i);
    else if (code === SLASH) break;
    else code = SLASH;

    if (code === SLASH) {
      if (lastSlash === i - 1 || dots === 1) {
        // 空段或 "."：跳过
      } else if (dots === 2) {
        // ".."
        if (
          res.length < 2 ||
          lastSegmentLength !== 2 ||
          res.charCodeAt(res.length - 1) !== DOT ||
          res.charCodeAt(res.length - 2) !== DOT
        ) {
          if (res.length > 2) {
            const lastSlashIndex = res.lastIndexOf('/');
            if (lastSlashIndex === -1) {
              res = '';
              lastSegmentLength = 0;
            } else {
              res = res.slice(0, lastSlashIndex);
              lastSegmentLength = res.length - 1 - res.lastIndexOf('/');
            }
            lastSlash = i;
            dots = 0;
            continue;
          } else if (res.length !== 0) {
            res = '';
            lastSegmentLength = 0;
            lastSlash = i;
            dots = 0;
            continue;
          }
        }
        if (allowAboveRoot) {
          res += res.length > 0 ? '/..' : '..';
          lastSegmentLength = 2;
        }
      } else {
        if (res.length > 0) res += '/' + p.slice(lastSlash + 1, i);
        else res = p.slice(lastSlash + 1, i);
        lastSegmentLength = i - lastSlash - 1;
      }
      lastSlash = i;
      dots = 0;
    } else if (code === DOT && dots !== -1) {
      ++dots;
    } else {
      dots = -1;
    }
  }
  return res;
}

export function normalize(p) {
  assertPath(p);
  if (p.length === 0) return '.';
  const isAbsolute = p.charCodeAt(0) === SLASH;
  const trailingSeparator = p.charCodeAt(p.length - 1) === SLASH;
  let out = normalizeString(p, !isAbsolute);
  if (out.length === 0) {
    if (isAbsolute) return '/';
    return trailingSeparator ? './' : '.';
  }
  if (trailingSeparator) out += '/';
  return isAbsolute ? '/' + out : out;
}

export function isAbsolute(p) {
  assertPath(p);
  return p.length > 0 && p.charCodeAt(0) === SLASH;
}

export function join(...parts) {
  if (parts.length === 0) return '.';
  let joined;
  for (const part of parts) {
    assertPath(part);
    if (part.length > 0) joined = joined === undefined ? part : joined + '/' + part;
  }
  if (joined === undefined || joined.length === 0) return '.';
  return normalize(joined);
}

export function resolve(...parts) {
  let resolvedPath = '';
  let resolvedAbsolute = false;
  // i === -1 是兜底的 cwd：虚拟根 "/"（工作区），不是宿主目录。
  for (let i = parts.length - 1; i >= -1 && !resolvedAbsolute; i--) {
    const p = i >= 0 ? parts[i] : '/';
    assertPath(p);
    if (p.length === 0) continue;
    resolvedPath = p + '/' + resolvedPath;
    resolvedAbsolute = p.charCodeAt(0) === SLASH;
  }
  let out = normalizeString(resolvedPath, !resolvedAbsolute);
  if (resolvedAbsolute) return '/' + out;
  return out.length > 0 ? out : '.';
}

export function relative(from, to) {
  assertPath(from);
  assertPath(to);
  if (from === to) return '';
  const fromAbs = resolve(from);
  const toAbs = resolve(to);
  if (fromAbs === toAbs) return '';

  const fromParts = fromAbs.split('/').filter((s) => s.length > 0);
  const toParts = toAbs.split('/').filter((s) => s.length > 0);
  let common = 0;
  while (common < fromParts.length && common < toParts.length && fromParts[common] === toParts[common]) {
    common++;
  }
  const up = new Array(fromParts.length - common).fill('..');
  const rest = toParts.slice(common);
  const out = up.concat(rest).join('/');
  return out;
}

export function dirname(p) {
  assertPath(p);
  if (p.length === 0) return '.';
  const hasRoot = p.charCodeAt(0) === SLASH;
  let end = -1;
  let matchedSlash = true;
  for (let i = p.length - 1; i >= 1; --i) {
    if (p.charCodeAt(i) === SLASH) {
      if (!matchedSlash) {
        end = i;
        break;
      }
    } else {
      matchedSlash = false;
    }
  }
  if (end === -1) return hasRoot ? '/' : '.';
  if (hasRoot && end === 1) return '//';
  return p.slice(0, end);
}

export function basename(p, ext) {
  assertPath(p);
  if (ext !== undefined) assertPath(ext);
  let end = p.length;
  while (end > 1 && p.charCodeAt(end - 1) === SLASH) end--;
  const start = p.lastIndexOf('/', end - 1) + 1;
  let base = p.slice(start, end);
  if (ext !== undefined && ext.length > 0 && base.length > ext.length && base.endsWith(ext)) {
    base = base.slice(0, base.length - ext.length);
  }
  return base;
}

export function extname(p) {
  assertPath(p);
  let startDot = -1;
  let startPart = 0;
  let end = -1;
  let matchedSlash = true;
  let preDotState = 0;
  for (let i = p.length - 1; i >= 0; --i) {
    const code = p.charCodeAt(i);
    if (code === SLASH) {
      if (!matchedSlash) {
        startPart = i + 1;
        break;
      }
      continue;
    }
    if (end === -1) {
      matchedSlash = false;
      end = i + 1;
    }
    if (code === DOT) {
      if (startDot === -1) startDot = i;
      else if (preDotState !== 1) preDotState = 1;
    } else if (startDot !== -1) {
      preDotState = -1;
    }
  }
  if (
    startDot === -1 ||
    end === -1 ||
    preDotState === 0 ||
    (preDotState === 1 && startDot === end - 1 && startDot === startPart + 1)
  ) {
    return '';
  }
  return p.slice(startDot, end);
}

export function parse(p) {
  assertPath(p);
  const ret = { root: '', dir: '', base: '', ext: '', name: '' };
  if (p.length === 0) return ret;
  if (p.charCodeAt(0) === SLASH) ret.root = '/';
  const rootLen = ret.root.length;
  let end = p.length;
  while (end > rootLen && p.charCodeAt(end - 1) === SLASH) end--;
  if (end <= rootLen) {
    ret.dir = ret.root;
    return ret;
  }
  const lastSlash = p.lastIndexOf('/', end - 1);
  const start = lastSlash < rootLen ? rootLen : lastSlash + 1;
  ret.base = p.slice(start, end);
  if (start > rootLen) ret.dir = p.slice(0, start - 1);
  else ret.dir = ret.root;
  const e = extname(ret.base);
  if (e.length > 0) {
    ret.ext = e;
    ret.name = ret.base.slice(0, ret.base.length - e.length);
  } else {
    ret.name = ret.base;
  }
  return ret;
}
export function format(obj) {
  if (obj === null || typeof obj !== 'object') throw invalidArgType('pathObject', 'object', typeof obj);
  const dir = obj.dir || obj.root || '';
  const base = obj.base || (obj.name || '') + (obj.ext || '');
  if (dir.length === 0) return base;
  if (dir === obj.root) return dir + base;
  return dir.charCodeAt(dir.length - 1) === SLASH ? dir + base : dir + '/' + base;
}

export function toNamespacedPath(p) {
  // 虚拟 FS 没有 Windows 命名空间；保留 API 形状，恒等返回。
  return p;
}

// ---- win32 视图 -------------------------------------------------------------
//
// 把 "\" 翻译成 "/" 后复用上面同一套算法，再翻译回去。这是**视图**，
// 不是完整 Win32 语义：盘符相对路径、UNC 归并与设备路径未实现（文件头已声明）。

function toPosix(p) {
  return p.replace(/\\/g, '/');
}
function fromPosix(p) {
  return p.replace(/\//g, '\\');
}

const win32 = {
  sep: '\\',
  delimiter: ';',
  normalize: (p) => fromPosix(normalize(toPosix(p))),
  isAbsolute: (p) => {
    const s = toPosix(p);
    if (s.length === 0) return false;
    if (s.charCodeAt(0) === SLASH) return true;
    // "C:\..." 与 "C:/..."：盘符 + 分隔符
    if (s.length >= 3 && s.charCodeAt(1) === 58 /* : */ && s.charCodeAt(2) === SLASH) return true;
    if (s.length === 2 && s.charCodeAt(1) === 58) return false;
    return s.length >= 3 && s.startsWith('//');
  },
  join: (...parts) => fromPosix(join(...parts.map(toPosix))),
  resolve: (...parts) => {
    const mapped = parts.map((p) => (typeof p === 'string' ? toPosix(p) : p));
    // 盘符形如 "C:"：作为独立段保留（简化规则：出现盘符就重置结果）。
    let drive = '';
    const rest = [];
    for (const part of mapped) {
      if (typeof part === 'string' && /^[A-Za-z]:$/.test(part)) {
        drive = part.toUpperCase();
        rest.length = 0;
        continue;
      }
      rest.push(part);
    }
    const out = resolve(...rest);
    return drive ? drive + fromPosix(out) : fromPosix(out);
  },
  relative: (from, to) => fromPosix(relative(toPosix(from), toPosix(to))),
  dirname: (p) => fromPosix(dirname(toPosix(p))),
  basename: (p, ext) => basename(toPosix(p), ext),
  extname: (p) => extname(toPosix(p)),
  toNamespacedPath: (p) => p,
  format: (obj) => {
    const dir = obj.dir || obj.root || '';
    const base = obj.base || (obj.name || '') + (obj.ext || '');
    if (dir.length === 0) return base;
    if (dir === obj.root) return dir + base;
    const last = dir.charCodeAt(dir.length - 1);
    return last === SLASH || last === 92 /* \ */ ? dir + base : dir + '\\' + base;
  },
  parse: (p) => parse(toPosix(p)),
};

const posix = {
  sep: '/',
  delimiter: ':',
  normalize,
  isAbsolute,
  join,
  resolve,
  relative,
  dirname,
  basename,
  extname,
  parse,
  format,
  toNamespacedPath,
};

const path = {
  sep: '/',
  delimiter: ':',
  normalize,
  isAbsolute,
  join,
  resolve,
  relative,
  dirname,
  basename,
  extname,
  parse,
  format,
  toNamespacedPath,
  posix,
  win32,
  // Node 20 也有 _makeLong（toNamespacedPath 的别名），这里保持形状。
  _makeLong: toNamespacedPath,
};

export { posix, win32 };
export default path;
