// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// Every hx-confirm asks through the layout's alert dialog (#confirm,
// views.confirmDialog) instead of the browser's window.confirm (owner,
// 2026-11-04: the UI on shadcn-templ's components): htmx's confirm event
// is held, the question shown, and the action issues the request. Without
// the dialog on the page the event runs on as before.
//
// Since 2026-11-17 the element asking may name the dialog's title
// (data-confirm-title), the action's word (data-confirm-action) and its
// tone (data-confirm-variant="default" for a question that destroys
// nothing), and a script asks the same dialog through
// window.clustarr.confirm({title, text, action, variant}, onOK): onOK runs
// on the action's click, nothing runs on Cancel or Escape.
(function () {
  "use strict";
  var pending = null;

  function ask(o, go) {
    var open = document.querySelector("[data-confirm-open]");
    var text = document.querySelector("[data-confirm-text]");
    if (!open || !text) return false;
    text.textContent = o.text || "";
    // The dialog's own heading: an asking element carries
    // data-confirm-title too, and the first of those in the document was
    // what a bare attribute selector found -- its label became the question.
    var title = document.querySelector('[data-slot="alert-dialog-title"] [data-confirm-title]');
    if (title) title.textContent = o.title || "Are you sure?";
    var variant = o.variant === "default" ? "default" : "destructive";
    document.querySelectorAll("[data-confirm-ok]").forEach(function (b) {
      var label = b.querySelector("[data-confirm-action]");
      if (label) label.textContent = o.action || "Continue";
      b.hidden = b.getAttribute("data-confirm-ok") !== variant;
    });
    pending = go;
    open.click();
    return true;
  }

  document.addEventListener("htmx:confirm", function (e) {
    var elt = e.detail && e.detail.elt;
    if (!elt || !elt.hasAttribute || !elt.hasAttribute("hx-confirm")) return;
    var issue = e.detail.issueRequest;
    var asked = ask(
      {
        text: e.detail.question || elt.getAttribute("hx-confirm") || "",
        title: elt.getAttribute("data-confirm-title"),
        action: elt.getAttribute("data-confirm-action"),
        variant: elt.getAttribute("data-confirm-variant"),
      },
      function () {
        issue(true);
      },
    );
    if (asked) e.preventDefault();
  });
  document.addEventListener("click", function (e) {
    var ok = e.target && e.target.closest ? e.target.closest("[data-confirm-ok]") : null;
    if (!ok || !pending) return;
    var go = pending;
    pending = null;
    go();
  });
  document.addEventListener("alert-dialog-open-change", function (e) {
    if (e.detail && e.detail.open === false) pending = null;
  });

  window.clustarr = window.clustarr || {};
  window.clustarr.confirm = function (o, onOK) {
    return ask(o || {}, onOK || function () {});
  };
})();
