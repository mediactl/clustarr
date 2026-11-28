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
})();
