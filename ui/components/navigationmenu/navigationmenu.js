// Clustarr's own component (2026-09-24): a port of Base UI's NavigationMenu
// behaviour for navigationmenu.templ, which shadcn-templ's registry lacks.
// Uses window.FloatingUIDOM from components/floatingui (the same bundle).
//
// A trigger opens its item's content after the root's delay on hover, at
// once on click (a click on the open trigger closes); the pointer leaving
// the root -- the popup is inside it -- closes after the close delay, as
// does focus leaving it, Escape (focus returns to the trigger), a press
// outside, and a click on a link marked close-on-click. The open content
// is moved into the popup's viewport, the previous one slides out the way
// Base UI's does (data-activation-direction says which way), the popup is
// sized to the content through --popup-width/height and
// --positioner-width/height and placed against the trigger with floating
// ui (side, align and the offsets from the positioner's attributes,
// --transform-origin for the popup's scale); a switch between items slides
// the positioner, a first open lands it without a transition
// (data-instant). The arrow keys move focus along the triggers (the
// orientation picks the axis, the writing direction the order) and carry
// an open menu along; the arrow into the content (down under a horizontal
// list) opens and focuses its first link; Home and End jump.
//
// State follows Base UI's public contract: data-popup-open on the trigger,
// data-open/closed with data-starting-style and data-ending-style on the
// content, popup and positioner, data-side and data-align on the
// positioner and popup, and the root's data-tui-navigation-menu-value for
// the open item, which navigation-menu-value-change announces before it
// changes (cancelable; a controlled root never changes it itself).
(function () {
  "use strict";

  const EXIT_MS = 350; // the tsx's 0.35s transitions
  const q = (v) => (window.CSS && CSS.escape ? CSS.escape(v) : String(v).replace(/"/g, '\\"'));
  const byId = (root, part) =>
    "[data-tui-navigation-menu-" + part + '][data-tui-navigation-menu-id="' + q(root.getAttribute("data-tui-navigation-menu-id")) + '"]';

  function rootOf(el) {
    return el && el.closest ? el.closest("[data-tui-navigation-menu]") : null;
  }
  function partsOf(root) {
    return {
      positioner: root.querySelector(byId(root, "positioner")),
      popup: root.querySelector(byId(root, "positioner") + " [data-tui-navigation-menu-popup]"),
      viewport: root.querySelector(byId(root, "positioner") + " [data-tui-navigation-menu-viewport]"),
      indicator: root.querySelector(byId(root, "indicator")),
    };
  }
  function triggersOf(root) {
    return [...root.querySelectorAll(byId(root, "trigger"))];
  }
  function triggerFor(root, value) {
    return root.querySelector(byId(root, "trigger") + '[data-tui-navigation-menu-value="' + q(value) + '"]');
  }
  function contentFor(root, value) {
    return root.querySelector(byId(root, "content") + '[data-tui-navigation-menu-value="' + q(value) + '"]');
  }
  function current(root) {
    return root.getAttribute("data-tui-navigation-menu-value") || "";
  }
  function num(el, name, fallback) {
    const n = parseInt(el.getAttribute(name), 10);
    return isNaN(n) ? fallback : n;
  }
  function timers(root) {
    return root._tuiNav || (root._tuiNav = { open: 0, close: 0, exit: 0 });
  }
  function nextFrames(fn) {
    requestAnimationFrame(() => requestAnimationFrame(fn));
  }

  // ----- geometry ------------------------------------------------------------

  function origin(side, align) {
    const x = align === "start" ? "left" : align === "end" ? "right" : "center";
    if (side === "bottom") return "top " + x;
    if (side === "top") return "bottom " + x;
    const y = align === "start" ? "top" : align === "end" ? "bottom" : "center";
    return (side === "right" ? "left " : "right ") + y;
  }

  // The content's own size, measured free of the popup's current one.
  // Nothing paints between the two style writes, so the content is not
  // hidden for the measure: a link inside it wears transition-all, and a
  // visibility that flips hidden and back would transition, leaving the
  // link unfocusable for the transition's length (an arrow into the
  // content right after it opened found it so).
  // The author's inline style stays (a width set there or by a class
  // holds); min-width keeps the old popup's width from wrapping a wider
  // content, and the tsx's h-full gives way to the content's own height.
  function sizeOf(content) {
    const saved = content.style.cssText;
    content.style.cssText =
      saved +
      ";position:absolute;top:0;left:0;min-width:max-content;" +
      (/(^|;)\s*height\s*:/.test(saved) ? "" : "height:auto;");
    const w = content.offsetWidth, h = content.offsetHeight;
    content.style.cssText = saved;
    return { w: Math.min(w, document.documentElement.clientWidth - 10), h };
  }

  function place(root, trigger, instant) {
    const p = partsOf(root);
    const pos = p.positioner;
    if (!pos || !window.FloatingUIDOM) return Promise.resolve();
    const { computePosition, offset, flip, shift } = window.FloatingUIDOM;
    const side = pos.getAttribute("data-tui-navigation-menu-side") || "bottom";
    const align = pos.getAttribute("data-tui-navigation-menu-align") || "start";
    const placement = align === "center" ? side : side + "-" + align;
    const sideOffset = num(pos, "data-tui-navigation-menu-side-offset", 8);
    const alignOffset = num(pos, "data-tui-navigation-menu-align-offset", 0);
    if (instant) pos.setAttribute("data-instant", "");
    return computePosition(trigger, pos, {
      placement,
      strategy: "absolute",
      middleware: [offset({ mainAxis: sideOffset, crossAxis: alignOffset }), flip(), shift({ padding: 5 })],
    }).then((r) => {
      pos.style.left = r.x + "px";
      pos.style.top = r.y + "px";
      const finalSide = r.placement.split("-")[0];
      const finalAlign = r.placement.split("-")[1] || "center";
      const tb = trigger.getBoundingClientRect();
      pos.style.setProperty("--anchor-width", tb.width + "px");
      pos.style.setProperty("--anchor-height", tb.height + "px");
      pos.style.setProperty("--available-width", document.documentElement.clientWidth - 10 + "px");
      pos.style.setProperty("--available-height", document.documentElement.clientHeight - 10 + "px");
      pos.style.setProperty("--transform-origin", origin(finalSide, finalAlign));
      [pos, p.popup].forEach((el) => {
        if (!el) return;
        el.setAttribute("data-side", finalSide);
        el.setAttribute("data-align", finalAlign);
      });
      if (instant) {
        pos.offsetHeight; // flush the move before transitions come back
        pos.removeAttribute("data-instant");
      }
      const ind = p.indicator;
      if (ind) {
        const rb = root.getBoundingClientRect();
        ind.hidden = false;
        ind.style.left = tb.left - rb.left + "px";
        ind.style.width = tb.width + "px";
        ind.setAttribute("data-state", "visible");
      }
    });
  }

  // ----- state ---------------------------------------------------------------

  // The content of value leaves the viewport: it slides out towards dir
  // (or fades, on a close) and goes home to its item once done.
  function retire(root, value, dir) {
    const trigger = triggerFor(root, value);
    if (trigger) {
      trigger.setAttribute("aria-expanded", "false");
      trigger.removeAttribute("data-popup-open");
    }
    const content = contentFor(root, value);
    if (!content) return;
    content.removeAttribute("data-open");
    content.removeAttribute("data-starting-style");
    content.setAttribute("data-closed", "");
    content.setAttribute("data-ending-style", "");
    if (dir) content.setAttribute("data-activation-direction", dir);
    else content.removeAttribute("data-activation-direction");
    // Out of the flow, so the next content takes the viewport at once.
    content.style.position = "absolute";
    content.style.inset = "0";
    setTimeout(() => {
      if (!content.hasAttribute("data-ending-style")) return;
      content.hidden = true;
      content.removeAttribute("data-ending-style");
      content.style.position = "";
      content.style.inset = "";
      if (content._tuiHome && content._tuiHome.isConnected) content._tuiHome.appendChild(content);
    }, EXIT_MS);
  }

  function open(root, value, opts) {
    opts = opts || {};
    const t = timers(root);
    clearTimeout(t.open);
    clearTimeout(t.close);
    const prev = current(root);
    if (prev === value) return;
    const trigger = triggerFor(root, value);
    const content = contentFor(root, value);
    const p = partsOf(root);
    if (!trigger || !content || !p.positioner || !p.popup || !p.viewport) return;
    let dir = "";
    if (prev) {
      const order = triggersOf(root);
      dir = order.indexOf(trigger) > order.indexOf(triggerFor(root, prev)) ? "right" : "left";
      retire(root, prev, dir);
    }
    clearTimeout(t.exit);
    root.setAttribute("data-tui-navigation-menu-value", value);
    trigger.setAttribute("aria-expanded", "true");
    trigger.setAttribute("data-popup-open", "");

    // The content moves into the viewport, as Base UI moves it.
    if (!content._tuiHome) content._tuiHome = content.parentElement;
    p.viewport.appendChild(content);
    content.hidden = false;
    content.style.position = "";
    content.style.inset = "";
    content.removeAttribute("data-closed");
    content.removeAttribute("data-ending-style");
    if (dir) content.setAttribute("data-activation-direction", dir);
    else content.removeAttribute("data-activation-direction");
    content.setAttribute("data-open", "");
    content.setAttribute("data-starting-style", "");

    // A first open shows the positioner before the content is measured: a
    // hidden ancestor measures as nothing. Its starting style keeps the
    // popup invisible until it is placed.
    const first = !prev;
    if (first) {
      [p.positioner, p.popup].forEach((el) => {
        el.hidden = false;
        el.removeAttribute("data-closed");
        el.removeAttribute("data-ending-style");
        el.setAttribute("data-open", "");
        el.setAttribute("data-starting-style", "");
      });
    }
    const size = sizeOf(content);
    p.positioner.style.setProperty("--positioner-width", size.w + "px");
    p.positioner.style.setProperty("--positioner-height", size.h + "px");
    p.positioner.style.setProperty("--popup-width", size.w + "px");
    p.positioner.style.setProperty("--popup-height", size.h + "px");
    place(root, trigger, first || opts.instant).then(() => {
      nextFrames(() => {
        content.removeAttribute("data-starting-style");
        if (first) {
          p.positioner.removeAttribute("data-starting-style");
          p.popup.removeAttribute("data-starting-style");
        }
      });
    });
  }

  function close(root) {
    const t = timers(root);
    clearTimeout(t.open);
    clearTimeout(t.close);
    const value = current(root);
    if (!value) return;
    root.removeAttribute("data-tui-navigation-menu-value");
    retire(root, value, "");
    const p = partsOf(root);
    [p.positioner, p.popup].forEach((el) => {
      if (!el) return;
      el.removeAttribute("data-open");
      el.removeAttribute("data-starting-style");
      el.setAttribute("data-closed", "");
      el.setAttribute("data-ending-style", "");
    });
    if (p.indicator) p.indicator.setAttribute("data-state", "hidden");
    t.exit = setTimeout(() => {
      if (p.positioner && p.positioner.hasAttribute("data-ending-style")) {
        p.positioner.hidden = true;
        p.positioner.removeAttribute("data-ending-style");
        if (p.popup) p.popup.removeAttribute("data-ending-style");
      }
      if (p.indicator && p.indicator.getAttribute("data-state") === "hidden") p.indicator.hidden = true;
    }, EXIT_MS);
  }

  // value "" asks to close. A controlled root only announces.
  function request(root, value) {
    if (!root || current(root) === value) return;
    const accepted = root.dispatchEvent(
      new CustomEvent("navigation-menu-value-change", {
        bubbles: true,
        cancelable: true,
        detail: { value: value || null },
      }),
    );
    if (!accepted || root.hasAttribute("data-tui-navigation-menu-controlled")) return;
    if (value) open(root, value);
    else close(root);
  }
  function delay(root) {
    return num(root, "data-tui-navigation-menu-delay", 50);
  }
  function closeDelay(root) {
    return num(root, "data-tui-navigation-menu-close-delay", 50);
  }

  // ----- events --------------------------------------------------------------

  document.addEventListener("pointerover", (e) => {
    if (e.pointerType === "touch") return;
    const root = rootOf(e.target);
    if (!root) return;
    const t = timers(root);
    clearTimeout(t.close);
    const trigger = e.target.closest("[data-tui-navigation-menu-trigger]");
    if (!trigger || rootOf(trigger) !== root || trigger.disabled) return;
    const value = trigger.getAttribute("data-tui-navigation-menu-value");
    if (current(root) === value) return;
    clearTimeout(t.open);
    t.open = setTimeout(() => request(root, value), delay(root));
  });

  document.addEventListener("pointerout", (e) => {
    if (e.pointerType === "touch") return;
    const root = rootOf(e.target);
    if (!root || (e.relatedTarget && root.contains(e.relatedTarget))) return;
    const t = timers(root);
    clearTimeout(t.open);
    if (!current(root)) return;
    clearTimeout(t.close);
    t.close = setTimeout(() => request(root, ""), closeDelay(root));
  });

  document.addEventListener("click", (e) => {
    const trigger = e.target.closest("[data-tui-navigation-menu-trigger]");
    if (trigger) {
      const root = rootOf(trigger);
      if (!root || trigger.disabled) return;
      const value = trigger.getAttribute("data-tui-navigation-menu-value");
      request(root, current(root) === value ? "" : value);
      return;
    }
    const link = e.target.closest("[data-tui-navigation-menu-close-on-click]");
    if (link) request(rootOf(link), "");
  });

  // A press outside an open menu closes it.
  document.addEventListener("pointerdown", (e) => {
    document.querySelectorAll("[data-tui-navigation-menu][data-tui-navigation-menu-value]").forEach((root) => {
      if (!root.contains(e.target)) request(root, "");
    });
  });

  document.addEventListener("focusin", (e) => {
    const root = rootOf(e.target);
    if (root) clearTimeout(timers(root).close);
  });
  document.addEventListener("focusout", (e) => {
    const root = rootOf(e.target);
    if (!root || !current(root) || (e.relatedTarget && root.contains(e.relatedTarget))) return;
    const t = timers(root);
    clearTimeout(t.close);
    t.close = setTimeout(() => request(root, ""), closeDelay(root));
  });

  document.addEventListener("keydown", (e) => {
    const root = rootOf(e.target);
    if (!root) return;
    if (e.key === "Escape") {
      const value = current(root);
      if (!value) return;
      e.preventDefault();
      const trigger = triggerFor(root, value);
      request(root, "");
      if (trigger) trigger.focus();
      return;
    }
    const trigger = e.target.closest("[data-tui-navigation-menu-trigger]");
    if (!trigger || rootOf(trigger) !== root) return;
    const vertical = root.getAttribute("data-orientation") === "vertical";
    const rtl = getComputedStyle(root).direction === "rtl";
    const prev = vertical ? "ArrowUp" : rtl ? "ArrowRight" : "ArrowLeft";
    const next = vertical ? "ArrowDown" : rtl ? "ArrowLeft" : "ArrowRight";
    const into = vertical ? (rtl ? "ArrowLeft" : "ArrowRight") : "ArrowDown";
    if (e.key === into) {
      e.preventDefault();
      const value = trigger.getAttribute("data-tui-navigation-menu-value");
      if (current(root) !== value) request(root, value);
      const content = contentFor(root, value);
      const first = content && !content.hidden && content.querySelector('a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])');
      if (first) first.focus();
      return;
    }
    const all = triggersOf(root).filter((t) => !t.disabled);
    const i = all.indexOf(trigger);
    if (i < 0) return;
    let target = null;
    if (e.key === next) target = all[(i + 1) % all.length];
    else if (e.key === prev) target = all[(i - 1 + all.length) % all.length];
    else if (e.key === "Home") target = all[0];
    else if (e.key === "End") target = all[all.length - 1];
    if (!target) return;
    e.preventDefault();
    target.focus();
    if (current(root)) request(root, target.getAttribute("data-tui-navigation-menu-value"));
  });

  // An item rendered open (the root's Value or DefaultValue) has its content
  // in its item still; move it into the viewport and land the popup without
  // a transition.
  function init() {
    document.querySelectorAll("[data-tui-navigation-menu][data-tui-navigation-menu-value]").forEach((root) => {
      const value = current(root);
      const content = contentFor(root, value);
      const p = partsOf(root);
      if (!content || !p.viewport || content.parentElement === p.viewport) return;
      root.removeAttribute("data-tui-navigation-menu-value");
      open(root, value, { instant: true });
    });
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  // Re-init on any childList mutation, directly (never rAF-deferred: rAF
  // does not fire in hidden tabs or throttled iframes): swapped-in markup
  // wires itself.
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });

  window.tui = window.tui || {};
  window.tui.navigationMenu = {
    open: (root, value) => open(root, value),
    close: (root) => close(root),
  };
})();
