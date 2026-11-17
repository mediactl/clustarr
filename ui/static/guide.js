// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// The TV Guide's clock (the direct-tuners and ui design 2026-10-27 §5.6):
// the grid is placed from the guide's first instant (data-from) at 7.5 px a
// minute, server-side and timezone-free; here the browser scrolls it to now
// less 30 minutes, moves the now line every 15 seconds, dims past programmes,
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

  // One formatter per shape, made once: toLocaleTimeString builds one per
  // call, and a guide holds nineteen thousand <time> elements.
  var hourFmt = new Intl.DateTimeFormat([], { hour: "numeric" });
  var timeFmt = new Intl.DateTimeFormat([], { hour: "numeric", minute: "2-digit" });

  // label fills the <time> elements not yet labelled (data-done): every
  // settle used to re-label all of them, about 4 s for one programme click.
  var label = function (root) {
    root.querySelectorAll('time[data-local="hour"]:not([data-done])').forEach(function (t) {
      t.setAttribute("data-done", "");
      var d = new Date(t.getAttribute("datetime"));
      if (isNaN(d)) return;
      var text = hourFmt.format(d);
      var first = t.closest("[data-window]");
      if (d.getHours() === 0 || (first && first.querySelector('time[data-local="hour"]') === t)) {
        text = dayLabel(d) + " " + text;
      }
      t.textContent = text;
    });
    root.querySelectorAll('time[data-local="time"]:not([data-done])').forEach(function (t) {
      t.setAttribute("data-done", "");
      var d = new Date(t.getAttribute("datetime"));
      if (!isNaN(d)) t.textContent = timeFmt.format(d);
    });
  };

  // line moves the now line; cells dims the programmes that are over and
  // fills the live ones' progress, a walk of every cell, so it runs once a
  // minute and when the guide itself gained cells, not every second.
  var line = function () {
    var g = guide();
    var el = document.getElementById("guide-now");
    if (g && el) el.style.left = pxAt(g, Date.now()) + "px";
  };
  var cells = function () {
    var g = guide();
    if (!g) return;
    var now = Date.now();
    g.querySelectorAll("[data-programme]").forEach(function (b) {
      var start = Date.parse(b.getAttribute("data-start"));
      var stop = Date.parse(b.getAttribute("data-stop"));
      if (stop <= now) {
        if (!b.hasAttribute("data-past")) b.setAttribute("data-past", "");
      } else if (b.hasAttribute("data-past")) {
        b.removeAttribute("data-past");
      }
      var bar = b.querySelector("[data-progress]");
      if (!bar) return;
      var width = start <= now && now < stop ? ((now - start) / (stop - start)) * 100 + "%" : "0";
      if (bar.style.width !== width) bar.style.width = width;
    });
  };
  var tick = function () {
    line();
    cells();
  };

  // place opens the guide on now: the now line a fifth of the way into what
  // is on screen, and never more than half an hour of the past before it
  // (a fixed half hour put the line at or past a phone's right edge, the
  // guide opening on what was over). force is the Now button's.
  var place = function (force) {
    var g = guide();
    var scroller = document.getElementById("guide-scroll");
    if (!g || !scroller || (!force && scroller.hasAttribute("data-placed"))) return;
    var lead = Math.min(pxAt(g, Date.now()) - pxAt(g, Date.now() - 30 * 60000), scroller.clientWidth * 0.2);
    var left = Math.max(0, pxAt(g, Date.now()) - lead);
    if (force && scroller.scrollTo) scroller.scrollTo({ left: left, behavior: "smooth" });
    else scroller.scrollLeft = left;
    scroller.setAttribute("data-placed", "");
  };
  document.addEventListener("click", function (e) {
    if (e.target.closest && e.target.closest("[data-guide-jump-now]")) place(true);
  });

  document.addEventListener("DOMContentLoaded", function () {
    label(document);
    place(false);
    tick();
  });
  // The whole document: a programme's dialog is moved to <body> before
  // htmx settles it (ui/static/portals.js).
  document.addEventListener("htmx:afterSettle", function (e) {
    label(document);
    place(false);
    var t = e.target;
    if (t && t.closest && t.closest("[data-guide]")) tick();
  });
  setInterval(line, 15000);
  setInterval(cells, 60000);
})();
