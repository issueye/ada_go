// node:assert —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 默认导出是可直接调用的 assert(value, message)，同时挂着整套 API（Node 的形状）。
//
// 有意简化：
//   - deepEqual（松散）按 deepStrictEqual 实现，不做 == 语义的隐式转换；
//   - 错误渲染是简化的 JSON-ish 文本（无颜色、无 diff 高亮）；
//   - 没有 partialDeepStrictEqual / snapshot / AssertionError 的 multi-line 报告；
//   - 生成的消息文案与 Node 不完全逐字一致（字段、code、operator 一致）。

function render(value, seen) {
  seen = seen || [];
  if (value === null) return "null";
  if (value === undefined) return "undefined";
  const t = typeof value;
  if (t === "string") return JSON.stringify(value);
  if (t === "number" || t === "boolean" || t === "bigint") return String(value);
  if (t === "symbol") return value.toString();
  if (t === "function") return "[Function: " + (value.name || "(anonymous)") + "]";
  if (seen.indexOf(value) >= 0) return "[Circular]";
  seen.push(value);
  try {
    if (Array.isArray(value)) return "[" + value.map((v) => render(v, seen)).join(", ") + "]";
    if (value instanceof Date) return value.toISOString();
    if (value instanceof RegExp) return value.toString();
    if (value instanceof Error) return value.name + ": " + value.message;
    if (value instanceof Map) {
      const parts = [];
      value.forEach((v, k) => parts.push(render(k, seen) + " => " + render(v, seen)));
      return "Map { " + parts.join(", ") + " }";
    }
    if (value instanceof Set) {
      const parts = [];
      value.forEach((v) => parts.push(render(v, seen)));
      return "Set { " + parts.join(", ") + " }";
    }
    const keys = Object.keys(value);
    return "{ " + keys.map((k) => k + ": " + render(value[k], seen)).join(", ") + " }";
  } finally {
    seen.pop();
  }
}

export class AssertionError extends Error {
  constructor(options) {
    const opts = options || {};
    let message;
    if (opts.message !== undefined && opts.message !== null) {
      message = String(opts.message);
    } else if (opts.generatedMessage) {
      message = "The expression evaluated to a falsy value:";
    } else {
      message = "Assertion failed";
    }
    super(message);
    this.name = "AssertionError";
    this.code = "ERR_ASSERTION";
    this.actual = opts.actual;
    this.expected = opts.expected;
    this.operator = opts.operator;
    this.generatedMessage = !!opts.generatedMessage;
  }
}

function fail(message, actual, expected, operator) {
  throw new AssertionError({
    message,
    actual,
    expected,
    operator,
    generatedMessage: message === undefined || message === null,
  });
}

function invalidArg(name, expected) {
  const e = new TypeError('The "' + name + '" argument must be of type ' + expected);
  e.code = "ERR_INVALID_ARG_TYPE";
  return e;
}

function innerFail(actual, expected, message, operator) {
  throw new AssertionError({
    actual,
    expected,
    message,
    operator,
    generatedMessage: message === undefined || message === null,
  });
}

function assert(value, message) {
  if (!value) {
    innerFail(value, true, message, "==");
  }
}

// isDeepStrict 是 deepStrictEqual 的比较核心：不做隐式转换，处理循环引用。
function isDeepStrict(a, b, memo) {
  if (Object.is(a, b)) return true;
  if (typeof a !== typeof b) return false;
  if (a === null || b === null) return false;
  if (typeof a !== "object") return false;

  if (a instanceof Date || b instanceof Date) {
    return a instanceof Date && b instanceof Date && Object.is(a.getTime(), b.getTime());
  }
  if (a instanceof RegExp || b instanceof RegExp) {
    return a instanceof RegExp && b instanceof RegExp && a.source === b.source && a.flags === b.flags;
  }
  if (a instanceof Error || b instanceof Error) {
    return a instanceof Error && b instanceof Error && a.name === b.name && a.message === b.message;
  }
  if (a instanceof Map || b instanceof Map) {
    if (!(a instanceof Map) || !(b instanceof Map) || a.size !== b.size) return false;
    const aEntries = [];
    a.forEach((v, k) => aEntries.push([k, v]));
    for (const [k, v] of aEntries) {
      if (!b.has(k) || !isDeepStrict(v, b.get(k), memo)) return false;
    }
    return true;
  }
  if (a instanceof Set || b instanceof Set) {
    if (!(a instanceof Set) || !(b instanceof Set) || a.size !== b.size) return false;
    for (const v of a) {
      if (!b.has(v)) return false;
    }
    return true;
  }
  if (ArrayBuffer.isView(a) || ArrayBuffer.isView(b)) {
    if (!ArrayBuffer.isView(a) || !ArrayBuffer.isView(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
    return true;
  }
  if (Array.isArray(a) !== Array.isArray(b)) return false;

  // 循环引用：已比较过的 (a,b) 对直接视为相等（避免无限递归）。
  memo = memo || [];
  for (const pair of memo) {
    if (pair[0] === a && pair[1] === b) return true;
  }
  memo.push([a, b]);

  const aKeys = Object.keys(a).filter((k) => a[k] !== undefined);
  const bKeys = Object.keys(b).filter((k) => b[k] !== undefined);
  if (aKeys.length !== bKeys.length) return false;
  for (const key of aKeys) {
    if (!Object.prototype.hasOwnProperty.call(b, key)) return false;
    if (!isDeepStrict(a[key], b[key], memo)) return false;
  }
  return true;
}

function strictEqual(actual, expected, message) {
  if (!Object.is(actual, expected)) {
    innerFail(actual, expected, message, "strictEqual");
  }
}

function notStrictEqual(actual, expected, message) {
  if (Object.is(actual, expected)) {
    innerFail(actual, expected, message, "notStrictEqual");
  }
}

function equal(actual, expected, message) {
  // eslint-disable-next-line eqeqeq
  if (actual != expected) {
    innerFail(actual, expected, message, "==");
  }
}

function notEqual(actual, expected, message) {
  // eslint-disable-next-line eqeqeq
  if (actual == expected) {
    innerFail(actual, expected, message, "!=");
  }
}

function deepStrictEqual(actual, expected, message) {
  if (!isDeepStrict(actual, expected)) {
    innerFail(actual, expected, message, "deepStrictEqual");
  }
}

function notDeepStrictEqual(actual, expected, message) {
  if (isDeepStrict(actual, expected)) {
    innerFail(actual, expected, message, "notDeepStrictEqual");
  }
}

function deepEqual(actual, expected, message) {
  deepStrictEqual(actual, expected, message);
}

function notDeepEqual(actual, expected, message) {
  notDeepStrictEqual(actual, expected, message);
}

function matchesExpectation(err, expected) {
  if (expected === undefined || expected === null) return true;
  if (typeof expected === "function") return err instanceof expected;
  if (expected instanceof RegExp) return expected.test(String(err && err.message));
  if (typeof expected === "object") {
    // 支持 { name, message, code } 这类属性匹配（Node 的形状）。
    for (const key of Object.keys(expected)) {
      const want = expected[key];
      const have = err ? err[key] : undefined;
      if (want instanceof RegExp) {
        if (!want.test(String(have))) return false;
      } else if (!Object.is(have, want)) {
        return false;
      }
    }
    return true;
  }
  return false;
}

function throws(fn, expected, message) {
  if (typeof fn !== "function") throw invalidArg("fn", "function");
  let thrown = false;
  let err;
  try {
    fn();
  } catch (e) {
    thrown = true;
    err = e;
  }
  if (!thrown) {
    throw new AssertionError({
      message: message || "Missing expected exception.",
      actual: undefined,
      expected,
      operator: "throws",
      generatedMessage: !message,
    });
  }
  if (!matchesExpectation(err, expected)) {
    const rendered = expected instanceof RegExp ? String(expected) : render(expected);
    throw new AssertionError({
      message: message || "The error did not match the expected: " + rendered + " (actual: " + render(err) + ")",
      actual: err,
      expected,
      operator: "throws",
      generatedMessage: !message,
    });
  }
  return err;
}

function doesNotThrow(fn, message) {
  if (typeof fn !== "function") throw invalidArg("fn", "function");
  try {
    fn();
  } catch (e) {
    const err = new AssertionError({
      message: message || "Got unwanted exception: " + render(e),
      actual: e,
      operator: "doesNotThrow",
      generatedMessage: !message,
    });
    err.cause = e;
    throw err;
  }
}

function rejects(promiseOrFn, expected, message) {
  let promise;
  if (typeof promiseOrFn === "function") {
    try {
      promise = promiseOrFn();
    } catch (e) {
      promise = Promise.reject(e);
    }
  } else {
    promise = promiseOrFn;
  }
  return Promise.resolve(promise).then(
    () => {
      throw new AssertionError({
        message: message || "Missing expected rejection.",
        expected,
        operator: "rejects",
        generatedMessage: !message,
      });
    },
    (err) => {
      if (!matchesExpectation(err, expected)) {
        throw new AssertionError({
          message: message || "The rejection did not match: " + render(err),
          actual: err,
          expected,
          operator: "rejects",
          generatedMessage: !message,
        });
      }
      return err;
    },
  );
}

function doesNotReject(promiseOrFn, message) {
  let promise;
  if (typeof promiseOrFn === "function") {
    try {
      promise = promiseOrFn();
    } catch (e) {
      promise = Promise.reject(e);
    }
  } else {
    promise = promiseOrFn;
  }
  return Promise.resolve(promise).then(
    () => undefined,
    (err) => {
      throw new AssertionError({
        message: message || "Got unwanted rejection: " + render(err),
        actual: err,
        operator: "doesNotReject",
        generatedMessage: !message,
      });
    },
  );
}

function ifError(value) {
  if (value !== null && value !== undefined) {
    throw new AssertionError({ actual: value, expected: null, operator: "ifError", generatedMessage: true });
  }
}

function match(str, regexp, message) {
  if (!regexp.test(String(str))) {
    innerFail(str, regexp, message, "match");
  }
}

function doesNotMatch(str, regexp, message) {
  if (regexp.test(String(str))) {
    innerFail(str, regexp, message, "doesNotMatch");
  }
}

// 把 API 挂到 default 导出的函数上（Node 的形状）。
assert.ok = assert;
assert.equal = equal;
assert.notEqual = notEqual;
assert.strictEqual = strictEqual;
assert.notStrictEqual = notStrictEqual;
assert.deepEqual = deepEqual;
assert.notDeepEqual = notDeepEqual;
assert.deepStrictEqual = deepStrictEqual;
assert.notDeepStrictEqual = notDeepStrictEqual;
assert.throws = throws;
assert.doesNotThrow = doesNotThrow;
assert.rejects = rejects;
assert.doesNotReject = doesNotReject;
assert.fail = function fail_(message) {
  throw new AssertionError({ message: message || "Failed", operator: "fail", generatedMessage: !message });
};
assert.ifError = ifError;
assert.match = match;
assert.doesNotMatch = doesNotMatch;
assert.AssertionError = AssertionError;

// assert.strict：同一套严格函数（Node 里它是函数 + 属性，这里保持同样形状）。
const strict = function strict(value, message) {
  return assert(value, message);
};
strict.ok = assert.ok;
strict.equal = strictEqual;
strict.notEqual = notStrictEqual;
strict.strictEqual = strictEqual;
strict.notStrictEqual = notStrictEqual;
strict.deepEqual = deepStrictEqual;
strict.notDeepEqual = notDeepStrictEqual;
strict.deepStrictEqual = deepStrictEqual;
strict.notDeepStrictEqual = notDeepStrictEqual;
strict.throws = throws;
strict.doesNotThrow = doesNotThrow;
strict.rejects = rejects;
strict.doesNotReject = doesNotReject;
strict.fail = assert.fail;
strict.ifError = ifError;
strict.match = match;
strict.doesNotMatch = doesNotMatch;
strict.AssertionError = AssertionError;
assert.strict = strict;

export { strict, equal, notEqual, strictEqual, notStrictEqual, deepEqual, notDeepEqual, deepStrictEqual, notDeepStrictEqual, throws, doesNotThrow, rejects, doesNotReject, ifError, fail, match, doesNotMatch };
export const ok = assert;
export default assert;
