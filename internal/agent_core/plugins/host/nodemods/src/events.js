// node:events —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
// 只用 ECMAScript 内建对象：无 import，也没有 Buffer / process / console。
// 有意简化：不触发 newListener / removeListener 元事件；不实现 captureRejections 的
// Promise 捕获与 Symbol.for('nodejs.rejection')；MaxListeners 超限不打印告警（无 stderr）；
// errorMonitor、captureRejectionSymbol 与 EventTarget 分支未提供；events.on 无背压/signal。

// 组装 Node 风格类型报错所需的取值描述（不能 import util.inspect，从简）。
function describeValue(value) {
  if (value === null) return 'null';
  const t = typeof value;
  if (t === 'object') return 'an instance of ' + ((value.constructor && value.constructor.name) || 'Object');
  if (t === 'string') return 'type string (' + JSON.stringify(value) + ')';
  return 'type ' + t + ' (' + String(value) + ')';
}

function invalidArgType(name, expected, actual) {
  const err = new TypeError(
    'The "' + name + '" argument must be of type ' + expected + '. Received ' + describeValue(actual)
  );
  err.code = 'ERR_INVALID_ARG_TYPE';
  return err;
}

function checkListener(listener) {
  if (typeof listener !== 'function') throw invalidArgType('listener', 'function', listener);
}

// once 包装器：先把自己从监听器表里摘掉，再以 emitter 为 this 调原函数；.listener 暴露原函数。
function wrapOnce(emitter, name, listener) {
  let fired = false;
  function wrapper(...args) {
    if (fired) return undefined;
    fired = true;
    emitter.removeListener(name, wrapper);
    return listener.apply(this, args);
  }
  wrapper.listener = listener;
  return wrapper;
}

function unwrap(listener) {
  return typeof listener.listener === 'function' ? listener.listener : listener;
}

class EventEmitter {
  constructor(options) {
    this._events = new Map(); // eventName(string|symbol) -> [listener 或 once 包装器]
    this._maxListeners = undefined; // undefined：跟随模块级 defaultMaxListeners
    this.captureRejections = !!(options && options.captureRejections); // 接受但忽略语义
  }

  _add(name, listener, prepend) {
    const list = this._events.get(name);
    if (list === undefined) this._events.set(name, [listener]);
    else if (prepend) list.unshift(listener);
    else list.push(listener);
    // 超过 getMaxListeners() 时 Node 会 process.emitWarning；这里刻意静默。
  }

  on(name, listener) { checkListener(listener); this._add(name, listener, false); return this; }

  prependListener(name, listener) { checkListener(listener); this._add(name, listener, true); return this; }

  once(name, listener) { checkListener(listener); this._add(name, wrapOnce(this, name, listener), false); return this; }

  prependOnceListener(name, listener) {
    checkListener(listener); this._add(name, wrapOnce(this, name, listener), true); return this;
  }

  removeListener(name, listener) {
    checkListener(listener);
    const list = this._events.get(name);
    if (list !== undefined) {
      // 从后往前找：off(name, fn) 移除「最近添加」的那个匹配项。
      for (let i = list.length - 1; i >= 0; i -= 1) {
        if (list[i] === listener || list[i].listener === listener) {
          list.splice(i, 1);
          if (list.length === 0) this._events.delete(name);
          break;
        }
      }
    }
    return this;
  }

  removeAllListeners(name) {
    if (arguments.length === 0 || name === undefined) this._events.clear();
    else this._events.delete(name);
    return this;
  }

  emit(name, ...args) {
    const list = this._events.get(name);
    if (name === 'error' && !list) {
      // 与 Node 一致：无监听器的 error 事件直接抛出；Error 原样抛，其余包成 Error。
      const er = args[0];
      if (er instanceof Error) throw er;
      const err = new Error('Unhandled error. (' + describeValue(er) + ')');
      err.code = 'ERR_UNHANDLED_ERROR';
      err.context = er;
      throw err;
    }
    if (!list || list.length === 0) return false;
    const snapshot = list.slice(); // 快照：回调里增删监听器不影响本轮（同 Node）
    for (let i = 0; i < snapshot.length; i += 1) snapshot[i].apply(this, args);
    return true;
  }

  listeners(name) {
    const list = this._events.get(name);
    return list === undefined ? [] : list.map(unwrap);
  }

  rawListeners(name) {
    const list = this._events.get(name);
    return list === undefined ? [] : list.slice();
  }

  listenerCount(name, listener) {
    const list = this._events.get(name);
    if (list === undefined) return 0;
    if (listener === undefined) return list.length;
    checkListener(listener);
    let count = 0;
    for (let i = 0; i < list.length; i += 1) {
      if (list[i] === listener || list[i].listener === listener) count += 1;
    }
    return count;
  }

  eventNames() { return Array.from(this._events.keys()); }

  setMaxListeners(n) {
    if (typeof n !== 'number' || Number.isNaN(n)) throw invalidArgType('n', 'number', n);
    if (n < 0) { // 与 Node 一致：负数 RangeError（Infinity 合法）
      const err = new RangeError('The value of "n" is out of range. It must be >= 0. Received ' + n);
      err.code = 'ERR_OUT_OF_RANGE';
      throw err;
    }
    this._maxListeners = n;
    return this;
  }

  getMaxListeners() {
    return this._maxListeners === undefined ? defaultMaxListeners : this._maxListeners;
  }
}

// 别名共享同一函数引用（Node 中 on === addListener、off === removeListener）。
EventEmitter.prototype.addListener = EventEmitter.prototype.on;
EventEmitter.prototype.off = EventEmitter.prototype.removeListener;

let defaultMaxListeners = 10;

// 让 EventEmitter.defaultMaxListeners 与模块导出共用同一个动态绑定。
Object.defineProperty(EventEmitter, 'defaultMaxListeners', {
  enumerable: true,
  get() { return defaultMaxListeners; },
  set(value) { defaultMaxListeners = value; },
});
EventEmitter.EventEmitter = EventEmitter;

// events.once(emitter, name)：把「下一次事件」包成 Promise，参数数组作为 resolve 值。
function once(emitter, name) {
  if (emitter === null || typeof emitter !== 'object' || typeof emitter.once !== 'function') {
    throw invalidArgType('emitter', 'EventEmitter', emitter);
  }
  if (name === undefined) {
    const err = new TypeError('The "event" argument must be specified');
    err.code = 'ERR_MISSING_ARGS';
    throw err;
  }
  return new Promise((resolve, reject) => {
    let settled = false;
    function finish(err, args) {
      if (settled) return;
      settled = true;
      emitter.removeListener(name, onEvent);
      if (name !== 'error') emitter.removeListener('error', onError);
      if (err) reject(err); else resolve(args);
    }
    function onEvent(...args) { finish(null, args); }
    function onError(err) { finish(err, null); }
    emitter.once(name, onEvent);
    if (name !== 'error') emitter.once('error', onError); // error 也让 Promise 拒绝
  });
}

// events.on(emitter, name)：事件流包成 async iterator。调用时立即挂监听（同 Node），
// 迭代器 return()/throw() 时卸载；未消费事件先入队。不做背压与 signal 取消。
function on(emitter, name) {
  const queue = [];
  let failure = null;
  let wake = null;
  const notify = () => { if (wake !== null) { const resume = wake; wake = null; resume(); } };
  const onEvent = (...args) => { queue.push(args); notify(); };
  const onError = (err) => { failure = err || new Error('Unhandled error'); notify(); };
  const cleanup = () => {
    emitter.removeListener(name, onEvent);
    if (name !== 'error') emitter.removeListener('error', onError);
  };
  emitter.on(name, onEvent);
  if (name !== 'error') emitter.on('error', onError);
  const iterator = {
    next() {
      return new Promise((resolve, reject) => {
        const step = () => {
          if (queue.length > 0) resolve({ value: queue.shift(), done: false });
          else if (failure !== null) reject(failure);
          else wake = step;
        };
        step();
      });
    },
    return() { cleanup(); return Promise.resolve({ value: undefined, done: true }); },
    throw(err) { cleanup(); return Promise.reject(err); },
    [Symbol.asyncIterator]() { return iterator; },
  };
  return iterator;
}

// getEventListeners(emitter, name)：返回 { eventName: [原始监听器] }。
function getEventListeners(emitter, name) {
  const out = {};
  const names = name === undefined ? emitter.eventNames() : Array.isArray(name) ? name : [name];
  for (let i = 0; i < names.length; i += 1) {
    const list = emitter.listeners(names[i]);
    if (list.length > 0) out[names[i]] = list;
  }
  return out;
}

// 兼容静态形态：EventEmitter.listenerCount(emitter, name, listener?)。
function listenerCount(emitter, name, listener) {
  return emitter.listenerCount(name, listener);
}

EventEmitter.getEventListeners = getEventListeners;
EventEmitter.listenerCount = listenerCount;

export { EventEmitter, once, on, getEventListeners, listenerCount, defaultMaxListeners };

export default EventEmitter;
