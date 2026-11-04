// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// The dashboard's tabbed cards (owner, 2026-11-04): the summary poller
// replaces the list cards every 10 s, and the charts poller and the range
// picker the chart cards, so each of their requests says which tab every
// card shows (tab-<card>=<value>), and the server renders those open -- a
// refresh never snaps a tab back.
(function () {
  "use strict";

  document.addEventListener("htmx:configRequest", function (e) {
    var path = (e.detail && e.detail.path) || "";
    if (path.indexOf("/dashboard/") !== 0) return;
    document.querySelectorAll("[data-dashboard-tabs]").forEach(function (root) {
      var tab = root.querySelector('[role="tab"][aria-selected="true"]');
      if (tab) {
        e.detail.parameters["tab-" + root.getAttribute("data-dashboard-tabs")] = tab.getAttribute("data-templ-value");
      }
    });
  });
})();
