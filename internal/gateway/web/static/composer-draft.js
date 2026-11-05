/*
 * 未发送输入草稿。
 *
 * 整页刷新不只发生在用户主动按 F5 时：切换模型/工作区会 reload，开发模式下
 * 源码变化触发桌面壳热更新时也会 reload。composer 里的文字如果只存在 DOM 里，
 * 这些刷新会把用户刚写的内容直接抹掉。
 *
 * 这里只做一件事：给带 data-draft-key 的 textarea 做「输入即存、加载即还原」。
 * 提交成功后删除草稿；页面卸载前把还在防抖窗口里的最后一次输入同步落盘。
 * 图片附件是 File 对象，无法这样序列化，因此只保文本。
 */
(function () {
  'use strict';

  var STORAGE_PREFIX = 'a_da_draft:';
  var SAVE_DELAY = 250;
  var timers = Object.create(null);
  var submitted = Object.create(null);

  function storage() {
    try {
      var s = window.localStorage;
      return s && typeof s.getItem === 'function' ? s : null;
    } catch (err) {
      return null;
    }
  }

  function keyFor(input) {
    var key = input && input.getAttribute ? input.getAttribute('data-draft-key') : '';
    return key ? STORAGE_PREFIX + key : '';
  }

  function read(key) {
    var s = storage();
    if (!s) return '';
    try {
      return s.getItem(key) || '';
    } catch (err) {
      return '';
    }
  }

  function write(key, value) {
    var s = storage();
    if (!s) return;
    try {
      s.setItem(key, value);
    } catch (err) {
      /* 隐私模式或配额满：草稿能力降级，不影响输入 */
    }
  }

  function remove(key) {
    var s = storage();
    if (!s) return;
    try {
      s.removeItem(key);
    } catch (err) {
      /* 同上 */
    }
  }

  function save(input) {
    var key = keyFor(input);
    if (!key) return;
    var value = String(input.value || '');
    if (value === '') remove(key);
    else write(key, value);
  }

  function scheduleSave(input) {
    var key = keyFor(input);
    if (!key) return;
    delete submitted[key];
    if (timers[key]) window.clearTimeout(timers[key]);
    timers[key] = window.setTimeout(function () {
      delete timers[key];
      save(input);
    }, SAVE_DELAY);
  }

  function flushAll() {
    var inputs = document.querySelectorAll('textarea[data-draft-key]');
    for (var i = 0; i < inputs.length; i += 1) {
      var key = keyFor(inputs[i]);
      if (timers[key]) {
        window.clearTimeout(timers[key]);
        delete timers[key];
      }
      // 已经提交、正在离开本页的输入不再写回，否则会在提交后的卸载前
      // 把刚发出的内容重新存成草稿。
      if (!submitted[key]) save(inputs[i]);
    }
  }

  function restore(input) {
    var key = keyFor(input);
    if (!key) return;
    var saved = read(key);
    if (saved === '') return;
    if (String(input.value || '') !== '') {
      // 服务端已预填时以页面为准（例如错误页回显），顺手同步草稿。
      write(key, input.value);
      return;
    }
    input.value = saved;
    try {
      input.setSelectionRange(saved.length, saved.length);
    } catch (err) {
      /* 极老的浏览器/只读控件：光标位置不重要 */
    }
  }

  function bind(input) {
    if (!keyFor(input)) return;
    restore(input);
    input.addEventListener('input', function () {
      scheduleSave(input);
    });
    input.addEventListener('change', function () {
      save(input);
    });
  }

  function boot() {
    var inputs = document.querySelectorAll('textarea[data-draft-key]');
    for (var i = 0; i < inputs.length; i += 1) bind(inputs[i]);

    // 提交成功才清草稿。data-confirm 的拦截在 app.js 的捕获阶段完成，
    // 这里位于冒泡阶段，能看到 defaultPrevented；被确认框拦下的那次提交
    // 不清草稿，用户点「确认」后的 requestSubmit 会再走一次。
    document.addEventListener('submit', function (event) {
      if (event.defaultPrevented) return;
      var form = event.target;
      if (!form || !form.querySelectorAll) return;
      var fields = form.querySelectorAll('textarea[data-draft-key]');
      for (var j = 0; j < fields.length; j += 1) {
        var key = keyFor(fields[j]);
        if (!key) continue;
        submitted[key] = true;
        if (timers[key]) {
          window.clearTimeout(timers[key]);
          delete timers[key];
        }
        remove(key);
      }
    });

    // 热更新/刷新可能就在 250ms 防抖窗口内发生，卸载前同步补一次。
    window.addEventListener('pagehide', flushAll);
    window.addEventListener('beforeunload', flushAll);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
