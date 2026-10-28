// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// The TV Guide's clock (the direct-tuners and ui design 2026-10-27 §5.6):
// the grid is placed from the guide's first instant (data-from) at 7.5 px a
// minute, server-side and timezone-free; here the browser scrolls it to now
// less 30 minutes, moves the now line every second, dims past programmes,
// fills the live one's progress bar, and labels hours and days in its own
// zone ("Today", "Tomorrow", a weekday).
(function () {
  "use strict";

  var PX_PER_MINUTE = 7.5;

  var guide = function () {
    return document.querySelector("[data-guide]");
  };
  var pxAt = function (g, t) {
    return ((t - Date.parse(g.getAttribute("data-from"))) / 60000) * PX_PER_MINUTE;
  };

  var dayLabel = function (d) {
    var today = new Date();
    today.setHours(0, 0, 0, 0);
    var day = new Date(d.getTime());
    day.setHours(0, 0, 0, 0);
    var diff = Math.round((day - today) / 86400000);
    if (diff === 0) return "Today";
    if (diff === 1) return "Tomorrow";
    if (diff === -1) return "Yesterday";
    return d.toLocaleDateString([], { weekday: "short" });
  };

  var label = function (root) {
    root.querySelectorAll('time[data-local="hour"]').forEach(function (t, i, all) {
      var d = new Date(t.getAttribute("datetime"));
      if (isNaN(d)) return;
      var text = d.toLocaleTimeString([], { hour: "numeric" });
      var first = t.closest("[data-window]");
      if (d.getHours() === 0 || (first && first.querySelector('time[data-local="hour"]') === t)) {
        text = dayLabel(d) + " " + text;
      }
      t.textContent = text;
    });
    root.querySelectorAll('time[data-local="time"]').forEach(function (t) {
      var d = new Date(t.getAttribute("datetime"));
      if (!isNaN(d)) t.textContent = d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
    });
  };

  var tick = function () {
    var g = guide();
    if (!g) return;
    var now = Date.now();
    var line = document.getElementById("guide-now");
    if (line) line.style.left = pxAt(g, now) + "px";
    g.querySelectorAll("[data-programme]").forEach(function (b) {
      var start = Date.parse(b.getAttribute("data-start"));
      var stop = Date.parse(b.getAttribute("data-stop"));
      if (stop <= now) b.setAttribute("data-past", "");
      else b.removeAttribute("data-past");
      var bar = b.querySelector("[data-progress]");
      if (bar) bar.style.width = start <= now && now < stop ? ((now - start) / (stop - start)) * 100 + "%" : "0";
    });
  };

  var place = function () {
    var g = guide();
    var scroller = document.getElementById("guide-scroll");
    if (!g || !scroller || scroller.hasAttribute("data-placed")) return;
    scroller.scrollLeft = Math.max(0, pxAt(g, Date.now() - 30 * 60000));
    scroller.setAttribute("data-placed", "");
  };

  document.addEventListener("DOMContentLoaded", function () {
    label(document);
    place();
    tick();
  });
  // The whole document: a programme's dialog is moved to <body> before
  // htmx settles it (ui/static/portals.js).
  document.addEventListener("htmx:afterSettle", function () {
    label(document);
    place();
    tick();
  });
  setInterval(tick, 1000);
})();
