// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// The Live TV pages' small behaviours (the direct-tuners and ui design
// 2026-10-27): a [data-copy] button writes its value to the clipboard and
// says so; a channel logo that will not load gives way to its number tile;
// a <time data-local="datetime"> reads in the browser's zone. Delegated on
// the document, so rows htmx swaps in need nothing more.
(function () {
  "use strict";

  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest("[data-copy]");
    if (!btn || !btn.getAttribute("data-copy")) {
      return;
    }
    ev.preventDefault();
    var value = btn.getAttribute("data-copy");
    var done = function () {
      var before = btn.getAttribute("title") || "";
      btn.setAttribute("title", "Copied");
      btn.setAttribute("data-copied", "");
      setTimeout(function () {
        btn.setAttribute("title", before);
        btn.removeAttribute("data-copied");
      }, 1500);
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(value).then(done, function () {});
      return;
    }
    // Plain http behind an ingress: the old way.
    var ta = document.createElement("textarea");
    ta.value = value;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
      done();
    } catch (e) {}
    document.body.removeChild(ta);
  });

  // A logo the tuner has not stored yet answers 404: show the tile.
  document.addEventListener(
    "error",
    function (ev) {
      var img = ev.target;
      if (img && img.tagName === "IMG" && img.hasAttribute("data-logo")) {
        img.hidden = true;
        var tile = img.nextElementSibling;
        if (tile && tile.hasAttribute("data-logo-tile")) {
          tile.hidden = false;
        }
      }
    },
    true
  );

  var localise = function (root) {
    root.querySelectorAll('time[data-local="datetime"]').forEach(function (t) {
      var d = new Date(t.getAttribute("datetime"));
      if (!isNaN(d)) {
        t.textContent = d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
      }
    });
  };
  document.addEventListener("DOMContentLoaded", function () {
    localise(document);
  });
  document.addEventListener("htmx:afterSettle", function (ev) {
    localise(ev.target);
  });
})();
