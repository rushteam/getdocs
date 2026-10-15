(function () {
  "use strict";
  var root = document.documentElement;

  // 主题切换
  document.querySelectorAll("[data-toggle-theme]").forEach(function (b) {
    b.addEventListener("click", function () {
      var next = root.getAttribute("data-theme") === "dark" ? "light" : "dark";
      root.setAttribute("data-theme", next);
      try { localStorage.setItem("theme", next); } catch (e) {}
    });
  });

  // 移动端侧边栏
  document.querySelectorAll("[data-toggle-nav]").forEach(function (b) {
    b.addEventListener("click", function () { document.body.classList.toggle("nav-open"); });
  });
  document.addEventListener("click", function (e) {
    if (document.body.classList.contains("nav-open") && !e.target.closest(".sidebar, [data-toggle-nav]")) {
      document.body.classList.remove("nav-open");
    }
  });

  // "/" 聚焦搜索框
  document.addEventListener("keydown", function (e) {
    if (e.key !== "/" || e.target.closest("input, textarea, [contenteditable], dialog[open]")) return;
    var input = document.querySelector(".search-box input[type=search]");
    if (input) { e.preventDefault(); input.focus(); input.select(); }
  });

  // 侧边栏滚动到当前文档
  var active = document.querySelector(".nav-tree a.active");
  if (active && active.scrollIntoView) active.scrollIntoView({ block: "center" });

  // 标题锚点
  document.querySelectorAll(".markdown-body :is(h1,h2,h3,h4,h5,h6)[id]").forEach(function (h) {
    var a = document.createElement("a");
    a.className = "anchor";
    a.href = "#" + encodeURIComponent(h.id);
    a.setAttribute("aria-label", "链接到本节");
    a.textContent = "#";
    h.appendChild(a);
  });

  // 代码块复制按钮
  document.querySelectorAll(".markdown-body pre:not(.mermaid)").forEach(function (pre) {
    if (!navigator.clipboard) return;
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "copy-btn";
    btn.textContent = "复制";
    btn.addEventListener("click", function () {
      var code = pre.querySelector("code") || pre;
      navigator.clipboard.writeText(code.innerText).then(function () {
        btn.textContent = "已复制";
        setTimeout(function () { btn.textContent = "复制"; }, 1500);
      });
    });
    var wrap = document.createElement("div");
    wrap.className = "code-wrap";
    pre.parentNode.insertBefore(wrap, pre);
    wrap.appendChild(pre);
    wrap.appendChild(btn);
  });

  // 复制文档 markdown 原文，便于粘贴给 AI
  document.querySelectorAll("[data-copy-url]").forEach(function (btn) {
    if (!navigator.clipboard || !window.fetch) return;
    btn.hidden = false;
    var label = btn.textContent;
    btn.addEventListener("click", function () {
      fetch(btn.getAttribute("data-copy-url"))
        .then(function (r) { if (!r.ok) throw new Error(r.status); return r.text(); })
        .then(function (text) { return navigator.clipboard.writeText(text); })
        .then(function () { btn.textContent = "已复制"; }, function () { btn.textContent = "复制失败"; })
        .then(function () { setTimeout(function () { btn.textContent = label; }, 1500); });
    });
  });

  // 本页目录高亮
  var tocLinks = document.querySelectorAll(".toc a");
  if (tocLinks.length && "IntersectionObserver" in window) {
    var byId = {};
    tocLinks.forEach(function (a) { byId[decodeURIComponent(a.hash.slice(1))] = a; });
    var visible = new Set();
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) { en.isIntersecting ? visible.add(en.target.id) : visible.delete(en.target.id); });
      var first = null;
      document.querySelectorAll(".markdown-body [id]").forEach(function (el) {
        if (!first && visible.has(el.id) && byId[el.id]) first = el.id;
      });
      if (first) {
        tocLinks.forEach(function (a) { a.classList.remove("active"); });
        byId[first].classList.add("active");
      }
    }, { rootMargin: "-60px 0px -70% 0px" });
    Object.keys(byId).forEach(function (id) {
      var el = document.getElementById(id);
      if (el) io.observe(el);
    });
  }

  // ---- 自绘控件：不使用浏览器原生的 select / alert / confirm / prompt ----
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }
  var uid = 0;
  var CLOSE_ICON = '<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="m4 4 8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>';
  function closeButton(label) {
    var b = el("button", "icon-btn");
    b.type = "button";
    b.setAttribute("aria-label", label);
    b.innerHTML = CLOSE_ICON;
    return b;
  }

  // 下拉选择：WAI-ARIA combobox（按钮 + listbox），选中值写入隐藏输入框随表单提交。
  document.querySelectorAll("[data-select]").forEach(function (box) {
    var input = box.querySelector("input[type=hidden]");
    var trigger = box.querySelector(".select-trigger");
    var label = trigger.querySelector("span");
    var list = box.querySelector("[role=listbox]");
    var opts = Array.prototype.slice.call(list.querySelectorAll("[role=option]"));
    var active = -1;
    list.id = "select-" + (++uid);
    trigger.setAttribute("aria-controls", list.id);

    function selected() {
      for (var i = 0; i < opts.length; i++) if (opts[i].getAttribute("aria-selected") === "true") return i;
      return 0;
    }
    function setActive(i, scroll) {
      if (active >= 0) opts[active].classList.remove("is-active");
      active = i;
      if (i < 0) { trigger.removeAttribute("aria-activedescendant"); return; }
      opts[i].classList.add("is-active");
      trigger.setAttribute("aria-activedescendant", opts[i].id);
      if (scroll) opts[i].scrollIntoView({ block: "nearest" });
    }
    function open() {
      if (!list.hidden) return;
      list.hidden = false;
      list.classList.remove("is-up");
      // 下方空间不足时向上展开
      var r = list.getBoundingClientRect();
      if (r.bottom > window.innerHeight && box.getBoundingClientRect().top > r.height + 8) list.classList.add("is-up");
      trigger.setAttribute("aria-expanded", "true");
      setActive(selected(), true);
    }
    function close() {
      if (list.hidden) return;
      list.hidden = true;
      trigger.setAttribute("aria-expanded", "false");
      setActive(-1);
    }
    function choose(i) {
      opts.forEach(function (o, j) { o.setAttribute("aria-selected", String(i === j)); });
      input.value = opts[i].getAttribute("data-value");
      label.textContent = opts[i].textContent;
      close();
      input.dispatchEvent(new Event("change", { bubbles: true }));
    }

    opts.forEach(function (o, i) {
      o.id = list.id + "-" + i;
      o.addEventListener("click", function () { choose(i); });
      o.addEventListener("mousemove", function () { if (active !== i) setActive(i, false); });
    });
    // 点选项时保持焦点在触发按钮上，失焦即关闭（覆盖点击外部的情况）
    list.addEventListener("mousedown", function (e) { e.preventDefault(); });
    trigger.addEventListener("blur", close);
    // Safari 点击按钮不会使其获得焦点，因此另外处理点击外部
    document.addEventListener("mousedown", function (e) { if (!box.contains(e.target)) close(); });
    trigger.addEventListener("click", function () { trigger.focus(); list.hidden ? open() : close(); });
    trigger.addEventListener("keyup", function (e) { if (e.key === " ") e.preventDefault(); });
    trigger.addEventListener("keydown", function (e) {
      var last = opts.length - 1;
      if (list.hidden) {
        if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); }
        return;
      }
      switch (e.key) {
        case "ArrowDown": setActive(Math.min(active + 1, last), true); break;
        case "ArrowUp": setActive(Math.max(active - 1, 0), true); break;
        case "Home": case "PageUp": setActive(0, true); break;
        case "End": case "PageDown": setActive(last, true); break;
        case "Enter": case " ": choose(active); break;
        case "Escape": close(); break;
        case "Tab": close(); return;
        default:
          // 按首字符跳转
          if (e.key.length !== 1) return;
          for (var k = 1; k <= opts.length; k++) {
            var j = (active + k) % opts.length;
            if (opts[j].textContent.trim().toLowerCase().indexOf(e.key.toLowerCase()) === 0) { setActive(j, true); break; }
          }
      }
      e.preventDefault();
    });
  });

  // 对话框：替代 window.alert / confirm / prompt，返回 Promise。
  // mode 为 alert 时结果恒为 true；confirm 为是否确认；prompt 为输入内容，取消时为 null。
  function ask(o) {
    return new Promise(function (resolve) {
      var mode = o.mode || "alert";
      var id = "modal-" + (++uid);
      var result = mode === "prompt" ? null : mode === "alert";
      var opener = document.activeElement;

      var dlg = el("dialog", "modal" + (o.danger ? " modal-tone-danger" : ""));
      dlg.setAttribute("aria-labelledby", id + "-title");
      var form = el("form", "modal-inner");
      form.noValidate = true;
      var head = el("div", "modal-head");
      var title = el("h2", "modal-title", o.title);
      title.id = id + "-title";
      var x = closeButton("关闭");
      head.appendChild(title);
      head.appendChild(x);
      form.appendChild(head);
      if (o.body) {
        var body = el("p", "modal-body", o.body);
        body.id = id + "-body";
        dlg.setAttribute("aria-describedby", body.id);
        form.appendChild(body);
      }
      var input, err;
      if (mode === "prompt") {
        var field = el("div", "field");
        var lab = el("label", null, o.label || "");
        input = el("input", "control");
        input.id = lab.htmlFor = id + "-input";
        input.type = o.type || "text";
        input.value = o.value || "";
        input.autocomplete = "off";
        input.spellcheck = false;
        if (o.placeholder) input.placeholder = o.placeholder;
        err = el("p", "field-error");
        err.id = id + "-error";
        err.hidden = true;
        input.setAttribute("aria-describedby", err.id);
        input.addEventListener("input", function () {
          err.hidden = true;
          input.classList.remove("is-error");
          input.removeAttribute("aria-invalid");
        });
        field.appendChild(lab);
        field.appendChild(input);
        field.appendChild(err);
        form.appendChild(field);
      }
      var actions = el("div", "modal-actions");
      var cancel = null;
      if (mode !== "alert") {
        cancel = el("button", "btn btn-secondary", o.cancel || "取消");
        cancel.type = "button";
        cancel.addEventListener("click", function () { dlg.close(); });
        actions.appendChild(cancel);
      }
      var ok = el("button", "btn" + (o.danger && mode !== "alert" ? " btn-danger" : ""), o.ok || (mode === "alert" ? "知道了" : "确定"));
      ok.type = "submit";
      actions.appendChild(ok);
      form.appendChild(actions);
      dlg.appendChild(form);

      form.addEventListener("submit", function (e) {
        e.preventDefault();
        if (input) {
          var v = input.value.trim();
          if (!v) {
            err.textContent = (o.label || "内容") + "不能为空";
            err.hidden = false;
            input.classList.add("is-error");
            input.setAttribute("aria-invalid", "true");
            input.focus();
            return;
          }
          result = v;
        } else {
          result = true;
        }
        dlg.close();
      });
      x.addEventListener("click", function () { dlg.close(); });
      // 点击遮罩取消；按下点在对话框内（如拖选文字）时不算
      var downOnBackdrop = false;
      dlg.addEventListener("mousedown", function (e) { downOnBackdrop = e.target === dlg; });
      dlg.addEventListener("click", function (e) { if (downOnBackdrop && e.target === dlg) dlg.close(); });
      // Esc 由 dialog 原生处理（触发 close），结果保持默认的取消值
      dlg.addEventListener("close", function () {
        dlg.remove();
        if (opener && opener.focus && document.contains(opener)) opener.focus();
        resolve(result);
      });

      document.body.appendChild(dlg);
      dlg.showModal(); // 模态打开时页面其余部分不可交互，焦点限制在对话框内
      (input || (mode === "alert" || !o.danger ? ok : cancel)).focus();
    });
  }

  // 轻提示：右下角堆叠，自动消失。
  var toastBox = null;
  function toast(title, msg, tone) {
    if (!toastBox) {
      toastBox = el("div", "toasts");
      document.body.appendChild(toastBox);
    }
    var t = el("div", "toast" + (tone ? " toast-" + tone : ""));
    t.setAttribute("role", tone === "danger" ? "alert" : "status");
    var body = el("div");
    body.appendChild(el("strong", null, title));
    if (msg) body.appendChild(el("p", null, msg));
    var x = closeButton("关闭通知");
    t.appendChild(body);
    t.appendChild(x);
    toastBox.appendChild(t);
    var timer = setTimeout(dismiss, tone === "danger" ? 6000 : 3200);
    function dismiss() {
      clearTimeout(timer);
      t.classList.add("is-leaving");
      setTimeout(function () { t.remove(); }, 160);
    }
    x.addEventListener("click", dismiss);
  }

  // 刷新页面；对话框打开时推迟，避免打断用户输入
  function reload() {
    if (document.querySelector("dialog[open]")) { setTimeout(reload, 1000); return; }
    location.reload();
  }

  // ---- 手动同步 ----
  function token(needed) {
    if (!needed) return Promise.resolve("");
    var t = "";
    try { t = sessionStorage.getItem("admin-token") || ""; } catch (e) {}
    if (t) return Promise.resolve(t);
    return ask({
      mode: "prompt",
      title: "需要管理令牌",
      body: "同步操作需要服务端配置的 admin_token。令牌只保存在当前标签页，关闭后失效。",
      label: "管理令牌",
      type: "password",
      ok: "继续"
    }).then(function (v) {
      try { if (v) sessionStorage.setItem("admin-token", v); } catch (e) {}
      return v || "";
    });
  }

  function setBusy(btn, busy) {
    if (busy) {
      btn.setAttribute("data-label", btn.textContent);
      btn.textContent = "";
      btn.appendChild(el("span", "spinner"));
      btn.appendChild(document.createTextNode("同步中…"));
    } else {
      btn.textContent = btn.getAttribute("data-label");
    }
    btn.disabled = busy;
    btn.classList.toggle("is-loading", busy);
  }

  function post(url, btn) {
    var auth = btn.hasAttribute("data-auth");
    var label = btn.textContent.trim();
    var question = btn.getAttribute("data-confirm");
    var confirmed = question ? ask({ mode: "confirm", title: "确认" + label + "？", body: question, ok: label }) : Promise.resolve(true);
    confirmed
      .then(function (yes) { return yes ? token(auth) : null; })
      .then(function (t) {
        if (t === null || (auth && !t)) return;
        var headers = {};
        if (t) headers["Authorization"] = "Bearer " + t;
        setBusy(btn, true);
        return fetch(url, { method: "POST", headers: headers })
          .then(function (r) {
            if (r.status === 401) {
              try { sessionStorage.removeItem("admin-token"); } catch (e) {}
              throw new Error("管理令牌无效，请重新点击并输入正确的令牌。");
            }
            return r.json().then(function (body) {
              if (!r.ok) throw new Error(body.error || r.statusText);
              return body;
            });
          })
          .then(function () {
            toast("已开始同步", "页面将自动刷新以显示最新状态。", "ok");
            setTimeout(reload, 1500);
          })
          .catch(function (err) {
            setBusy(btn, false);
            return ask({ mode: "alert", title: "同步失败", body: err.message, danger: true });
          });
      });
  }

  document.querySelectorAll("[data-sync]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var key = btn.getAttribute("data-sync").split("/").map(encodeURIComponent).join("/");
      post("/api/v1/trees/" + key + "/sync" + (btn.hasAttribute("data-force") ? "?force=true" : ""), btn);
    });
  });
  document.querySelectorAll("[data-sync-all]").forEach(function (btn) {
    btn.addEventListener("click", function () { post("/api/v1/sync", btn); });
  });

  function request(method, url, t, payload) {
    var headers = { "Authorization": "Bearer " + t };
    if (payload) headers["Content-Type"] = "application/json";
    return fetch(url, { method: method, headers: headers, body: payload ? JSON.stringify(payload) : undefined })
      .then(function (r) {
        if (r.status === 401) {
          try { sessionStorage.removeItem("admin-token"); } catch (e) {}
          throw new Error("管理令牌无效，请重试并输入正确的令牌。");
        }
        return r.json().then(function (body) {
          if (!r.ok) throw new Error(body.error || r.statusText);
          return body;
        });
      });
  }

  // ---- 登记 / 删除文档树 ----
  document.querySelectorAll("dialog.modal[id]").forEach(function (dlg) {
    var opener = null;
    document.querySelectorAll('[data-open="' + dlg.id + '"]').forEach(function (btn) {
      btn.addEventListener("click", function () { opener = btn; dlg.showModal(); dlg.querySelector("input.control").focus(); });
    });
    dlg.querySelectorAll("[data-close]").forEach(function (b) {
      b.addEventListener("click", function () { dlg.close(); });
    });
    var downOnBackdrop = false;
    dlg.addEventListener("mousedown", function (e) { downOnBackdrop = e.target === dlg; });
    dlg.addEventListener("click", function (e) { if (downOnBackdrop && e.target === dlg) dlg.close(); });
    dlg.addEventListener("close", function () { if (opener && document.contains(opener)) opener.focus(); });
  });

  document.querySelectorAll("form[data-add-tree]").forEach(function (form) {
    var formErr = form.querySelector(".form-error");
    var submit = form.querySelector('[type="submit"]');
    function fieldError(input, msg) {
      var e = input.closest(".field").querySelector(".field-error");
      e.textContent = msg;
      e.hidden = false;
      input.classList.add("is-error");
      input.setAttribute("aria-invalid", "true");
    }
    form.querySelectorAll("input.control").forEach(function (input) {
      input.addEventListener("input", function () {
        var e = input.closest(".field").querySelector(".field-error");
        if (e) e.hidden = true;
        input.classList.remove("is-error");
        input.removeAttribute("aria-invalid");
      });
    });
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      formErr.hidden = true;
      var data = {};
      new FormData(form).forEach(function (v, k) { data[k] = String(v).trim(); });
      var first = null;
      form.querySelectorAll("input[required]").forEach(function (input) {
        if (!input.value.trim()) {
          fieldError(input, input.closest(".field").querySelector("label").textContent + "不能为空");
          first = first || input;
        }
      });
      if (first) { first.focus(); return; }
      token(true).then(function (t) {
        if (!t) return;
        submit.disabled = true;
        submit.classList.add("is-loading");
        return request("POST", "/api/v1/trees", t, data)
          .then(function (body) {
            toast("已添加", "正在进行首次同步。", "ok");
            location.href = body.status_url;
          })
          .catch(function (err) {
            submit.disabled = false;
            submit.classList.remove("is-loading");
            formErr.textContent = err.message;
            formErr.hidden = false;
          });
      });
    });
  });

  document.querySelectorAll("[data-remove-tree]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var key = btn.getAttribute("data-remove-tree");
      ask({ mode: "confirm", title: "删除文档树 " + key + "？", body: "将停止同步并从站点、搜索中移除。已保存的快照保留在存储中，重新添加同名文档树可直接复用。", ok: "删除", danger: true })
        .then(function (yes) { return yes ? token(true) : ""; })
        .then(function (t) {
          if (!t) return;
          btn.disabled = true;
          return request("DELETE", "/api/v1/trees/" + key.split("/").map(encodeURIComponent).join("/"), t)
            .then(function () { location.href = "/status"; })
            .catch(function (err) {
              btn.disabled = false;
              return ask({ mode: "alert", title: "删除失败", body: err.message, danger: true });
            });
        });
    });
  });

  // 有同步进行中时自动刷新状态页
  if (location.pathname.indexOf("/status") === 0 && document.querySelector(".badge-info")) {
    setTimeout(reload, 3000);
  }
})();
