/*
 * The library page's mass editor and Options (2026-10-06, after Sonarr's).
 *
 * - [data-action="select-toggle"] turns selecting on and off: it toggles
 *   data-selecting on #library-page, whose group variants swap the
 *   toolbar's label and show the cards' marks, Select All and the bulk bar.
 *   While selecting, a click on a card ([data-ref]) selects it instead of
 *   opening it (data-selected="true"); [data-action="select-all"] selects every card on screen.
 *   The selection is kept here, by item, and re-applied whenever the live
 *   stream or the infinite scroll replaces the cards -- the stream morphs
 *   them (idiomorph, 2026-10-21), which still syncs every attribute to the
 *   frame's markup and so strips data-selected. Turning selecting off,
 *   or a view change that replaces #library-page, clears it.
 * - A [data-bulk-form] submits the selection as repeated "item" fields,
 *   namespace/kind/name, read from each card's data-ref and data-kind;
 *   [data-bulk-action] buttons are disabled while nothing is selected, and
 *   every [data-selected-count] shows how many are.
 * - A card's own Delete ([data-card-delete], 2026-10-06) opens the same
 *   delete dialog for that item alone: the dialog then names it, and its
 *   form posts it instead of the selection; the bulk bar's Delete goes back
 *   to the selection. #action-status, where a card's other actions answer,
 *   is cleared a few seconds after each answer.
 * - The cards' actions are one bar ([data-card-actions], 2026-11-10), moved
 *   into the card under the pointer or holding focus, and parked back in
 *   [data-card-actions-home] before a card it sits in is morphed or
 *   removed; its buttons post to the card's own route, put in place of the
 *   placeholder at htmx:configRequest.
 * - The stream's library-manifest event (2026-11-10, the library no longer
 *   paginates) lists the cards the view keeps, in order, each with its
 *   fingerprint: the new and changed ones are fetched from
 *   POST /library/{tab}/cards and morphed in (Idiomorph, so a card keeps
 *   its loaded poster), the ones gone dropped, the rest put in order, and
 *   the placeholder and the filter's "n of m" follow.
 * - Saved custom filters are the reader's own, per tab, in localStorage
 *   (clustarr.library.filters): the Filter menu lists them
 *   ([data-saved-filters], from its template), the save dialog adds the
 *   page's view under a name, an entry's x forgets it. [data-open-dialog]
 *   opens a dialog by id; [data-filter-remove] drops a builder row.
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
  // single is the item a card's Delete opened the dialog for, else null.
  var single = null;

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
    if (!single) {
      document.querySelectorAll("[data-delete-count]").forEach(function (el) {
        el.textContent = String(selected.size);
      });
    }
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

  // deleteSubject names what the delete dialog deletes: the card's title
  // for a card's Delete, else its default, the selection.
  function deleteSubject(text) {
    document.querySelectorAll("[data-delete-subject]").forEach(function (el) {
      el.textContent = text || el.getAttribute("data-default") || "";
    });
    document.querySelectorAll("[data-delete-count]").forEach(function (el) {
      el.textContent = String(text ? 1 : selected.size);
    });
  }

  document.addEventListener(
    "click",
    function (e) {
      if (!e.target.closest) return;
      var cardDelete = e.target.closest("[data-card-delete]");
      if (cardDelete) {
        var owner = cardDelete.closest("[data-ref]");
        single = owner ? itemOf(owner) : null;
        var link = owner && owner.querySelector("[data-card-link]");
        deleteSubject(link ? link.getAttribute("aria-label") : single);
        return;
      }
      if (e.target.closest('[data-bulk-action="delete"]')) {
        single = null;
        deleteSubject(null);
        return;
      }
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
    // Only the delete dialog's form carries a hidden action field; the
    // bar's form names its action on the button.
    var items = single && form.querySelector('input[name="action"][value="delete"]') ? new Set([single]) : selected;
    if (items.size === 0) {
      e.preventDefault();
      return;
    }
    form.querySelectorAll('input[name="item"]').forEach(function (el) {
      el.remove();
    });
    items.forEach(function (it) {
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

  var statusTimer = null;
  document.addEventListener("htmx:afterSwap", function (e) {
    if (!e.target || e.target.id !== "action-status") return;
    clearTimeout(statusTimer);
    // A question (a bulk delete's) waits for its answer.
    if (e.target.querySelector("[data-action-confirm]")) return;
    statusTimer = setTimeout(function () {
      e.target.innerHTML = "";
    }, 4000);
  });

  // The cards' shared action bar.
  function actionBar() {
    return document.querySelector("[data-card-actions]");
  }
  function parkBar() {
    var bar = actionBar();
    var home = document.querySelector("[data-card-actions-home]");
    if (bar && home && bar.parentNode !== home) home.appendChild(bar);
  }
  function cardOf(target) {
    return target && target.closest ? target.closest("#library-rows [data-ref]") : null;
  }
  function placeBar(card) {
    var poster = card && card.querySelector("[data-card-poster]");
    var bar = actionBar();
    if (poster && bar && bar.parentNode !== poster) poster.appendChild(bar);
  }
  // A move of the bar is a DOM mutation, and every mutation has the
  // components' lifecycle observer rescan the page (about 10 ms over a
  // thousand cards), so a pointer sweeping across the grid moves it only
  // once it rests on a card; the bar fades in over longer than the wait.
  var barTimer = null;
  document.addEventListener("pointerover", function (e) {
    var card = cardOf(e.target);
    clearTimeout(barTimer);
    if (!card) return;
    var pointerType = e.pointerType || "mouse";
    barTimer = setTimeout(function () {
      if (!card.matches(":hover")) return;
      placeBar(card);
      attachTooltip(card, function (link) {
        // The pointer is already over the link: tell the tooltip so, as
        // the browser would have had it been there.
        if (!link.matches(":hover")) return;
        link.dispatchEvent(new PointerEvent("pointerenter", { pointerType: pointerType }));
        link.dispatchEvent(new MouseEvent("mouseenter"));
      });
    }, 60);
  });
  document.addEventListener("focusin", function (e) {
    var card = cardOf(e.target);
    placeBar(card);
    attachTooltip(card, function (link) {
      // focusin on the link alone: the tooltip's focus interaction listens
      // there, and this one need not hear it again.
      if (document.activeElement === link) link.dispatchEvent(new FocusEvent("focusin"));
    });
  });

  // The cards' title tooltip (views.cardTooltip): no card renders one, so
  // the first rest of the pointer on a card, or focus entering it, clones
  // the page's one tooltip template into it, the card's link its trigger
  // and its label the link's aria-label. The components' lifecycle
  // observer starts the clone at the next microtask, after which opened
  // tells it of the hover or focus that is already under way. A stream
  // frame that morphs the card takes the tooltip away again, until the
  // next rest.
  function attachTooltip(card, opened) {
    var link = card && card.querySelector("[data-card-link]");
    var tpl = document.querySelector("template[data-card-tooltip]");
    if (!link || !tpl || !card.id) return;
    var id = "card-tip-" + card.id;
    if (link.getAttribute("data-templ-tooltip-trigger") === id && document.getElementById(id)) return;
    var tip = tpl.content.cloneNode(true);
    var content = tip.querySelector('[data-slot="tooltip-content"]');
    var label = tip.querySelector("[data-card-tooltip-label]");
    if (!content || !label) return;
    content.id = id;
    label.textContent = link.getAttribute("aria-label") || "";
    link.setAttribute("data-slot", "tooltip-trigger");
    link.setAttribute("data-base-ui-tooltip-trigger", "");
    link.setAttribute("data-templ-tooltip-trigger", id);
    link.after(tip);
    setTimeout(function () {
      opened(link);
    }, 0);
  }
  document.addEventListener("htmx:configRequest", function (e) {
    var elt = e.detail && e.detail.elt;
    if (!elt || !elt.closest || !elt.closest("[data-card-actions]")) return;
    var card = elt.closest("[data-ref]");
    var it = card ? itemOf(card) : "";
    if (!it) {
      e.preventDefault();
      return;
    }
    e.detail.path = "/library/" + it + "/" + elt.getAttribute("data-card-action");
  });

  // The stream's manifest.
  var pendingManifest = null;
  var fetchingCards = false;
  function parseManifest(data) {
    var lines = String(data || "").split("\n");
    var m = { total: 0, entries: [] };
    lines.forEach(function (line, i) {
      var parts = line.split(" ");
      if (i === 0 && parts[0] === "total") {
        m.total = parseInt(parts[1] || "0", 10) || 0;
        return;
      }
      if (parts.length === 2 && parts[0]) m.entries.push({ id: parts[0], fp: parts[1] });
    });
    return m;
  }
  function grid() {
    var rows = document.getElementById("library-rows");
    return rows ? rows.querySelector("[data-library-grid]") : null;
  }
  function placeCards(g, html) {
    var tpl = document.createElement("template");
    tpl.innerHTML = html;
    Array.prototype.slice.call(tpl.content.children).forEach(function (card) {
      var old = card.id ? document.getElementById(card.id) : null;
      if (old) {
        var bar = actionBar();
        if (bar && old.contains(bar)) parkBar();
        if (window.Idiomorph) window.Idiomorph.morph(old, card);
        else old.replaceWith(card);
      } else {
        g.appendChild(card);
        if (window.htmx) window.htmx.process(card);
      }
    });
  }
  function orderCards(g, m) {
    var want = new Set();
    m.entries.forEach(function (en) {
      want.add(en.id);
    });
    var bar = actionBar();
    Array.prototype.slice.call(g.children).forEach(function (el) {
      if (el.id && !want.has(el.id)) {
        if (bar && el.contains(bar)) parkBar();
        el.remove();
      }
    });
    var cur = g.firstElementChild;
    m.entries.forEach(function (en) {
      var el = document.getElementById(en.id);
      if (!el || el.parentNode !== g) return;
      if (el === cur) cur = cur.nextElementSibling;
      else g.insertBefore(el, cur);
    });
    var rows = document.getElementById("library-rows");
    var empty = rows && rows.querySelector('[data-empty="library"]');
    if (empty) empty.hidden = m.entries.length > 0;
    if (rows) rows.setAttribute("data-total", String(m.entries.length));
    document.querySelectorAll("[data-filter-count]").forEach(function (el) {
      el.textContent = m.entries.length + " of " + m.total;
    });
    render();
  }
  function applyManifest() {
    if (fetchingCards || pendingManifest === null) return;
    var m = parseManifest(pendingManifest);
    pendingManifest = null;
    var g = grid();
    var p = page();
    if (!g || !p) return;
    var need = m.entries.filter(function (en) {
      var el = document.getElementById(en.id);
      return !el || el.getAttribute("data-fp") !== en.fp;
    });
    if (!need.length) {
      orderCards(g, m);
      return;
    }
    var body = new URLSearchParams();
    need.forEach(function (en) {
      body.append("id", en.id);
    });
    fetchingCards = true;
    fetch("/library/" + p.getAttribute("data-tab") + "/cards", {
      method: "POST",
      body: body,
      credentials: "same-origin",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
    })
      .then(function (r) {
        return r.ok ? r.text() : Promise.reject(new Error("cards: " + r.status));
      })
      .then(function (html) {
        // The page may have been swapped for another view meanwhile.
        if (grid() !== g) return;
        placeCards(g, html);
        orderCards(g, m);
      })
      .catch(function () {
        /* the next manifest asks again */
      })
      .then(function () {
        fetchingCards = false;
        applyManifest();
      });
  }
  document.addEventListener("htmx:sseMessage", function (e) {
    if (!e.detail || e.detail.type !== "library-manifest") return;
    pendingManifest = e.detail.data;
    applyManifest();
  });

  // Dialogs opened from the Filter menu and the chips, and builder rows.
  document.addEventListener("click", function (e) {
    if (!e.target.closest) return;
    var opener = e.target.closest("[data-open-dialog]");
    if (opener) {
      if (opener.hasAttribute("data-disabled") || opener.getAttribute("aria-disabled") === "true") return;
      e.preventDefault();
      var id = opener.getAttribute("data-open-dialog");
      // After the menu closes and gives focus back.
      setTimeout(function () {
        if (window.templ && window.templ.dialog) window.templ.dialog.open(id);
      }, 0);
      return;
    }
    var remove = e.target.closest("[data-filter-remove]");
    if (remove) {
      e.preventDefault();
      var row = remove.closest("[data-filter-row]");
      if (row) row.remove();
    }
  });

  // Saved custom filters.
  var FILTERS_KEY = "clustarr.library.filters";
  function savedAll() {
    try {
      var all = JSON.parse(stored(FILTERS_KEY) || "{}");
      return all && typeof all === "object" ? all : {};
    } catch (err) {
      return {};
    }
  }
  function savedFor(tab) {
    var list = savedAll()[tab];
    return Array.isArray(list) ? list : [];
  }
  function setSaved(tab, list) {
    var all = savedAll();
    all[tab] = list;
    store(FILTERS_KEY, JSON.stringify(all));
  }
  function renderSaved() {
    document.querySelectorAll("[data-saved-filters]").forEach(function (box) {
      var tab = box.getAttribute("data-tab");
      var tpl = box.nextElementSibling;
      if (!tpl || !tpl.matches("template[data-saved-filter-template]")) return;
      box.innerHTML = "";
      var current = (page() && page().getAttribute("data-query")) || "";
      savedFor(tab).forEach(function (f, i) {
        if (!f || typeof f.name !== "string") return;
        var node = tpl.content.firstElementChild.cloneNode(true);
        var query = typeof f.query === "string" ? f.query : "";
        var href = "/library/" + tab + (query ? "?" + query : "");
        node.setAttribute("href", href);
        node.setAttribute("hx-get", href);
        node.setAttribute("hx-push-url", "true");
        node.setAttribute("hx-target", "#library-page");
        node.setAttribute("hx-select", "#library-page");
        node.setAttribute("hx-swap", "outerHTML");
        node.setAttribute("data-saved-filter", String(i));
        if (query === current) node.setAttribute("data-active", "");
        var name = node.querySelector("[data-saved-filter-name]");
        if (name) name.textContent = f.name;
        box.appendChild(node);
      });
      if (window.htmx) window.htmx.process(box);
    });
  }
  document.addEventListener(
    "click",
    function (e) {
      var del = e.target.closest ? e.target.closest("[data-saved-filter-delete]") : null;
      if (!del) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      var entry = del.closest("[data-saved-filter]");
      var box = del.closest("[data-saved-filters]");
      if (!entry || !box) return;
      var tab = box.getAttribute("data-tab");
      var list = savedFor(tab);
      list.splice(parseInt(entry.getAttribute("data-saved-filter"), 10), 1);
      setSaved(tab, list);
      renderSaved();
    },
    true
  );
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form || !form.hasAttribute || !form.hasAttribute("data-filter-save-form")) return;
    e.preventDefault();
    var input = form.querySelector('input[name="name"]');
    var name = input ? input.value.trim() : "";
    if (!name) return;
    var tab = form.getAttribute("data-tab");
    var list = savedFor(tab).filter(function (f) {
      return f && f.name !== name;
    });
    list.push({ name: name, query: form.getAttribute("data-query") || "" });
    setSaved(tab, list);
    renderSaved();
    if (input) input.value = "";
    if (window.templ && window.templ.dialog) window.templ.dialog.close("library-filter-save");
  });
  document.addEventListener("htmx:afterSettle", renderSaved);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", renderSaved);
  } else {
    renderSaved();
  }

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
