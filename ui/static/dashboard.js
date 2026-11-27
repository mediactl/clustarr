// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// The tabbed cards and paged lists of the dashboard (owner, 2026-11-04) and
// the Activity pages (2026-11-10): the list poller ([data-list-poller])
// replaces the list cards every 10 s, and on the dashboard the charts
// poller and the range picker the chart cards, so each of their requests says
// which tab every card shows (tab-<card>=<value>) and which page every
// paged list shows (page-<list>=<n>), and the server renders those -- a
// refresh never snaps a tab or a page back. A pager's link moves its list
// to the page and asks the list poller for it at once (dashboard:page);
// without this script the link loads the page on that page.
(function () {
  "use strict";

  // asked holds a page a pager's link asked for until a summary request
  // carries it: if a refresh is in flight when the link is pressed, htmx
  // queues the link's request behind it, and that refresh's answer re-renders
  // the list on its old page before the queued request reads it.
  var asked = {};

  document.addEventListener("htmx:configRequest", function (e) {
    var path = (e.detail && e.detail.path) || "";
    // Only the pollers' own requests: a sidebar entry's GET of an Activity
    // page also starts /activity/, and carried the leaving page's tab and
    // page into the next page's URL.
    if (path.indexOf("/dashboard/") !== 0 && !/^\/activity\/[^/]+\/list/.test(path)) return;
    // The charts' times are the reader's, not the ui pod's (UTC in a
    // cluster): the server labels them in this zone.
    if (path.indexOf("/dashboard/charts") === 0) {
      try {
        e.detail.parameters.tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
      } catch (err) {
        /* no Intl: the server's zone */
      }
    }
    document.querySelectorAll("[data-dashboard-tabs], [data-activity-tabs]").forEach(function (root) {
      var tab = root.querySelector('[role="tab"][aria-selected="true"]');
      if (tab) {
        var key = root.getAttribute("data-dashboard-tabs") || root.getAttribute("data-activity-tabs");
        e.detail.parameters["tab-" + key] = tab.getAttribute("data-templ-value");
      }
    });
    document.querySelectorAll("[data-dashboard-page-list]").forEach(function (list) {
      e.detail.parameters["page-" + list.getAttribute("data-dashboard-page-list")] = list.getAttribute("data-dashboard-page");
    });
    if (path.indexOf("/dashboard/summary") === 0 || /^\/activity\/[^/]+\/list/.test(path)) {
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
    var poller = document.querySelector("[data-list-poller]");
    if (!list || !poller || !window.htmx) return;
    e.preventDefault();
    asked[key] = link.getAttribute("data-dashboard-goto");
    paged = true;
    window.htmx.trigger(poller, "dashboard:page");
  });

  // An Activity page's open tab and that tab's page are its URL's (owner,
  // 2026-11-27: "selected tab should be both saved on page refresh and
  // reflected in the url"): ?tab=<value>, and &page=<n> past the first. The
  // URL is replaced, not pushed, so Back still leaves the page; it moves
  // when a person opens a tab or a page, never at a refresh alone.
  var paged = false;

  function activityURL() {
    var root = document.querySelector("[data-activity-tabs]");
    var tab = root && root.querySelector('[role="tab"][aria-selected="true"]');
    if (!tab) return;
    var value = tab.getAttribute("data-templ-value");
    var list = document.querySelector('[data-dashboard-page-list="' + root.getAttribute("data-activity-tabs") + "-" + value + '"]');
    var page = list ? list.getAttribute("data-dashboard-page") : "1";
    var url = new URL(window.location.href);
    url.searchParams.set("tab", value);
    if (page && page !== "1") url.searchParams.set("page", page);
    else url.searchParams.delete("page");
    window.history.replaceState(window.history.state, "", url);
  }

  // The tabs component announces a change before it makes it.
  document.addEventListener("tabs-value-change", function (e) {
    if (!e.target || !e.target.closest || !e.target.closest("[data-activity-tabs]")) return;
    window.setTimeout(activityURL, 0);
  });

  // htmx raises an out-of-band swap's events with the swapped element as
  // the detail's target; the event's own target is the requester afterwards.
  function swapped(e) {
    return (e.detail && e.detail.target) || e.target;
  }

  document.addEventListener("htmx:oobAfterSwap", function (e) {
    var card = swapped(e);
    if (!paged || !card || card.id !== "activity-card") return;
    paged = false;
    activityURL();
  });

  // A row's menu and its popover are moved to <body> while they are open
  // (ui/components/baseui/portal.js) and stay there, closed, afterwards. The
  // lists' poll waits while one is open (views.activityPollTrigger), and a
  // redraw first takes away every one the old rows own: the morph draws each
  // row's again where it is declared, from what the row now is.
  var FLOAT = '[data-slot="dropdown-menu-content"][data-open], [data-slot="popover-content"][data-open]';

  window.activityFloatOpen = function () {
    return !!document.querySelector("[data-activity]") && !!document.querySelector(FLOAT);
  };

  document.addEventListener("htmx:oobBeforeSwap", function (e) {
    var card = swapped(e);
    if (!card || card.id !== "activity-card") return;
    var portal = window.templ && window.templ.portal;
    document.querySelectorAll("body > [data-base-ui-portal]").forEach(function (node) {
      var owner = node._templPortalOwner;
      if (!owner || !card.contains(owner)) return;
      if (portal && portal.remove) portal.remove(node);
      else node.remove();
    });
  });
})();
