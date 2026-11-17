// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
//
// The Live TV pages' small behaviours (the direct-tuners and ui design
// 2026-10-27): a [data-copy] button writes its value to the clipboard and
// says so; a channel logo that will not load gives way to its number tile;
// a <time data-local="datetime"> reads in the browser's zone. Delegated on
// the document, so rows htmx swaps in need nothing more. (A bulk delete's
// question is an alert dialog since 2026-11-04, whose Cancel is its own.)
(function () {
  "use strict";

  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest("[data-copy]");
    if (!btn || !btn.getAttribute("data-copy")) {
      return;
    }
    ev.preventDefault();
    var value = btn.getAttribute("data-copy");
    var done = function () {
      var before = btn.getAttribute("title") || "";
      btn.setAttribute("title", "Copied");
      btn.setAttribute("data-copied", "");
      setTimeout(function () {
        btn.setAttribute("title", before);
        btn.removeAttribute("data-copied");
      }, 1500);
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(value).then(done, function () {});
      return;
    }
    // Plain http behind an ingress: the old way.
    var ta = document.createElement("textarea");
    ta.value = value;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
      done();
    } catch (e) {}
    document.body.removeChild(ta);
  });

  // A logo the tuner has not stored yet answers 404: show the tile. An
  // image may fail before this script runs (it is deferred), so the
  // ones already broken are swept at load and after every swap too.
  var toTile = function (img) {
    img.hidden = true;
    var tile = img.nextElementSibling;
    if (tile && tile.hasAttribute("data-logo-tile")) {
      tile.hidden = false;
    }
  };
  document.addEventListener(
    "error",
    function (ev) {
      var img = ev.target;
      if (img && img.tagName === "IMG" && img.hasAttribute("data-logo")) {
        toTile(img);
      }
    },
    true
  );
  var sweep = function (root) {
    root.querySelectorAll("img[data-logo]").forEach(function (img) {
      if (img.complete && img.naturalWidth === 0) {
        toTile(img);
      }
    });
  };

  var localise = function (root) {
    root.querySelectorAll('time[data-local="datetime"]').forEach(function (t) {
      var d = new Date(t.getAttribute("datetime"));
      if (!isNaN(d)) {
        t.textContent = d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
      }
    });
  };
  // Channels: the head's mark selects every row on the page, and the bulk
  // form says how many are selected (it had neither, 2026-11-17).
  function channelBoxes() {
    return Array.prototype.slice.call(document.querySelectorAll("#channels-table [data-channel-select]"));
  }
  function isOn(box) {
    return box.getAttribute("aria-checked") === "true";
  }
  function tallyChannels() {
    var boxes = channelBoxes();
    var n = boxes.filter(isOn).length;
    document.querySelectorAll("[data-channels-selected]").forEach(function (el) {
      var text = n + " selected";
      if (el.textContent !== text) el.textContent = text;
      el.toggleAttribute("data-some", n > 0);
    });
    var head = document.querySelector("[data-channel-select-all]");
    if (head) head.setAttribute("aria-checked", n === 0 ? "false" : n === boxes.length ? "true" : "mixed");
  }
  document.addEventListener("click", function (ev) {
    if (!ev.target.closest) return;
    var head = ev.target.closest("[data-channel-select-all]");
    if (head) {
      var boxes = channelBoxes();
      var turnOn = !boxes.every(isOn);
      boxes.forEach(function (box) {
        if (isOn(box) !== turnOn) box.click();
      });
    }
    // After the checkbox's own handler has run.
    if (head || ev.target.closest("#channels-table")) setTimeout(tallyChannels, 0);
  });
  document.addEventListener("keyup", function (ev) {
    if (ev.target.closest && ev.target.closest("#channels-table")) setTimeout(tallyChannels, 0);
  });
  document.addEventListener("htmx:afterSettle", tallyChannels);

  document.addEventListener("DOMContentLoaded", function () {
    localise(document);
    sweep(document);
  });
  // The whole document, not the swap's target: a dialog htmx swaps in is
  // moved to <body> before htmx settles it (ui/static/portals.js).
  document.addEventListener("htmx:afterSettle", function () {
    localise(document);
    sweep(document);
  });
})();
