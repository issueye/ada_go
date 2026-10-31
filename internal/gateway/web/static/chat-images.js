/*
 * 聊天/新建任务输入框的图片附件（粘贴/选择/拖拽 + 缩略图预览与移除）。
 *
 * 文件名固定为 images 字段：表单 multipart 提交后由基座 handleTurn /
 * handleCreateThread 校验（类型/单张 8MB/最多 4 张）并转成
 * turn.start / turn.steer 的 images 参数。
 * 脚本缺席时退化为原生 file input + 表单提交，功能仍然可用。
 */
(function () {
  "use strict";

  var inputs = document.querySelectorAll('input[name="images"]');
  Array.prototype.forEach.call(inputs, function (input) {
    var form = input.closest("form");
    if (!form || form.getAttribute("data-image-bound") === "1") return;
    form.setAttribute("data-image-bound", "1");

    var holder = form.querySelector("[data-image-thumbs]");
    var textarea = form.querySelector(".ada-composer__input");

    function render() {
      if (!holder) return;
      holder.textContent = "";
      var files = Array.from(input.files || []);
      if (!files.length) {
        holder.hidden = true;
        return;
      }
      holder.hidden = false;
      files.forEach(function (file, index) {
        var item = document.createElement("span");
        item.className = "ada-composer__thumb";
        var img = document.createElement("img");
        img.alt = file.name || "图片";
        // 和消息里的缩略图共用同一个查看浮层（见文件末尾的 data-image-viewer 处理）。
        img.setAttribute("data-image-viewer", "");
        // CSP img-src 只放行 self/data:——用 FileReader 读成 data URL，
        // 不用 blob: URL（createObjectURL 会被拦成破图）。
        var reader = new FileReader();
        reader.onload = function () {
          img.src = String(reader.result || "");
        };
        reader.readAsDataURL(file);
        item.appendChild(img);
        var remove = document.createElement("button");
        remove.type = "button";
        remove.className = "ada-composer__thumb-remove";
        remove.setAttribute("aria-label", "移除图片 " + (index + 1));
        remove.textContent = "×";
        remove.addEventListener("click", function () {
          var dt = new DataTransfer();
          Array.from(input.files || []).forEach(function (f, i) {
            if (i !== index) dt.items.add(f);
          });
          input.files = dt.files;
          render();
        });
        item.appendChild(remove);
        holder.appendChild(item);
      });
    }

    function addFiles(fileList) {
      var incoming = Array.from(fileList || []).filter(function (f) {
        return f && f.type && f.type.indexOf("image/") === 0;
      });
      if (!incoming.length) return;
      var dt = new DataTransfer();
      Array.from(input.files || []).forEach(function (f) {
        dt.items.add(f);
      });
      incoming.forEach(function (f) {
        dt.items.add(f);
      });
      input.files = dt.files;
      render();
    }

    input.addEventListener("change", render);
    if (textarea) {
      textarea.addEventListener("paste", function (event) {
        var files = event.clipboardData && event.clipboardData.files;
        if (files && files.length) {
          event.preventDefault();
          addFiles(files);
        }
      });
      textarea.addEventListener("dragover", function (event) {
        event.preventDefault();
      });
      textarea.addEventListener("drop", function (event) {
        var files = event.dataTransfer && event.dataTransfer.files;
        if (files && files.length) {
          event.preventDefault();
          addFiles(files);
        }
      });
    }
  });

  // ── 图片查看浮层 ──────────────────────────────────────────────────────
  // 缩略图（消息里与输入框里）点开在当前页放大查看，不开新标签。
  // 事件委托到 document：实时流/htmx 换掉的节点不需要重新绑定。
  var activeViewer = null;
  var lastTrigger = null;

  function closeViewer() {
    if (!activeViewer) return;
    activeViewer.remove();
    activeViewer = null;
    document.body.classList.remove("ada-image-viewer-open");
    if (lastTrigger && lastTrigger.isConnected) lastTrigger.focus();
    lastTrigger = null;
  }

  function openViewer(trigger) {
    var img = trigger.tagName === "IMG" ? trigger : trigger.querySelector("img");
    if (!img || !img.src || img.src.indexOf("data:") !== 0) return;
    closeViewer();
    lastTrigger = trigger;
    var viewer = document.createElement("div");
    viewer.className = "ada-image-viewer";
    viewer.setAttribute("role", "dialog");
    viewer.setAttribute("aria-modal", "true");
    viewer.setAttribute("aria-label", "图片预览");

    var big = document.createElement("img");
    big.src = img.src;
    big.alt = img.alt || "图片";
    viewer.appendChild(big);

    var close = document.createElement("button");
    close.type = "button";
    close.className = "ada-image-viewer__close";
    close.setAttribute("aria-label", "关闭图片预览");
    close.textContent = "×";
    close.addEventListener("click", closeViewer);
    viewer.appendChild(close);

    viewer.addEventListener("click", function (event) {
      // 点遮罩关闭；点图片本身不关。
      if (event.target === viewer) closeViewer();
    });

    document.body.classList.add("ada-image-viewer-open");
    document.body.appendChild(viewer);
    activeViewer = viewer;
    close.focus();
  }

  document.addEventListener("click", function (event) {
    var trigger = event.target.closest && event.target.closest("[data-image-viewer]");
    if (!trigger) return;
    event.preventDefault();
    openViewer(trigger);
  });

  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape" && activeViewer) {
      event.preventDefault();
      closeViewer();
    }
  });
})();
