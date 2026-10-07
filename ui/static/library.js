/*
 * The library page's mass editor and Options (2026-10-06, after Sonarr's).
 *
 * - [data-action="select-toggle"] turns selecting on and off: it toggles
 *   data-selecting on #library-page, whose group variants swap the
 *   toolbar's label and show the cards' marks, Select All and the bulk bar.
 *   While selecting, a click on a card ([data-ref]) selects it instead of
 *   opening it (data-selected="true"); [data-action="select-all"] selects every card on screen.
 *   The selection is kept here, by item, and re-applied whenever the live
 *   stream or the infinite scroll replaces the cards. Turning selecting off,
 *   or a view change that replaces #library-page, clears it.
 * - A [data-bulk-form] submits the selection as repeated "item" fields,
 *   namespace/kind/name, read from each card's data-ref and data-kind;
 *   [data-bulk-action] buttons are disabled while nothing is selected, and
 *   every [data-selected-count] shows how many are.
 * - The Options menu's poster size ([data-poster-size] radio items) and
 *   "Show details" ([data-library-option="details"]) are the reader's own:
 *   kept in localStorage and applied to <html> as --library-poster-width and
 *   data-library-details="hidden", so they outlast every swap.
 *
 * Bound once by delegation.
 */
(function () {
  "use strict";
  var selected = new Set();

  function page() {
    return document.getElementById("library-page");
  }
  function selecting() {
    var p = page();
    return !!(p && p.hasAttribute("data-selecting"));
  }
  function itemOf(card) {
    var ref = (card.getAttribute("data-ref") || "").split("/");
    if (ref.length !== 2) return "";
    return ref[0] + "/" + card.getAttribute("data-kind") + "/" + ref[1];
  }
  function cards() {
    var p = page();
    return p ? Array.prototype.slice.call(p.querySelectorAll("#library-rows [data-ref]")) : [];
  }

  // render re-applies the selection to the cards on screen and the counts
  // and buttons to it.
  function render() {
    // data-selected="true": shadcn's data-selected variant matches the
    // value, not the bare attribute.
    cards().forEach(function (card) {
      if (selected.has(itemOf(card))) card.setAttribute("data-selected", "true");
      else card.removeAttribute("data-selected");
    });
    document.querySelectorAll("[data-selected-count]").forEach(function (el) {
      el.textContent = String(selected.size);
    });
    document.querySelectorAll("[data-bulk-action]").forEach(function (el) {
      el.disabled = selected.size === 0;
      el.toggleAttribute("data-disabled", selected.size === 0);
    });
  }

  function setSelecting(on) {
    var p = page();
    if (!p) return;
    p.toggleAttribute("data-selecting", on);
    p.querySelectorAll('[data-action="select-toggle"]').forEach(function (b) {
      b.setAttribute("aria-pressed", on ? "true" : "false");
    });
    if (!on) selected.clear();
    render();
  }

  document.addEventListener(
    "click",
    function (e) {
      if (!e.target.closest) return;
      if (e.target.closest('[data-action="select-toggle"]')) {
        e.preventDefault();
        setSelecting(!selecting());
        return;
      }
      if (e.target.closest('[data-action="select-all"]')) {
        e.preventDefault();
        cards().forEach(function (card) {
          var it = itemOf(card);
          if (it) selected.add(it);
        });
        render();
        return;
      }
      if (!selecting()) return;
      var card = e.target.closest("#library-rows [data-ref]");
      if (!card) return;
      // A selecting click selects; it never follows the card's link.
      e.preventDefault();
      var it = itemOf(card);
      if (!it) return;
      if (selected.has(it)) selected.delete(it);
      else selected.add(it);
      render();
    },
    true
  );

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && selecting() && !document.querySelector('[role="dialog"]:not([hidden])')) setSelecting(false);
  });

  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form || !form.hasAttribute || !form.hasAttribute("data-bulk-form")) return;
    if (selected.size === 0) {
      e.preventDefault();
      return;
    }
    form.querySelectorAll('input[name="item"]').forEach(function (el) {
      el.remove();
    });
    selected.forEach(function (it) {
      var input = document.createElement("input");
      input.type = "hidden";
      input.name = "item";
      input.value = it;
      form.appendChild(input);
    });
  });

  // The stream replaces the cards' markup on every frame, the infinite
  // scroll the whole grid, and a view change the whole page, which drops
  // selecting with it.
  document.addEventListener("htmx:afterSettle", function () {
    if (!selecting()) selected.clear();
    render();
  });
  document.addEventListener("htmx:sseMessage", render);

  // Options.
  var SIZE_KEY = "clustarr.library.posterSize";
  var DETAILS_KEY = "clustarr.library.details";
  function stored(key) {
    try {
      return window.localStorage.getItem(key);
    } catch (err) {
      return null;
    }
  }
  function store(key, value) {
    try {
      window.localStorage.setItem(key, value);
    } catch (err) {
      /* private mode: the choice lasts for this page only */
    }
  }
  function sizeItem(key) {
    return document.querySelector('[data-poster-size="' + key + '"]');
  }
  function setChecked(item, checked) {
    if (!item) return;
    item.toggleAttribute("data-checked", checked);
    item.toggleAttribute("data-unchecked", !checked);
    item.setAttribute("aria-checked", checked ? "true" : "false");
    var indicator = item.querySelector(":scope > span > span[aria-hidden]");
    if (indicator) indicator.hidden = !checked;
  }
  function applySize(key) {
    var item = sizeItem(key);
    var width = item && item.getAttribute("data-width");
    if (!width) return;
    document.documentElement.style.setProperty("--library-poster-width", width);
    document.querySelectorAll("[data-poster-size]").forEach(function (other) {
      setChecked(other, other === item);
    });
  }
  function applyDetails(show) {
    if (show) document.documentElement.removeAttribute("data-library-details");
    else document.documentElement.setAttribute("data-library-details", "hidden");
    setChecked(document.querySelector('[data-library-option="details"]'), show);
  }
  function applyOptions() {
    var size = stored(SIZE_KEY);
    if (size) applySize(size);
    applyDetails(stored(DETAILS_KEY) !== "hidden");
  }

  document.addEventListener("menubar-value-change", function (e) {
    var group = e.target.closest && e.target.closest("[data-poster-sizes]");
    if (!group || !e.detail || !e.detail.value) return;
    store(SIZE_KEY, e.detail.value);
    applySize(e.detail.value);
  });
  document.addEventListener("menubar-checked-change", function (e) {
    var item = e.target.closest && e.target.closest('[data-library-option="details"]');
    if (!item || !e.detail) return;
    store(DETAILS_KEY, e.detail.checked ? "shown" : "hidden");
    applyDetails(e.detail.checked);
  });

  document.addEventListener("htmx:afterSettle", applyOptions);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", applyOptions);
  } else {
    applyOptions();
  }
})();
