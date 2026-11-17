// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// One app-wide modal region, #modal in Shell holds at most one dialog, #modal-dialog (views.FormDialog
// or any dialog a handler renders with that id). This script opens it (a[data-modal-open][href], the
// address #modal=<url>, clustarr.modal.open), gives it a history entry, guards unsaved changes, returns
// focus, and fills a form's __baseline.
(function () {
  "use strict";
  var HASH = "#modal=";
  // Dialog URLs that may be opened from the address: nothing else is fetched on a hash's word.
  var ROUTES = [/^\/settings\/(new|edit)\/[a-z]+(\/|\?|$)/, /^\/library\/collections\/(new|[^\/?#]+\/[^\/?#]+\/edit)(\?|$)/];
  var st = { opener: null, openerId: "", url: "", host: "", pushed: false, expectPop: false, closing: false, opening: false, base: null };

  function region() { return document.getElementById("modal"); }
  function popup() { return document.getElementById("modal-dialog"); }
  function form() { var p = popup(); return p ? p.querySelector("form[data-modal-form]") : null; }
  function addressable(u) { return ROUTES.some(function (re) { return re.test(u); }); }
  function here() { return location.pathname + location.search; }
  function ser(f) { var d = new FormData(f); d.delete("__baseline"); return new URLSearchParams(d).toString(); }
  // dirty is asked only when it matters (a close, Back, leaving): nothing listens to input events.
  function dirty() {
    var p = popup(), f = form();
    return !!p && !!f && p.hasAttribute("data-modal-guard") && st.base !== null && ser(f) !== st.base;
  }
  function hashURL() {
    if (location.hash.indexOf(HASH) !== 0) return null;
    var u = location.hash.slice(HASH.length);
    return u.charAt(0) === "/" && u.charAt(1) !== "/" && addressable(u) ? u : null;
  }
  function dropHash() {
    if (location.hash.indexOf(HASH) === 0) {
      if (st.pushed) { st.expectPop = true; history.back(); }
      else history.replaceState(null, "", here());
    }
    st.pushed = false;
  }
  // ask puts the discard question through the layout's confirm (foundation.md 4.8: clustarr.confirm(opts,
  // onOK), which says nothing of a Cancel). The confirm's own dialog-close tells the rest: onCancel runs
  // when the action was not pressed, and focus goes back where it was (the confirm's trigger is hidden, so
  // its focus manager returns focus nowhere).
  function ask(onOK, onCancel) {
    var c = window.clustarr && window.clustarr.confirm;
    if (!c) { onOK(); return; }
    var ok = false, prev = document.activeElement;
    c({ title: "Discard changes?", text: "Your edits are not saved.", action: "Discard" }, function () { ok = true; onOK(); });
    document.addEventListener("dialog-close", function closed(e) {
      if (e.target.id !== "confirm") return;
      document.removeEventListener("dialog-close", closed);
      if (ok) return;
      if (onCancel) onCancel();
      setTimeout(function () {
        var a = document.activeElement, box = document.getElementById("confirm");
        if (prev && prev.isConnected && (!a || a === document.body || (box && box.contains(a)))) prev.focus({ preventScroll: true });
      }, 0);
    });
  }
  // force closes without asking: the public close skips the cancelable dialog-open-change.
  function force() {
    var p = popup();
    if (!p) return;
    st.base = null;
    st.closing = true;
    window.templ.dialog.close(p);
  }

  function open(url, opener) {
    if (st.closing && !popup()) st.closing = false;
    if (st.opening || st.closing) return;
    if (dirty()) { ask(function () { st.base = null; open(url, opener); }); return; }
    var before = popup();
    if (before && opener && before.contains(opener)) opener = st.opener;   // opened from inside the dialog
    st.opening = true;
    if (opener) opener.setAttribute("aria-busy", "true");
    var done = function () { st.opening = false; if (opener) opener.removeAttribute("aria-busy"); };
    // The region is its own source: the request inherits nobody's hx-select, hx-include or hx-push-url,
    // and waits in nobody else's per-element queue (a request without a source is <body>'s).
    htmx.ajax("GET", url, { source: "#modal", target: "#modal", swap: "innerHTML" }).then(function () {
      done();
      var p = popup();
      if (!p || p === before) { if (!before) dropHash(); return; }   // an error toast, not a dialog
      st.opener = opener || null;
      st.openerId = (opener && opener.id) || "";
      st.url = url; st.base = null; st.closing = false; st.host = here();
      wrapPop();
      if (addressable(url) && hashURL() !== url) {
        if (location.hash.indexOf(HASH) === 0) history.replaceState({ modal: true }, "", HASH + url);
        else { history.pushState({ modal: true }, "", HASH + url); st.pushed = true; }
      }
    }, done);
  }

  document.addEventListener("click", function (e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    var a = e.target.closest && e.target.closest("a[data-modal-open][href]");
    if (!a) return;
    e.preventDefault();
    open(a.getAttribute("href"), a);
  });

  // The baseline: the form as it landed, after settings.js's condition pass of the same settle.
  document.addEventListener("htmx:afterSettle", function (e) {
    var p = popup(), r = region();
    if (!p || !r || !(r.contains(e.target) || p.contains(e.target))) return;
    setTimeout(function () {
      var f = form();
      if (!f) return;
      if (st.base === null) st.base = ser(f);
      var b = f.querySelector('input[name="__baseline"]');
      if (b && b.value === "") b.value = st.base;   // also the form a rejected save re-rendered
    }, 0);
  });
  // A rejected save: show and focus the error.
  document.addEventListener("htmx:afterSwap", function (e) {
    var p = popup();
    if (!p || !p.contains(e.target)) return;
    var err = p.querySelector("[data-form-error]");
    if (!err) return;
    var b = p.querySelector("[data-modal-body]");
    if (b) b.scrollTop = 0;
    err.focus({ preventScroll: true });
  });

  // Escape, the X and Cancel all pass here (dialog.js requestOpenChange); a backdrop press never does.
  document.addEventListener("dialog-open-change", function (e) {
    if (e.target.id !== "modal-dialog" || !e.detail || e.detail.open !== false) return;
    if (dirty()) { e.preventDefault(); ask(force); return; }
    st.closing = true;
  });
  document.addEventListener("modal-close", force);   // the server's HX-Trigger
  document.addEventListener("dialog-close", function (e) {
    // A dialog replaced by a new load fires this too (lifecycle destroy); only a real close cleans up.
    if (e.target.id !== "modal-dialog" || !st.closing) return;
    var opener = st.opener, id = st.openerId;
    region().replaceChildren();
    st.opener = null; st.openerId = ""; st.url = ""; st.base = null; st.closing = false;
    dropHash();
    setTimeout(function () {
      var a = document.activeElement;
      if (a && a !== document.body) return;   // the focus manager returned it already
      var to = opener && opener.isConnected ? opener : (id && document.getElementById(id)) || document.getElementById("page-body");
      if (to) to.focus({ preventScroll: true });
    }, 0);
  });

  // History. htmx restores the page on a popstate whose state is {htmx:true}; its handler is
  // window.onpopstate, set at its own start, and it runs before any listener added later, capture or
  // not (measured, Chromium 152). So that handler is wrapped.
  function onPop() {
    if (st.expectPop) { st.expectPop = false; return true; }
    var p = popup(), h = hashURL();
    if (p && !st.closing && !h && here() === st.host) {   // Back left the dialog's entry
      st.pushed = false;
      if (dirty()) ask(force, function () { history.pushState({ modal: true }, "", HASH + st.url); st.pushed = true; });
      else force();
      return true;
    }
    if (!p && h) { st.pushed = true; open(h, null); return true; }   // Forward onto it
    return false;
  }
  function wrapPop() {
    var prev = window.onpopstate;
    if (prev && prev.__modal) return;
    var w = function (e) { if (!onPop() && prev) prev.call(window, e); };
    w.__modal = true;
    window.onpopstate = w;
  }
  window.addEventListener("beforeunload", function (e) { if (dirty()) { e.preventDefault(); e.returnValue = ""; } });

  // A dialog that lands by a load focuses its first field; on a touch screen that raises the keyboard
  // over a sheet nobody has read. The popup takes the focus there, as a dialog opened by its trigger does.
  document.addEventListener("focusin", function (e) {
    var p = popup();
    if (!p || !p.hasAttribute("data-modal-fresh") || !p.contains(e.target)) return;
    p.removeAttribute("data-modal-fresh");
    if (matchMedia("(pointer: coarse)").matches && e.target.matches("input:not([type=checkbox]):not([type=radio]), textarea")) {
      p.focus({ preventScroll: true });
    }
  });

  // A required select: the component's visually hidden input is what the browser refuses, with a bubble
  // on a 1px box. Mark the trigger instead; the browser still reports the first native field.
  var SELECT_INPUT = 'input[aria-hidden="true"][tabindex="-1"]';
  function triggerOf(input) {
    var el = input.previousElementSibling;
    while (el && !(el.matches && el.matches('[data-slot="select-trigger"]'))) el = el.previousElementSibling;
    return el;
  }
  document.addEventListener("invalid", function (e) {
    var input = e.target;
    if (!input.matches || !input.matches(SELECT_INPUT)) return;
    var t = triggerOf(input);
    if (!t) return;
    e.preventDefault();
    t.setAttribute("aria-invalid", "true");
    var f = input.form;
    setTimeout(function () {
      var a = document.activeElement;
      if (a && a.matches && a.matches(":invalid")) return;   // the browser took a native field
      var first = f && f.querySelector('[data-slot="select-trigger"][aria-invalid="true"]');
      if (first) { first.scrollIntoView({ block: "center" }); first.focus({ preventScroll: true }); }
    }, 0);
  }, true);
  document.addEventListener("change", function (e) {
    if (!e.target.matches || !e.target.matches(SELECT_INPUT)) return;
    var t = triggerOf(e.target);
    if (t) t.removeAttribute("aria-invalid");
  });

  window.clustarr = window.clustarr || {};
  window.clustarr.modal = { open: open, close: force };
  function start() { wrapPop(); if (hashURL()) open(hashURL(), null); }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start); else start();
})();
