// node:stream/web —— ada-go 宿主自建的 WHATWG Streams 子集（虚拟 ES 模块，moejs 装载）。
//
// Node 的 `stream` 模块不存在（12 §6.12.2：没有 Node 的 stream 模块；pipe 用
// pipeTo/pipeThrough）。这里提供 ReadableStream / WritableStream / TransformStream
// 与两种排队策略，够插件做「读一块、转一下、写出去」。
//
// 有意简化（文件头声明，便于对账）：
//   - 背压只用 desiredSize/highWaterMark 计数表示，没有真正暂停上游的调度；
//   - pipeTo 的 preventClose/preventAbort/preventCancel 接受但不改变行为；
//   - tee() 用「两个队列各自缓冲」实现，不保证两侧消费节奏；
//   - 没有 TextEncoderStream/TextDecoderStream（宿主没有全局 TextEncoder）、
//     没有 byte streams（type: "bytes"）、没有 transferable streams；
//   - 没有 DOMException 时用同名 fallback 类（只有 name/message）。

const DOMExceptionImpl =
  typeof DOMException !== "undefined"
    ? DOMException
    : class DOMException extends Error {
        constructor(message, name) {
          super(message);
          this.name = name || "Error";
        }
      };

function typeError(message) {
  return new TypeError(message);
}

function queueMicrotaskish(fn) {
  if (typeof queueMicrotask === "function") queueMicrotask(fn);
  else Promise.resolve().then(fn);
}

// ---- ReadableStream --------------------------------------------------------

class ReadableStreamDefaultController {
  constructor(stream) {
    this._stream = stream;
    this.desiredSize = stream._highWaterMark;
  }

  enqueue(chunk) {
    if (this._stream._state !== "readable") {
      throw new TypeError("Cannot enqueue into a stream that is not readable");
    }
    this._stream._enqueue(chunk);
  }

  close() {
    if (this._stream._state !== "readable") return;
    this._stream._state = "closed";
    this._stream._flushPending();
    this._stream._maybeClose();
  }

  error(reason) {
    this._stream._error(reason);
  }
}

export class ReadableStream {
  constructor(underlyingSource, strategy) {
    const src = underlyingSource || {};
    this._source = src;
    this._queue = [];
    this._state = "readable";
    this._error = undefined;
    this._readers = [];
    this._pending = [];
    this._pulling = false;
    this._closeRequested = false;
    this._highWaterMark = (strategy && strategy.highWaterMark) || (strategy && strategy.size ? 1 : 1);
    this._controller = new ReadableStreamDefaultController(this);
    this._locked = false;
    if (typeof src.start === "function") {
      const maybe = src.start(this._controller);
      if (maybe && typeof maybe.then === "function") maybe.then(() => this._maybePull(), () => {});
    }
  }

  get locked() {
    return this._locked;
  }

  _enqueue(chunk) {
    this._queue.push(chunk);
    this._flushPending();
  }

  _error(reason) {
    if (this._state === "errored") return;
    this._state = "errored";
    this._error = reason;
    while (this._pending.length > 0) {
      const p = this._pending.shift();
      p.reject(reason);
    }
  }

  _flushPending() {
    while (this._pending.length > 0 && (this._queue.length > 0 || this._state !== "readable")) {
      const p = this._pending.shift();
      if (this._queue.length > 0) {
        p.resolve({ value: this._queue.shift(), done: false });
      } else if (this._state === "closed") {
        p.resolve({ value: undefined, done: true });
      } else {
        p.reject(this._error);
      }
    }
    this._controller.desiredSize = this._highWaterMark - this._queue.length;
  }

  _maybeClose() {
    if (this._state === "closed" && this._queue.length === 0) this._flushPending();
  }

  _maybePull() {
    if (this._pulling) return;
    if (this._state !== "readable") return;
    if (typeof this._source.pull !== "function") return;
    if (this._queue.length > 0) return;
    this._pulling = true;
    let maybe;
    try {
      maybe = this._source.pull(this._controller);
    } catch (e) {
      this._pulling = false;
      this._error(e);
      return;
    }
    Promise.resolve(maybe).then(
      () => {
        this._pulling = false;
      },
      (e) => {
        this._pulling = false;
        this._error(e);
      },
    );
  }

  getReader() {
    if (this._locked) throw typeError("ReadableStream is already locked to a reader");
    this._locked = true;
    const stream = this;
    return {
      read() {
        if (stream._state === "errored") return Promise.reject(stream._error);
        if (stream._queue.length > 0) {
          const value = stream._queue.shift();
          stream._controller.desiredSize = stream._highWaterMark - stream._queue.length;
          return Promise.resolve({ value, done: false });
        }
        if (stream._state === "closed") return Promise.resolve({ value: undefined, done: true });
        return new Promise((resolve, reject) => {
          stream._pending.push({ resolve, reject });
          stream._maybePull();
        });
      },
      releaseLock() {
        stream._locked = false;
      },
      cancel(reason) {
        return stream.cancel(reason);
      },
      get closed() {
        return stream._closedPromise || Promise.resolve();
      },
    };
  }

  cancel(reason) {
    if (this._state === "errored") return Promise.reject(this._error);
    this._state = "closed";
    this._queue.length = 0;
    this._flushPending();
    if (typeof this._source.cancel === "function") {
      try {
        return Promise.resolve(this._source.cancel(reason)).then(() => undefined);
      } catch (e) {
        return Promise.reject(e);
      }
    }
    return Promise.resolve();
  }

  pipeTo(destination, options) {
    // options 的 prevent* 只接受不改变行为（文件头已声明）。
    void options;
    const reader = this.getReader();
    const writer = destination.getWriter();
    const pump = () =>
      reader.read().then(({ value, done }) => {
        if (done) {
          return writer.close().then(() => undefined);
        }
        return writer.write(value).then(pump);
      });
    return pump().then(
      () => undefined,
      (err) => writer.abort(err).then(() => Promise.reject(err)),
    );
  }

  pipeThrough(transform, options) {
    this.pipeTo(transform.writable, options).catch(() => {});
    return transform.readable;
  }

  tee() {
    const a = [];
    const b = [];
    const source = this;
    const make = (own, other) => {
      let done = false;
      let err;
      const pull = (controller) => {
        if (own.length > 0) {
          controller.enqueue(own.shift());
          return Promise.resolve();
        }
        if (done) {
          controller.close();
          return Promise.resolve();
        }
        if (err !== undefined) {
          controller.error(err);
          return Promise.resolve();
        }
        return Promise.resolve();
      };
      const stream = new ReadableStream({
        start(controller) {
          source._teeHooks = source._teeHooks || [];
          source._teeHooks.push({ own, other, controller, setDone: (e) => ((done = true), (err = e)) });
        },
        pull,
      });
      return stream;
    };
    // 简单策略：两个分支共享一个后台读取循环，读到就分别入队。
    if (!source._teePump) {
      source._teePump = true;
      const reader = source.getReader();
      const distribute = () => {
        reader.read().then(
          ({ value, done }) => {
            const hooks = source._teeHooks || [];
            for (const h of hooks) {
              if (done) h.setDone(undefined);
              else {
                h.own.push(value);
                h.other.push(value);
              }
            }
            for (const h of hooks) {
              if (h.own.length > 0) h.controller.enqueue(h.own.shift());
            }
            if (!done) distribute();
            else for (const h of hooks) h.controller.close();
          },
          (e) => {
            for (const h of source._teeHooks || []) h.setDone(e);
          },
        );
      };
      distribute();
    }
    return [make(a, b), make(b, a)];
  }

  values(options) {
    void options;
    const reader = this.getReader();
    return {
      next() {
        return reader.read().then(({ value, done }) => (done ? { value: undefined, done: true } : { value, done: false }));
      },
      return() {
        reader.releaseLock();
        return Promise.resolve({ value: undefined, done: true });
      },
      [Symbol.asyncIterator]() {
        return this;
      },
    };
  }

  [Symbol.asyncIterator]() {
    return this.values();
  }

  static from(iterable) {
    const iterator = iterable[Symbol.iterator] ? iterable[Symbol.iterator]() : iterable[Symbol.asyncIterator]();
    return new ReadableStream({
      async pull(controller) {
        const { value, done } = await iterator.next();
        if (done) controller.close();
        else controller.enqueue(value);
      },
    });
  }
}

// ---- WritableStream --------------------------------------------------------

export class WritableStream {
  constructor(underlyingSink, strategy) {
    const sink = underlyingSink || {};
    this._sink = sink;
    this._state = "writable";
    this._error = undefined;
    this._chain = Promise.resolve();
    this._locked = false;
    this._highWaterMark = (strategy && strategy.highWaterMark) || 1;
    if (typeof sink.start === "function") {
      try {
        const maybe = sink.start({});
        if (maybe && typeof maybe.then === "function") this._chain = maybe;
      } catch (e) {
        this._error = e;
        this._state = "errored";
      }
    }
  }

  get locked() {
    return this._locked;
  }

  _enqueue(op) {
    const next = this._chain.then(op, op);
    // 失败不阻塞后续 close/abort 的清理，但要把状态记下来。
    this._chain = next.catch((e) => {
      if (this._state === "writable") {
        this._state = "errored";
        this._error = e;
      }
    });
    return next;
  }

  getWriter() {
    if (this._locked) throw typeError("WritableStream is already locked to a writer");
    this._locked = true;
    const stream = this;
    return {
      write(chunk) {
        if (stream._state !== "writable") {
          return Promise.reject(stream._error || new DOMExceptionImpl("The stream is not writable", "InvalidStateError"));
        }
        if (typeof stream._sink.write !== "function") return Promise.resolve();
        return stream._enqueue(() => stream._sink.write(chunk));
      },
      close() {
        if (stream._state !== "writable") return Promise.resolve();
        stream._state = "closed";
        if (typeof stream._sink.close !== "function") return Promise.resolve();
        return stream._enqueue(() => stream._sink.close());
      },
      abort(reason) {
        stream._state = "errored";
        stream._error = reason;
        if (typeof stream._sink.abort !== "function") return Promise.resolve();
        return stream._enqueue(() => stream._sink.abort(reason));
      },
      releaseLock() {
        stream._locked = false;
      },
      get desiredSize() {
        return stream._highWaterMark;
      },
      get closed() {
        return stream._chain.then(() => undefined);
      },
      ready() {
        return Promise.resolve();
      },
    };
  }

  abort(reason) {
    return this.getWriter().abort(reason);
  }
}

// ---- TransformStream -------------------------------------------------------

export class TransformStream {
  constructor(transformer) {
    const t = transformer || {};
    let readableController = null;
    let closed = false;

    const readable = new ReadableStream({
      start(controller) {
        readableController = controller;
        if (typeof t.start === "function") {
          const maybe = t.start(controller);
          if (maybe && typeof maybe.then === "function") maybe.catch(() => {});
        }
      },
    });

    const controller = {
      enqueue(chunk) {
        if (!closed) readableController.enqueue(chunk);
      },
      error(reason) {
        if (closed) return;
        closed = true;
        readableController.error(reason);
      },
      terminate() {
        if (closed) return;
        closed = true;
        readableController.close();
      },
    };

    const writable = new WritableStream({
      async write(chunk) {
        if (closed) return;
        if (typeof t.transform === "function") {
          await t.transform(chunk, controller);
        } else {
          controller.enqueue(chunk);
        }
      },
      async close() {
        if (typeof t.flush === "function") await t.flush(controller);
        if (!closed) {
          closed = true;
          readableController.close();
        }
      },
      async abort(reason) {
        if (!closed) {
          closed = true;
          readableController.error(reason);
        }
      },
    });

    this.readable = readable;
    this.writable = writable;
  }
}
// ---- 排队策略 --------------------------------------------------------------

export class ByteLengthQueuingStrategy {
  constructor(options) {
    this.highWaterMark = (options && options.highWaterMark) || 0;
  }

  size(chunk) {
    return chunk ? chunk.byteLength : 0;
  }
}

export class CountQueuingStrategy {
  constructor(options) {
    this.highWaterMark = (options && options.highWaterMark) || 0;
  }

  size() {
    return 1;
  }
}

export default {
  ReadableStream,
  WritableStream,
  TransformStream,
  ByteLengthQueuingStrategy,
  CountQueuingStrategy,
};
