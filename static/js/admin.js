/* 管理后台脚本 — 无外部依赖 */
(function () {
  "use strict";

  var THEME_KEY = "blog-theme";

  /* ---------- 主题切换 ---------- */
  function initTheme() {
    var btn = document.getElementById("theme-toggle");
    if (!btn) return;

    if (!document.documentElement.getAttribute("data-theme")) {
      document.documentElement.setAttribute("data-theme", "dark");
    }

    btn.addEventListener("click", function () {
      var next = document.documentElement.getAttribute("data-theme") === "light" ? "dark" : "light";
      document.documentElement.setAttribute("data-theme", next);
      try { localStorage.setItem(THEME_KEY, next); } catch (e) { /* 忽略 */ }
    });
  }

  /* ---------- 移动端侧边栏 ---------- */
  function initMobileNav() {
    var toggle = document.getElementById("admin-menu-toggle");
    var scrim = document.getElementById("nav-scrim");
    if (!toggle) return;

    function close() {
      document.body.classList.remove("is-nav-open");
      toggle.setAttribute("aria-expanded", "false");
      if (scrim) scrim.hidden = true;
    }

    toggle.addEventListener("click", function () {
      var open = !document.body.classList.contains("is-nav-open");
      document.body.classList.toggle("is-nav-open", open);
      toggle.setAttribute("aria-expanded", String(open));
      if (scrim) scrim.hidden = !open;
    });

    if (scrim) scrim.addEventListener("click", close);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") close();
    });
  }

  /* ---------- 危险操作二次确认 ---------- */
  function initConfirm() {
    document.addEventListener("submit", function (e) {
      var form = e.target;
      if (!form || !form.dataset || !form.dataset.confirm) return;
      if (!window.confirm(form.dataset.confirm)) {
        e.preventDefault();
      }
    });
  }

  /* ---------- Markdown 编辑器 ---------- */
  function initEditor() {
    var textarea = document.getElementById("editor-content");
    var preview = document.getElementById("editor-preview");
    var stats = document.getElementById("editor-stats");
    if (!textarea || !preview) return;

    var endpoint = "/admin/preview";
    var csrfInput = document.querySelector('#editor-form input[name="_csrf"]');
    var csrf = csrfInput ? csrfInput.value : "";

    var timer = null;
    var lastValue = textarea.value;
    var dirty = false;

    /* --- 预览 --- */
    function renderPreview() {
      var markdown = textarea.value;
      if (!markdown.trim()) {
        preview.innerHTML = "";
        preview.classList.add("is-empty");
        if (stats) stats.textContent = "0 字";
        return;
      }

      fetch(endpoint, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrf
        },
        body: JSON.stringify({ markdown: markdown })
      })
        .then(function (res) {
          if (!res.ok) throw new Error("HTTP " + res.status);
          return res.json();
        })
        .then(function (data) {
          preview.innerHTML = data.html;
          preview.classList.remove("is-empty");
          if (stats) {
            stats.textContent = data.words + " 字 · " + markdown.split("\n").length + " 行";
          }
          enhancePreview();
        })
        .catch(function () {
          preview.innerHTML = '<p style="color:var(--text-muted)">预览加载失败，请检查网络或重新登录。</p>';
          preview.classList.remove("is-empty");
        });
    }

    function schedulePreview() {
      if (timer) clearTimeout(timer);
      timer = setTimeout(renderPreview, 220);
    }

    /* --- 预览中的标题锚点与代码复制 --- */
    function enhancePreview() {
      var headings = preview.querySelectorAll("h1[id], h2[id], h3[id], h4[id]");
      Array.prototype.forEach.call(headings, function (h) {
        if (h.querySelector(".heading-anchor")) return;
        var link = document.createElement("a");
        link.className = "heading-anchor";
        link.href = "#" + h.id;
        link.textContent = "#";
        link.setAttribute("aria-label", "标题锚点");
        h.appendChild(link);
      });
    }

    /* --- 工具栏 --- */
    var actions = {
      h2: function (sel) { return wrapLines(sel, "## ", ""); },
      bold: function (sel) { return wrap(sel, "**", "**", "粗体"); },
      italic: function (sel) { return wrap(sel, "*", "*", "斜体"); },
      strike: function (sel) { return wrap(sel, "~~", "~~", "删除线"); },
      code: function (sel) { return wrap(sel, "`", "`", "代码"); },
      codeblock: function (sel) { return wrapLines(sel, "```\n", "\n```"); },
      quote: function (sel) { return wrapLines(sel, "> ", ""); },
      ul: function (sel) { return wrapLines(sel, "- ", ""); },
      ol: function (sel) { return wrapLines(sel, "1. ", ""); },
      link: function (sel) { return wrap(sel, "[", "](https://)", "链接文字"); },
      table: function () {
        return {
          text: "| 列 1 | 列 2 |\n| --- | --- |\n| 内容 | 内容 |\n",
          selectFrom: 2,
          selectTo: 6
        };
      },
      hr: function () { return { text: "\n---\n", selectFrom: 5, selectTo: 5 }; }
    };

    function wrap(sel, before, after, placeholder) {
      var text = sel.text || placeholder;
      return {
        text: before + text + after,
        selectFrom: before.length,
        selectTo: before.length + text.length
      };
    }

    function wrapLines(sel, prefix, suffix) {
      if (!sel.text) {
        return { text: prefix, selectFrom: prefix.length, selectTo: prefix.length };
      }
      var lines = sel.text.split("\n");
      var out = lines.map(function (l) { return prefix + l; }).join("\n");
      if (suffix) out += suffix;
      return { text: out, selectFrom: prefix.length, selectTo: prefix.length + lines[0].length };
    }

    function applyAction(name) {
      var fn = actions[name];
      if (!fn) return;

      var start = textarea.selectionStart;
      var end = textarea.selectionEnd;
      var value = textarea.value;
      var sel = { text: value.slice(start, end) };

      var result = fn(sel);
      textarea.value = value.slice(0, start) + result.text + value.slice(end);

      var newStart = start + result.selectFrom;
      var newEnd = start + result.selectTo;
      textarea.focus();
      textarea.setSelectionRange(newStart, newEnd);

      onInput();
    }

    Array.prototype.forEach.call(document.querySelectorAll(".md-btn[data-md]"), function (btn) {
      btn.addEventListener("click", function () {
        applyAction(btn.dataset.md);
      });
    });

    /* --- 输入处理 --- */
    function onInput() {
      if (textarea.value !== lastValue) dirty = true;
      schedulePreview();
    }

    textarea.addEventListener("input", onInput);

    /* Tab 缩进 / Shift+Tab 反缩进 */
    textarea.addEventListener("keydown", function (e) {
      if (e.key === "Tab") {
        e.preventDefault();
        var start = textarea.selectionStart;
        var end = textarea.selectionEnd;
        var value = textarea.value;

        if (e.shiftKey) {
          var lineStart = value.lastIndexOf("\n", start - 1) + 1;
          if (value.slice(lineStart, lineStart + 2) === "  ") {
            textarea.value = value.slice(0, lineStart) + value.slice(lineStart + 2);
            textarea.setSelectionRange(Math.max(lineStart, start - 2), Math.max(lineStart, end - 2));
          }
        } else {
          textarea.value = value.slice(0, start) + "  " + value.slice(end);
          textarea.setSelectionRange(start + 2, start + 2);
        }
        onInput();
        return;
      }

      /* Ctrl/Cmd + B / I / K 快捷键 */
      if (!(e.ctrlKey || e.metaKey)) return;
      var key = e.key.toLowerCase();
      if (key === "b") { e.preventDefault(); applyAction("bold"); }
      else if (key === "i") { e.preventDefault(); applyAction("italic"); }
      else if (key === "k") { e.preventDefault(); applyAction("link"); }
      else if (key === "s") {
        e.preventDefault();
        var form = document.getElementById("editor-form");
        if (form) form.requestSubmit();
      }
    });

    /* --- 离开页面前提醒 --- */
    window.addEventListener("beforeunload", function (e) {
      if (!dirty) return;
      e.preventDefault();
      e.returnValue = "";
    });

    var form = document.getElementById("editor-form");
    if (form) {
      form.addEventListener("submit", function () { dirty = false; });
    }

    /* 初始渲染 */
    if (textarea.value.trim()) {
      renderPreview();
    } else {
      preview.classList.add("is-empty");
      if (stats) stats.textContent = "0 字";
    }
  }

  /* ---------- 登录验证码刷新 ---------- */
  function initCaptcha() {
    var button = document.getElementById("captcha-refresh");
    var img = document.getElementById("captcha-img");
    if (!button || !img) return;

    var input = document.getElementById("captcha-input");

    function refresh() {
      button.classList.add("is-loading");

      // 每次换一个参数，绕开浏览器缓存拿到全新的一张图
      var base = img.getAttribute("src").split("?")[0];
      img.setAttribute("src", base + "?t=" + Date.now().toString(36));

      img.onload = function () {
        button.classList.remove("is-loading");
      };
      img.onerror = function () {
        button.classList.remove("is-loading");
      };

      if (input) {
        input.value = "";
        input.focus();
      }
    }

    button.addEventListener("click", refresh);
  }

  /* ---------- 登录滑块验证 ---------- */
  function initSlider() {
    var root = document.getElementById("slider-captcha");
    if (!root) return;

    var stage = document.getElementById("slider-stage");
    var bg = document.getElementById("slider-bg");
    var piece = document.getElementById("slider-piece");
    var track = document.getElementById("slider-track");
    var handle = document.getElementById("slider-handle");
    var fill = document.getElementById("slider-fill");
    var status = document.getElementById("slider-status");
    var refreshBtn = document.getElementById("slider-refresh");
    var ticketInput = document.getElementById("captcha-ticket");
    var mask = document.getElementById("slider-mask");
    var form = document.getElementById("login-form");

    var csrfInput = form ? form.querySelector('input[name="_csrf"]') : null;
    var csrf = csrfInput ? csrfInput.value : "";

    var challenge = null;
    var naturalWidth = 0;
    var dragging = false;
    var grabOffset = 0;
    var currentX = 0;
    var maxHandleX = 1;
    var startedAt = 0;
    var samples = [];
    var verified = false;

    var MAX_SAMPLES = 120;

    function setStatus(text, kind) {
      status.textContent = text;
      status.className = "field-hint";
      if (kind) status.classList.add(kind);
    }

    function measure() {
      maxHandleX = Math.max(1, track.clientWidth - handle.offsetWidth);
    }

    // 手柄位移（像素）→ 拼图块位移（像素）。
    // 手柄与拼图块宽度一致，两者的可移动范围也相同，所以这里是等比的。
    function pieceOffsetFor(handleX) {
      var maxPiece = Math.max(0, stage.clientWidth - piece.offsetWidth);
      return (handleX / maxHandleX) * maxPiece;
    }

    function applyOffset(handleX) {
      currentX = Math.max(0, Math.min(maxHandleX, handleX));
      handle.style.transform = "translateX(" + currentX + "px)";
      fill.style.width = (currentX / maxHandleX * 100) + "%";
      handle.setAttribute("aria-valuenow", Math.round(currentX / maxHandleX * 100));

      var stageW = stage.clientWidth;
      if (stageW > 0) {
        piece.style.left = (pieceOffsetFor(currentX) / stageW * 100) + "%";
      }
    }

    function resetHandle() {
      applyOffset(0);
    }

    // 换算到「原图像素」——服务端只认这个坐标系
    function toNatural(handleX) {
      var stageW = stage.clientWidth;
      if (stageW <= 0 || naturalWidth <= 0) return 0;
      return Math.round(pieceOffsetFor(handleX) * (naturalWidth / stageW));
    }

    function recordSample() {
      var x = toNatural(currentX);
      if (samples.length === 0 || x !== samples[samples.length - 1]) {
        if (samples.length < MAX_SAMPLES) samples.push(x);
      }
    }

    function load() {
      verified = false;
      ticketInput.value = "";
      mask.hidden = true;
      samples = [];
      root.classList.remove("is-verified");
      handle.classList.remove("is-verified");
      handle.removeAttribute("aria-disabled");
      setStatus("正在加载验证码…", null);

      fetch(root.dataset.challengeUrl, { credentials: "same-origin" })
        .then(function (res) {
          if (!res.ok) throw new Error("HTTP " + res.status);
          return res.json();
        })
        .then(function (data) {
          challenge = data;
          naturalWidth = data.width;

          bg.src = data.background;
          piece.src = data.piece;

          // 全部用百分比定位，窄屏缩放时不会错位
          piece.style.width = (data.pieceSize / data.width * 100) + "%";
          piece.style.top = (data.y / data.height * 100) + "%";
          handle.style.width = (data.pieceSize / data.width * 100) + "%";

          resetHandle();

          // 图片解码完成后再量尺寸，否则 offsetWidth 可能为 0
          var ready = piece.decode ? piece.decode() : Promise.resolve();
          ready.then(function () {
            measure();
            applyOffset(0);
            setStatus("拖动滑块，让拼图块与缺口对齐", null);
          }).catch(function () {
            measure();
            applyOffset(0);
          });
        })
        .catch(function () {
          setStatus("验证码加载失败，请点击「换一张」重试", "is-error");
        });
    }

    function submit(duration) {
      setStatus("正在校验…", null);

      fetch(root.dataset.verifyUrl, {
        method: "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrf
        },
        body: JSON.stringify({
          token: challenge.token,
          x: toNatural(currentX),
          durationMs: duration,
          track: samples
        })
      })
        .then(function (res) {
          return res.json().then(function (data) {
            return { status: res.status, data: data };
          });
        })
        .then(function (r) {
          var data = r.data || {};

          if (data.ok) {
            verified = true;
            ticketInput.value = data.ticket;
            mask.hidden = false;
            root.classList.add("is-verified");
            handle.classList.add("is-verified");
            handle.setAttribute("aria-disabled", "true");
            setStatus("验证通过", "is-ok");
            return;
          }

          setStatus(data.error || "校验失败，请重试", "is-error");

          if (data.code === "expired") {
            // 挑战已作废，必须换一张
            setTimeout(load, 900);
          } else {
            // 只是没对齐，让用户在同一张图上再来一次
            resetHandle();
            samples = [];
          }
        })
        .catch(function () {
          setStatus("网络异常，请点击「换一张」重试", "is-error");
          resetHandle();
        });
    }

    /* --- 指针拖动 --- */
    handle.addEventListener("pointerdown", function (e) {
      if (verified || !challenge) return;

      dragging = true;
      grabOffset = e.clientX - currentX;
      startedAt = Date.now();
      samples = [];
      root.classList.add("is-dragging");
      handle.setPointerCapture(e.pointerId);
      e.preventDefault();
    });

    handle.addEventListener("pointermove", function (e) {
      if (!dragging) return;
      applyOffset(e.clientX - grabOffset);
      recordSample();
    });

    function endDrag() {
      if (!dragging) return;
      dragging = false;
      root.classList.remove("is-dragging");

      var duration = Date.now() - startedAt;

      // 只是轻点了一下，安静复位，不消耗尝试次数
      if (currentX < maxHandleX * 0.12) {
        resetHandle();
        setStatus("请按住滑块拖到缺口位置", null);
        return;
      }

      if (duration < 200) {
        resetHandle();
        setStatus("拖动太快了，请重新拖动", "is-error");
        return;
      }

      submit(duration);
    }

    handle.addEventListener("pointerup", endDrag);
    handle.addEventListener("pointercancel", endDrag);

    /* --- 键盘操作：方向键微调，回车提交 --- */
    handle.addEventListener("keydown", function (e) {
      if (verified || !challenge) return;

      var step = e.shiftKey ? 16 : 4;

      if (e.key === "ArrowRight" || e.key === "ArrowUp") {
        e.preventDefault();
        if (samples.length === 0) startedAt = Date.now();
        applyOffset(currentX + step);
        recordSample();
      } else if (e.key === "ArrowLeft" || e.key === "ArrowDown") {
        e.preventDefault();
        if (samples.length === 0) startedAt = Date.now();
        applyOffset(currentX - step);
        recordSample();
      } else if (e.key === "Home") {
        e.preventDefault();
        applyOffset(0);
      } else if (e.key === "End") {
        e.preventDefault();
        applyOffset(maxHandleX);
        recordSample();
      } else if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        if (samples.length < 5) {
          setStatus("请先用方向键移动滑块，再按回车提交", "is-error");
          return;
        }
        var duration = Date.now() - startedAt;
        submit(Math.max(duration, 400));
      }
    });

    /* --- 窗口尺寸变化后重新测量 --- */
    window.addEventListener("resize", function () {
      measure();
      applyOffset(currentX);
    });

    refreshBtn.addEventListener("click", load);

    if (form) {
      form.addEventListener("submit", function (e) {
        if (verified) return;
        e.preventDefault();
        setStatus("请先完成滑块拼图验证", "is-error");
        handle.focus();
      });
    }

    load();
  }

  function init() {
    initTheme();
    initMobileNav();
    initConfirm();
    initCaptcha();
    initSlider();
    initEditor();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
