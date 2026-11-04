// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// Every hx-confirm asks through the layout's alert dialog (#confirm,
// views.confirmDialog) instead of the browser's window.confirm (owner,
// 2026-11-04: the UI on shadcn-templ's components): htmx's confirm event
// is held, the question shown, and Continue issues the request. Without
// the dialog on the page the event runs on as before.
(function () {
  "use strict";
  var pending = null;
  document.addEventListener("htmx:confirm", function (e) {
    var elt = e.detail && e.detail.elt;
    if (!elt || !elt.hasAttribute || !elt.hasAttribute("hx-confirm")) return;
    var open = document.querySelector("[data-confirm-open]");
    var text = document.querySelector("[data-confirm-text]");
    if (!open || !text) return;
    e.preventDefault();
    pending = e.detail.issueRequest;
    text.textContent = e.detail.question || elt.getAttribute("hx-confirm") || "";
    open.click();
  });
  document.addEventListener("click", function (e) {
    var ok = e.target && e.target.closest ? e.target.closest("[data-confirm-ok]") : null;
    if (!ok || !pending) return;
    var go = pending;
    pending = null;
    go(true);
  });
  document.addEventListener("alert-dialog-open-change", function (e) {
    if (e.detail && e.detail.open === false) pending = null;
  });
})();
