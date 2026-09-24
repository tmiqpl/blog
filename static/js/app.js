/* 我的博客 — 前端交互脚本（无任何外部依赖） */
(function () {
  "use strict";

  var STORAGE_KEY = "blog-theme";

  /* ---------- 主题切换 ---------- */
  function currentTheme() {
    return document.documentElement.getAttribute("data-theme") === "light" ? "light" : "dark";
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    try {
      localStorage.setItem(STORAGE_KEY, theme);
    } catch (e) { /* 隐私模式下忽略 */ }
  }

  function initThemeToggle() {
    var btn = document.getElementById("theme-toggle");
    if (!btn) return;

    // 首次访问时跟随系统偏好
    if (!document.documentElement.getAttribute("data-theme")) {
      var prefersLight = window.matchMedia &&
        window.matchMedia("(prefers-color-scheme: light)").matches;
      document.documentElement.setAttribute("data-theme", prefersLight ? "light" : "dark");
    }

    btn.addEventListener("click", function () {
      applyTheme(currentTheme() === "dark" ? "light" : "dark");
    });
  }

  /* ---------- 标题锚点 ---------- */
  function initHeadingAnchors() {
    var prose = document.querySelectorAll(".prose");
    if (!prose.length) return;

    Array.prototype.forEach.call(prose, function (block) {
      var headings = block.querySelectorAll("h1[id], h2[id], h3[id], h4[id]");
      Array.prototype.forEach.call(headings, function (h) {
        var link = document.createElement("a");
        link.className = "heading-anchor";
        link.href = "#" + h.id;
        link.textContent = "#";
        link.setAttribute("aria-label", "复制标题链接");
        link.addEventListener("click", function (e) {
          e.preventDefault();
          var url = location.origin + location.pathname + "#" + h.id;
          history.replaceState(null, "", "#" + h.id);
          if (navigator.clipboard) {
            navigator.clipboard.writeText(url).catch(function () {});
          }
          h.scrollIntoView({ behavior: "smooth", block: "start" });
        });
        h.appendChild(link);
      });
    });
  }

  /* ---------- 代码块复制 ---------- */
  function initCodeCopy() {
    var blocks = document.querySelectorAll(".prose pre");
    if (!blocks.length) return;

    Array.prototype.forEach.call(blocks, function (pre) {
      // 包一层容器，让复制按钮不随代码横向滚动
      var wrapper = document.createElement("div");
      wrapper.className = "code-block";
      pre.parentNode.insertBefore(wrapper, pre);
      wrapper.appendChild(pre);

      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "code-copy";
      btn.textContent = "复制";

      btn.addEventListener("click", function () {
        var code = pre.querySelector("code");
        var text = code ? code.innerText : pre.innerText;
        var done = function () {
          btn.textContent = "已复制";
          btn.classList.add("is-done");
          setTimeout(function () {
            btn.textContent = "复制";
            btn.classList.remove("is-done");
          }, 1600);
        };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(done).catch(function () {});
        } else {
          var ta = document.createElement("textarea");
          ta.value = text;
          ta.style.position = "fixed";
          ta.style.opacity = "0";
          document.body.appendChild(ta);
          ta.select();
          try { document.execCommand("copy"); done(); } catch (e) { /* 忽略 */ }
          document.body.removeChild(ta);
        }
      });

      wrapper.appendChild(btn);
    });
  }

  /* ---------- 键盘快捷键 ---------- */
  function initShortcuts() {
    document.addEventListener("keydown", function (e) {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;

      var tag = (e.target.tagName || "").toLowerCase();
      if (tag === "input" || tag === "textarea" || e.target.isContentEditable) return;

      var input = document.querySelector(".search input");
      if (input) {
        e.preventDefault();
        input.focus();
        input.select();
      }
    });
  }

  /* ---------- 文章目录高亮 ---------- */
  function initTOC() {
    var toc = document.querySelector(".toc");
    if (!toc) return;

    var links = Array.prototype.slice.call(toc.querySelectorAll('a[href^="#"]'));
    if (!links.length) return;

    var map = {};
    var headings = [];

    links.forEach(function (link) {
      var id = decodeURIComponent(link.getAttribute("href").slice(1));
      var el = document.getElementById(id);
      if (!el) return;
      map[id] = link;
      headings.push(el);
    });
    if (!headings.length) return;

    var activeID = null;

    function setActive(id) {
      if (activeID === id) return;

      if (activeID && map[activeID]) map[activeID].classList.remove("is-active");
      activeID = id;

      var link = map[activeID];
      if (!link) return;

      link.classList.add("is-active");
      keepVisible(link);
    }

    // 目录本身可能比屏幕高，让当前项始终留在可视区域内
    function keepVisible(link) {
      var box = toc.getBoundingClientRect();
      var item = link.getBoundingClientRect();

      if (item.top < box.top) {
        toc.scrollTop -= box.top - item.top + 8;
      } else if (item.bottom > box.bottom) {
        toc.scrollTop += item.bottom - box.bottom + 8;
      }
    }

    var ticking = false;

    function update() {
      ticking = false;

      // 头部固定高度补偿：标题越过这条线就算「当前」
      var offset = 100;
      var current = headings[0].id;

      for (var i = 0; i < headings.length; i++) {
        if (headings[i].getBoundingClientRect().top - offset <= 0) {
          current = headings[i].id;
        } else {
          break;
        }
      }

      // 滚到底部时高亮最后一项（最后一个小节可能永远越不过基准线）
      if (window.innerHeight + window.scrollY >= document.body.offsetHeight - 4) {
        current = headings[headings.length - 1].id;
      }

      setActive(current);
    }

    function onScroll() {
      if (ticking) return;
      ticking = true;
      requestAnimationFrame(update);
    }

    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onScroll);

    links.forEach(function (link) {
      link.addEventListener("click", function () {
        setActive(decodeURIComponent(link.getAttribute("href").slice(1)));
      });
    });

    update();
  }

  function init() {
    initThemeToggle();
    initHeadingAnchors();
    initCodeCopy();
    initShortcuts();
    initTOC();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
