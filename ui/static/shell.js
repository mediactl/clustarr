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
})();
