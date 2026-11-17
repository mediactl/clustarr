// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
/*
 * What a request is doing, and how it went, for every page (2026-11-17).
 *
 *  - A navigation in flight (a request whose target is #page-body) sets
 *    data-navigating on <html> after 150ms, which draws the gold line along
 *    the top bar's foot, and aria-busy on the page body. A poll or an action
 *    never targets #page-body, so nothing flashes for them.
 *  - A request that failed says so in a toast: htmx swaps no error status,
 *    so a failed poll or fragment was silent everywhere. One toast, updated
 *    in place, however many requests fail.
 *  - #page-refresh, which redraws the page body after an action changed
 *    what it shows (HX-Trigger: page-changed), asks as a navigation would:
 *    some page URLs answer htmx a fragment, and the preload cache must not
 *    answer.
 *  - Polls pause in a hidden tab (views.pollTrigger) and are woken here when
 *    the tab shows again.
 *
 * Every listener ignores a preload: the preload extension cancels those
 * after the DOM listeners ran, and no afterRequest follows.
 */
(function () {
  function preloaded(e) {
    var h = e.detail && e.detail.requestConfig && e.detail.requestConfig.headers;
    return !!h && h["HX-Preloaded"] === "true";
  }
  function isNav(e) {
    return e.detail && e.detail.target && e.detail.target.id === "page-body";
  }

  var timer = null;
  function done() {
    clearTimeout(timer);
    timer = null;
    document.documentElement.removeAttribute("data-navigating");
    var body = document.getElementById("page-body");
    if (body) body.removeAttribute("aria-busy");
  }
  document.addEventListener("htmx:beforeRequest", function (e) {
    if (preloaded(e) || !isNav(e)) return;
    clearTimeout(timer);
    timer = setTimeout(function () {
      document.documentElement.setAttribute("data-navigating", "");
      var body = document.getElementById("page-body");
      if (body) body.setAttribute("aria-busy", "true");
    }, 150);
  });
  document.addEventListener("htmx:afterRequest", function (e) {
    if (isNav(e)) done();
  });

  function fail(title, description) {
    if (!window.templ || !window.templ.toast) return;
    window.templ.toast.add({ id: "request-error", type: "error", timeout: 12000, title: title, description: description || "" });
  }
  document.addEventListener("htmx:responseError", function (e) {
    if (preloaded(e)) return;
    var xhr = e.detail && e.detail.xhr;
    var status = xhr ? xhr.status : 0;
    var title = "Clustarr answered " + status;
    if (status === 404) title = "Not found";
    else if (status === 403) title = "Not allowed";
    else if (status === 409) title = "It changed in the meantime";
    var text = "";
    if (xhr && /^text\/plain/.test(xhr.getResponseHeader("Content-Type") || "") && xhr.responseText.length < 300) text = xhr.responseText.trim();
    fail(title, text);
  });
  document.addEventListener("htmx:sendError", function (e) {
    if (preloaded(e)) return;
    fail("Could not reach Clustarr", "");
  });

  document.addEventListener("htmx:configRequest", function (e) {
    if (!e.detail || !e.detail.elt || e.detail.elt.id !== "page-refresh") return;
    e.detail.path = window.location.pathname + window.location.search;
    e.detail.headers["HX-Request"] = "false";
    e.detail.headers["Cache-Control"] = "no-cache";
  });

  document.addEventListener("visibilitychange", function () {
    if (!document.hidden && window.htmx) window.htmx.trigger(document.body, "shell:visible");
  });
})();
