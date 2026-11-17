// Copyright 2026 The Clustarr Authors. GPL-3.0-or-later.
/*
 * Row selection for an inner table (owner, 2026-11-17: a season's episodes,
 * an artist's albums, an author's books, a comic's issues, the Live TV
 * channels). views.selectHead / selectCell draw the marks (a button
 * role=checkbox, no component script), views.RowAttrs names each row
 * (data-row-ref), views.selectionBar is the page's one bar.
 *
 * The selection is a page-level map of ref to the value posted for it (the
 * row's data-row-value, else the ref), so it survives a row swapped by its
 * own toggle, a season fetched again and a table refetched: rows are
 * re-marked when htmx settles, and a ref whose row is gone is dropped. A
 * click with Shift selects the range from the last row toggled, within one
 * table (its [data-select-scope]); the head's mark selects the whole table.
 * The bar's verbs post through htmx; the selected values are added as
 * "item" fields at htmx:configRequest. The server's rows-bulk-done event
 * clears the selection; a reply that reports failures keeps it.
 *
 * Only attributes and text nodes are written, each only when it differs,
 * so the component lifecycle's observer has nothing to rescan.
 */
(function () {
  "use strict";
  var selected = new Map();
  var anchor = null;

  function rowOf(el) {
    return el.closest("[data-row-ref]");
  }
  function refOf(row) {
    return row.getAttribute("data-row-ref");
  }
  function valueOf(row) {
    return row.getAttribute("data-row-value") || refOf(row);
  }
  function scopeOf(el) {
    return el.closest("[data-select-scope]") || document;
  }
  function rowsIn(scope) {
    return Array.prototype.slice.call(scope.querySelectorAll("[data-row-ref]"));
  }
  function setAttr(el, name, value) {
    if (value === null) {
      if (el.hasAttribute(name)) el.removeAttribute(name);
    } else if (el.getAttribute(name) !== value) {
      el.setAttribute(name, value);
    }
  }

  function render() {
    if (selected.size === 0 && !document.querySelector('[data-row-ref][data-state="selected"]')) {
      document.querySelectorAll("[data-selection-bar]").forEach(function (bar) {
        if (!bar.hidden) bar.hidden = true;
      });
      return;
    }
    document.querySelectorAll("[data-row-ref]").forEach(function (row) {
      var on = selected.has(refOf(row));
      setAttr(row, "data-state", on ? "selected" : null);
      var mark = row.querySelector("[data-select-row]");
      if (mark) setAttr(mark, "aria-checked", on ? "true" : "false");
    });
    document.querySelectorAll("[data-select-all]").forEach(function (all) {
      var rows = rowsIn(scopeOf(all));
      var n = rows.filter(function (r) {
        return selected.has(refOf(r));
      }).length;
      setAttr(all, "aria-checked", n === 0 ? "false" : n === rows.length ? "true" : "mixed");
    });
    document.querySelectorAll("[data-rows-count]").forEach(function (c) {
      var text = String(selected.size);
      if (c.firstChild) {
        if (c.firstChild.nodeValue !== text) c.firstChild.nodeValue = text;
      } else {
        c.textContent = text;
      }
    });
    document.querySelectorAll("[data-selection-bar]").forEach(function (bar) {
      var hide = selected.size === 0;
      if (bar.hidden !== hide) bar.hidden = hide;
    });
  }

  function set(row, on) {
    if (on) selected.set(refOf(row), valueOf(row));
    else selected.delete(refOf(row));
  }
  function clear() {
    selected.clear();
    anchor = null;
    render();
  }

  document.addEventListener("mousedown", function (e) {
    if (e.shiftKey && e.target.closest && e.target.closest("[data-select-row]")) e.preventDefault();
  });

  document.addEventListener("click", function (e) {
    var t = e.target.closest ? e.target : null;
    if (!t) return;
    var mark = t.closest("[data-select-row]");
    if (mark) {
      var row = rowOf(mark);
      if (!row) return;
      var on = !selected.has(refOf(row));
      var scope = scopeOf(row);
      var from = anchor && e.shiftKey ? scope.querySelector('[data-row-ref="' + CSS.escape(anchor) + '"]') : null;
      if (from) {
        var rows = rowsIn(scope);
        var a = rows.indexOf(from);
        var b = rows.indexOf(row);
        for (var i = Math.min(a, b); i <= Math.max(a, b); i++) set(rows[i], on);
      } else {
        set(row, on);
      }
      anchor = refOf(row);
      render();
      return;
    }
    var all = t.closest("[data-select-all]");
    if (all) {
      var rs = rowsIn(scopeOf(all));
      var every = rs.length > 0 && rs.every(function (r) {
        return selected.has(refOf(r));
      });
      rs.forEach(function (r) {
        set(r, !every);
      });
      render();
      return;
    }
    if (t.closest("[data-rows-clear]")) clear();
  });

  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape" || e.defaultPrevented || selected.size === 0) return;
    if (e.target.closest && e.target.closest("input, textarea, select, [contenteditable]")) return;
    if (document.querySelector('[role="dialog"]:not([hidden]), [role="alertdialog"]:not([hidden]), [role="menu"]:not([hidden]), [role="listbox"]:not([hidden])')) return;
    clear();
  });

  // The bar's verbs, and a dialog's form marked data-rows-form, post the
  // selection.
  document.addEventListener("htmx:configRequest", function (e) {
    var elt = e.detail && e.detail.elt;
    if (!elt || !elt.closest || !elt.closest("[data-selection-bar], [data-rows-form]")) return;
    if (selected.size === 0) {
      e.preventDefault();
      return;
    }
    selected.forEach(function (v) {
      e.detail.formData.append("item", v);
    });
  });

  function resync() {
    if (selected.size === 0) return;
    selected.forEach(function (_, ref) {
      var row = document.querySelector('[data-row-ref="' + CSS.escape(ref) + '"]');
      if (row) selected.set(ref, valueOf(row));
      else selected.delete(ref);
    });
    render();
  }
  document.addEventListener("htmx:afterSettle", resync);
  document.addEventListener("htmx:oobAfterSwap", resync);
  document.addEventListener("rows-bulk-done", clear);
})();
