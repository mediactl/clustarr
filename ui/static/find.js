/*
 * The top bar search's keys (2026-10-01, after Sonarr's and Radarr's
 * header search). htmx fills #library-find-results from the box as the
 * reader types, and #library-find-count out of band; this moves a
 * highlight through its links with the arrow keys, opens the highlighted
 * one (or the first) with Enter, and closes the dropdown with Escape, a
 * click outside it or an emptied box. Nothing here fetches.
 */
(function () {
  "use strict";

  function parts(el) {
    var root = el && el.closest ? el.closest("[data-find]") : null;
    if (!root) return null;
    return {
      root: root,
      input: root.querySelector("[data-find-input]"),
      group: root.querySelector('[data-slot="input-group"]'),
      results: root.querySelector("#library-find-results"),
      count: root.querySelector("#library-find-count"),
    };
  }

  function options(p) {
    return Array.prototype.slice.call(p.results.querySelectorAll("[data-find-ref]"));
  }

  function activeIndex(opts) {
    for (var i = 0; i < opts.length; i++) {
      if (opts[i].hasAttribute("data-find-active")) return i;
    }
    return -1;
  }

  // highlight marks option i and tells the box which one it is
  // (aria-activedescendant), so a screen reader announces the highlighted
  // match; i < 0 clears it.
  function highlight(opts, i, input) {
    opts.forEach(function (o, j) {
      if (!o.id) o.id = "find-opt-" + j;
      if (j === i) {
        o.setAttribute("data-find-active", "");
        o.setAttribute("aria-selected", "true");
        o.scrollIntoView({ block: "nearest" });
      } else {
        o.removeAttribute("data-find-active");
        o.removeAttribute("aria-selected");
      }
    });
    if (!input) return;
    if (i >= 0 && opts[i]) input.setAttribute("aria-activedescendant", opts[i].id);
    else input.removeAttribute("aria-activedescendant");
  }

  // place puts the dropdown under the input group. It is position: fixed so
  // no ancestor's overflow can clip it, which means following the box by
  // hand: at least 320px wide, never wider than the window less a gutter,
  // the window's whole width under 640px, never above the top bar's foot,
  // and no taller than the room under it (2026-11-17: it ran off a phone's
  // right edge and widened the page, and a landscape phone's foot cut it).
  function place(p) {
    var r = (p.group || p.input).getBoundingClientRect();
    var vw = document.documentElement.clientWidth;
    var w = Math.min(Math.max(r.width, 320), vw - 16);
    var left = Math.min(Math.max(r.left, 8), vw - 8 - w);
    if (vw < 640) {
      left = 8;
      w = vw - 16;
    }
    var top = r.bottom + 6;
    var vh = window.visualViewport ? window.visualViewport.height : window.innerHeight;
    p.results.style.top = top + "px";
    p.results.style.left = left + "px";
    p.results.style.width = w + "px";
    p.results.style.setProperty("--find-max-h", Math.max(160, vh - top - 12) + "px");
  }

  function close(p) {
    p.results.innerHTML = "";
    p.input.setAttribute("aria-expanded", "false");
    p.input.removeAttribute("aria-activedescendant");
  }

  // The search lives in the sidebar (the Liquid Glass shell, 2026-11-28):
  // where the sidebar is hidden -- a tablet's overlay, a phone's sheet, a
  // desktop that hid it -- a search button opens it and focuses the box in
  // the same gesture (iOS raises the keyboard only inside one).
  function input() {
    return document.querySelector("#header-find [data-find-input]");
  }
  function openPhone() {
    var i = input();
    if (!i) return null;
    if (i.offsetParent === null || i.getBoundingClientRect().right <= 0) {
      var t = document.querySelector('[data-templ-sidebar-trigger="app-sidebar"]');
      if (t) t.click();
    }
    i.focus();
    setTimeout(function () {
      i.focus();
    }, 260);
    return i;
  }
  function closePhone() {
    var i = input();
    var p = i ? parts(i) : null;
    if (p) close(p);
  }

  // An emptied box answers nothing (the handler sends no count), so the
  // count is cleared here.
  document.addEventListener("input", function (e) {
    if (!e.target.matches || !e.target.matches("[data-find-input]")) return;
    var p = parts(e.target);
    if (p && p.count && e.target.value.trim() === "") p.count.textContent = "";
  });

  document.addEventListener("keydown", function (e) {
    if (!e.target.matches || !e.target.matches("[data-find-input]")) return;
    var p = parts(e.target);
    if (!p) return;
    var opts = options(p);
    var i = activeIndex(opts);
    switch (e.key) {
      case "ArrowDown":
        if (!opts.length) return;
        e.preventDefault();
        highlight(opts, (i + 1) % opts.length, p.input);
        break;
      case "ArrowUp":
        if (!opts.length) return;
        e.preventDefault();
        highlight(opts, i <= 0 ? opts.length - 1 : i - 1, p.input);
        break;
      case "Enter":
        if (!opts.length) return;
        e.preventDefault();
        window.location.assign(opts[Math.max(i, 0)].getAttribute("href"));
        break;
      case "Escape":
        if (e.target.value === "") closePhone();
        close(p);
        break;
    }
  });

  // A new dropdown starts with nothing highlighted and marks the box open.
  document.addEventListener("htmx:afterSwap", function (e) {
    var p = parts(e.target);
    if (!p || e.target !== p.results) return;
    p.input.setAttribute("aria-expanded", p.results.children.length ? "true" : "false");
    place(p);
  });

  function placeAll() {
    document.querySelectorAll("[data-find]").forEach(function (root) {
      var p = parts(root);
      if (p.results.children.length) place(p);
    });
  }
  window.addEventListener("resize", placeAll);
  window.addEventListener("scroll", placeAll, true);

  if (window.visualViewport) window.visualViewport.addEventListener("resize", placeAll);

  document.addEventListener("click", function (e) {
    var t = e.target && e.target.closest ? e.target : null;
    if (t && t.closest("[data-find-open-button]")) {
      openPhone();
      return;
    }
    if (t && t.closest("[data-find-cancel]")) {
      closePhone();
      return;
    }
    document.querySelectorAll("[data-find]").forEach(function (root) {
      if (!root.contains(e.target)) close(parts(root));
    });
  });

  // A page swap ends the phone's search.
  document.addEventListener("htmx:afterSettle", function (e) {
    if (e.detail && e.detail.target && e.detail.target.id === "page-body") closePhone();
  });

  // "/" focuses the search from anywhere a key is not being typed and no
  // dialog is open; on a phone it opens the search first.
  document.addEventListener("keydown", function (e) {
    if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
    var a = document.activeElement;
    if (a && (a.matches("input, textarea, select") || a.isContentEditable)) return;
    if (document.querySelector('[role="dialog"]:not([hidden]), [role="alertdialog"]:not([hidden])')) return;
    var box = input();
    if (!box) return;
    e.preventDefault();
    box = openPhone();
    if (box) box.select();
  });
})();
