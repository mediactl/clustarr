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

  function highlight(opts, i) {
    opts.forEach(function (o, j) {
      if (j === i) {
        o.setAttribute("data-find-active", "");
        o.setAttribute("aria-selected", "true");
        o.scrollIntoView({ block: "nearest" });
      } else {
        o.removeAttribute("data-find-active");
        o.removeAttribute("aria-selected");
      }
    });
  }

  // place puts the dropdown under the input group. It is position: fixed so
  // no ancestor's overflow can clip it, which means following the box by
  // hand.
  function place(p) {
    var r = (p.group || p.input).getBoundingClientRect();
    p.results.style.top = r.bottom + 4 + "px";
    p.results.style.left = r.left + "px";
    p.results.style.width = Math.max(r.width, 288) + "px";
  }

  function close(p) {
    p.results.innerHTML = "";
    p.input.setAttribute("aria-expanded", "false");
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
        highlight(opts, (i + 1) % opts.length);
        break;
      case "ArrowUp":
        if (!opts.length) return;
        e.preventDefault();
        highlight(opts, i <= 0 ? opts.length - 1 : i - 1);
        break;
      case "Enter":
        if (!opts.length) return;
        e.preventDefault();
        window.location.assign(opts[Math.max(i, 0)].getAttribute("href"));
        break;
      case "Escape":
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

  document.addEventListener("click", function (e) {
    document.querySelectorAll("[data-find]").forEach(function (root) {
      if (!root.contains(e.target)) close(parts(root));
    });
  });
})();
