// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// The dashboard's tabbed cards and paged lists (owner, 2026-11-04): the
// summary poller replaces the list cards every 10 s, and the charts poller
// and the range picker the chart cards, so each of their requests says
// which tab every card shows (tab-<card>=<value>) and which page every
// paged list shows (page-<list>=<n>), and the server renders those -- a
// refresh never snaps a tab or a page back. A pager's link moves its list
// to the page and asks the summary poller for it at once (dashboard:page);
// without this script the link loads the dashboard on that page.
(function () {
  "use strict";

  // asked holds a page a pager's link asked for until a summary request
  // carries it: if a refresh is in flight when the link is pressed, htmx
  // queues the link's request behind it, and that refresh's answer re-renders
  // the list on its old page before the queued request reads it.
  var asked = {};

  document.addEventListener("htmx:configRequest", function (e) {
    var path = (e.detail && e.detail.path) || "";
    if (path.indexOf("/dashboard/") !== 0) return;
    document.querySelectorAll("[data-dashboard-tabs]").forEach(function (root) {
      var tab = root.querySelector('[role="tab"][aria-selected="true"]');
      if (tab) {
        e.detail.parameters["tab-" + root.getAttribute("data-dashboard-tabs")] = tab.getAttribute("data-templ-value");
      }
    });
    document.querySelectorAll("[data-dashboard-page-list]").forEach(function (list) {
      e.detail.parameters["page-" + list.getAttribute("data-dashboard-page-list")] = list.getAttribute("data-dashboard-page");
    });
    if (path.indexOf("/dashboard/summary") === 0) {
      Object.keys(asked).forEach(function (key) {
        e.detail.parameters["page-" + key] = asked[key];
      });
      asked = {};
    }
  });

  document.addEventListener("click", function (e) {
    var link = e.target && e.target.closest ? e.target.closest("[data-dashboard-goto]") : null;
    if (!link) return;
    var key = link.getAttribute("data-dashboard-list");
    var list = document.querySelector('[data-dashboard-page-list="' + key + '"]');
    var poller = document.getElementById("dashboard-summary-poller");
    if (!list || !poller || !window.htmx) return;
    e.preventDefault();
    asked[key] = link.getAttribute("data-dashboard-goto");
    window.htmx.trigger(poller, "dashboard:page");
  });
})();
