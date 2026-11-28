// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
/*
 * The shell's memory (2026-11-17). The server renders the sidebar as the
 * reader left it from two cookies (ui/shellstate.go), so nothing flashes at
 * a load or at an out-of-band swap of the menu; this keeps them written.
 *
 *  - sidebar_state is the sidebar component's own cookie (collapsed to its
 *    icon rail or not). The component writes it for seven days; it is
 *    rewritten here for a year whenever it exists or the reader toggles.
 *  - nav_closed lists the sidebar groups the reader closed, "."-separated.
 *
 * And a sub-entry's page starts at its top with the keyboard's focus on the
 * page body, where a full navigation would have left both.
 */
(function () {
  var YEAR = 60 * 60 * 24 * 365;

  function cookie(name) {
    var m = document.cookie.match(new RegExp("(?:^|; )" + name + "=([^;]*)"));
    return m ? m[1] : null;
  }
  function setCookie(name, value) {
    document.cookie = name + "=" + value + "; path=/; max-age=" + YEAR + "; SameSite=Lax";
  }

  function keepSidebar() {
    var v = cookie("sidebar_state");
    if (v !== null) setCookie("sidebar_state", v);
  }
  keepSidebar();
  document.addEventListener("click", function (e) {
    if (e.target.closest && e.target.closest("[data-templ-sidebar-trigger], [data-slot='sidebar-rail']")) setTimeout(keepSidebar, 0);
  });
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && e.key === "b") setTimeout(keepSidebar, 0);
  });

  // The event precedes the change, so the group that fired is taken from
  // the event and the others from their attributes.
  document.addEventListener("collapsible-open-change", function (e) {
    var group = e.target.closest ? e.target.closest("[data-nav-group]") : null;
    if (!group || !e.detail) return;
    var closed = [];
    document.querySelectorAll("[data-nav-group]").forEach(function (g) {
      var key = g.getAttribute("data-nav-group");
      var isClosed = g === group ? e.detail.open === false : g.hasAttribute("data-closed");
      if (isClosed && closed.indexOf(key) < 0) closed.push(key);
    });
    setCookie("nav_closed", closed.join("."));
  });

  document.addEventListener("htmx:afterSettle", function (e) {
    var d = e.detail;
    if (!d || !d.target || d.target.id !== "page-body") return;
    var elt = d.requestConfig && d.requestConfig.elt;
    if (!elt || !elt.hasAttribute || !elt.hasAttribute("data-nav-link")) return;
    window.scrollTo(0, 0);
    var body = document.getElementById("page-body");
    if (body) body.focus({ preventScroll: true });
  });

  // The Liquid Glass shell (2026-11-28). html[data-scrolled] fades the
  // scroll-edge layer in under the toolbar once the page has moved;
  // html[data-title-collapsed] shows the page's name in the toolbar once its
  // large title (the page's first h1) has scrolled under the bar.
  var root = document.documentElement;
  function onScroll() {
    root.toggleAttribute("data-scrolled", window.scrollY > 4);
  }
  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();
  var titleObserver = null;
  function watchTitle() {
    if (titleObserver) titleObserver.disconnect();
    root.removeAttribute("data-title-collapsed");
    var h = document.querySelector("[data-shell-page] h1");
    if (!h || !("IntersectionObserver" in window)) return;
    var bar = document.querySelector("[data-topbar]");
    var top = bar ? bar.getBoundingClientRect().height : 52;
    titleObserver = new IntersectionObserver(
      function (entries) {
        var e = entries[0];
        root.toggleAttribute("data-title-collapsed", !e.isIntersecting && e.boundingClientRect.top < top);
      },
      { rootMargin: "-" + top + "px 0px 0px 0px" },
    );
    titleObserver.observe(h);
  }
  watchTitle();
  document.addEventListener("htmx:afterSettle", function (e) {
    if (e.detail && e.detail.target && e.detail.target.id === "page-body") watchTitle();
  });

  // From 768 to 1023px the sidebar is an overlay: a press outside it or a
  // navigation from it hides it again, and opening or hiding it there does
  // not change what a wider window remembers (sidebar_state).
  var TABLET = "(min-width: 768px) and (max-width: 1023.98px)";
  function sidebarEl() {
    return document.querySelector('[data-templ-sidebar-id="app-sidebar"]');
  }
  function hideOverlay() {
    var sb = sidebarEl();
    if (!sb || sb.getAttribute("data-state") !== "expanded") return;
    var t = document.querySelector('[data-templ-sidebar-trigger="app-sidebar"]');
    if (t) t.click();
  }
  var savedState = null;
  document.addEventListener(
    "click",
    function (e) {
      if (!matchMedia(TABLET).matches) return;
      var t = e.target.closest ? e.target : null;
      if (!t) return;
      if (t.closest("[data-templ-sidebar-trigger]")) {
        savedState = cookie("sidebar_state");
        setTimeout(function () {
          if (savedState !== null) setCookie("sidebar_state", savedState);
        }, 0);
        return;
      }
      if (t.closest('[data-slot="sidebar-container"], [data-find-open-button], [data-slot="dropdown-menu-content"], #library-find-results')) return;
      hideOverlay();
    },
    true,
  );
  document.addEventListener("htmx:afterSettle", function (e) {
    if (!matchMedia(TABLET).matches) return;
    if (e.detail && e.detail.target && e.detail.target.id === "page-body") hideOverlay();
  });

  // The phone's search sheet ends with its sheet (find.js opens it).
  function syncSearchMode() {
    if (!root.hasAttribute("data-search-mode")) return;
    var sheet = document.querySelector('[data-mobile="true"][data-slot="sheet-content"]');
    if (!sheet || sheet.offsetParent === null || getComputedStyle(sheet).visibility === "hidden") root.removeAttribute("data-search-mode");
  }
  document.addEventListener("click", function () {
    setTimeout(syncSearchMode, 450);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") setTimeout(syncSearchMode, 450);
  });
  document.addEventListener("htmx:afterSettle", syncSearchMode);

  // A section strip shows its lit page: scrolled into view on load.
  function revealStrip() {
    var lit = document.querySelector("[data-section-strip] [data-active]");
    if (lit && lit.scrollIntoView) lit.scrollIntoView({ block: "nearest", inline: "center" });
  }
  revealStrip();
  document.addEventListener("htmx:afterSettle", function (e) {
    if (e.detail && e.detail.target && e.detail.target.id === "page-body") revealStrip();
  });

  // An overview clamped to three lines offers "More" only when it is cut.
  function markClamps() {
    document.querySelectorAll("[data-clamp]").forEach(function (c) {
      var p = c.querySelector("[data-overview]");
      if (p) c.toggleAttribute("data-clamped", p.scrollHeight > p.clientHeight + 2);
    });
  }
  markClamps();
  window.addEventListener("resize", markClamps);
  document.addEventListener("htmx:afterSettle", markClamps);
  document.addEventListener("click", function (e) {
    var more = e.target.closest && e.target.closest("[data-clamp-more]");
    if (more) more.closest("[data-clamp]").setAttribute("data-expanded", "");
  });

  // A segmented filter that does not fit its row becomes a pull-down
  // (2026-11-28, the designer's round 2: a clipped "Ski…" or a hidden
  // Failed reads as broken): a native select of the same segments, which a
  // phone shows as its own picker; choosing one presses its segment. A
  // section strip (navigation) keeps scrolling instead.
  function segLabel(t) {
    var parts = [];
    t.childNodes.forEach(function (n) {
      var x = (n.textContent || "").replace(/\s+/g, " ").trim();
      if (x) parts.push(x);
    });
    return parts.join(" · ");
  }
  function fitSegments(list) {
    if (list.closest("[data-section-strip]")) return;
    var host = list.parentElement;
    if (!host) return;
    var triggers = list.querySelectorAll('[data-slot="tabs-trigger"]');
    if (triggers.length < 2) return;
    var over = list.scrollWidth > host.clientWidth + 2;
    list.toggleAttribute("data-collapsed", over);
    var sel = host.querySelector(":scope > select[data-segment-select]");
    if (!over) {
      if (sel) sel.remove();
      return;
    }
    if (!sel) {
      sel = document.createElement("select");
      sel.setAttribute("data-segment-select", "");
      sel.setAttribute("aria-label", list.getAttribute("aria-label") || "Show");
      sel.addEventListener("change", function () {
        var t = list.querySelector('[data-slot="tabs-trigger"][data-templ-value="' + sel.value + '"]');
        if (t) t.click();
      });
      list.insertAdjacentElement("afterend", sel);
    }
    var html = "";
    triggers.forEach(function (t) {
      var v = t.getAttribute("data-templ-value") || "";
      html += '<option value="' + v.replace(/"/g, "&quot;") + '"' + (t.hasAttribute("data-active") ? " selected" : "") + ">" +
        segLabel(t).replace(/[&<>]/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]; }) + "</option>";
    });
    if (sel.innerHTML !== html) sel.innerHTML = html;
  }
  function fitAllSegments() {
    document.querySelectorAll('[data-slot="tabs-list"]').forEach(fitSegments);
  }
  if ("ResizeObserver" in window) {
    var segObserver = new ResizeObserver(function (entries) {
      entries.forEach(function (e) {
        var list = e.target.querySelector && e.target.querySelector(':scope > [data-slot="tabs-list"]');
        if (list) fitSegments(list);
      });
    });
    var observeSegments = function () {
      document.querySelectorAll('[data-slot="tabs-list"]').forEach(function (l) {
        if (l.parentElement) segObserver.observe(l.parentElement);
      });
      fitAllSegments();
    };
    observeSegments();
    document.addEventListener("htmx:afterSettle", observeSegments);
  }
  document.addEventListener("click", function (e) {
    var t = e.target.closest && e.target.closest('[data-slot="tabs-trigger"]');
    if (t) setTimeout(fitAllSegments, 0);
  });
})();
