// Clustarr's own component (2026-09-24): a port of Base UI's ScrollArea
// behaviour for scrollarea.templ, which shadcn-templ's registry lacks.
//
// The viewport scrolls natively with its own bar hidden; this draws the
// bar over it. Each thumb's length is the viewport's share of the content
// and its place the scroll's share of the range, as Base UI computes them
// (never under 16px). A press on the thumb drags it (pointer capture, so
// the drag survives leaving the bar); a press on the track centres the
// thumb under the pointer and drags from there; a wheel over the bar
// scrolls the viewport under it. data-hovering marks the bars while the
// pointer is over the area, data-scrolling the area and thumbs for half a
// second after each scroll, data-has-overflow-x/y and
// data-overflow-*-start/end the root, viewport and content as Base UI
// sets them, with the --scroll-area-overflow-* distances on the viewport
// and --scroll-area-thumb-* on each bar. A bar with nothing to scroll
// leaves the DOM (hidden) unless it is kept mounted, and the corner shows
// only while both bars do.
//
// Binds once per root, on load and on every childList mutation, so htmx
// swaps wire themselves; a root is set up once, and every init re-measures
// it, since the swap may have replaced its content.
(function () {
  "use strict";

  const MIN_THUMB = 16; // Base UI's MIN_THUMB_SIZE
  const SCROLL_END_MS = 500; // data-scrolling lingers this long past the last scroll event
  const THUMB = "[data-tui-scroll-area-thumb]";
  const BAR = "[data-tui-scroll-area-scrollbar]";

  function padding(bar, vertical) {
    const cs = getComputedStyle(bar);
    return vertical
      ? parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom)
      : parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight);
  }

  function setup(root) {
    if (root._tuiScrollArea) {
      root._tuiScrollArea.observe();
      root._tuiScrollArea.measure();
      return;
    }
    const viewport = root.querySelector(":scope > [data-tui-scroll-area-viewport]");
    if (!viewport) return;
    const content = () => viewport.querySelector(":scope > [data-tui-scroll-area-content]");
    const corner = () => root.querySelector(":scope > [data-tui-scroll-area-corner]");
    const bars = () => [...root.querySelectorAll(":scope > " + BAR)];
    const state = { scrollTimer: 0, drag: null, observed: null };
    root._tuiScrollArea = state;

    function mark(name, on) {
      [root, viewport, content()].forEach((el) => el && el.toggleAttribute(name, on));
    }

    function measure() {
      const sh = viewport.scrollHeight, sw = viewport.scrollWidth;
      const ch = viewport.clientHeight, cw = viewport.clientWidth;
      const top = viewport.scrollTop, left = viewport.scrollLeft;
      const maxY = Math.max(0, sh - ch), maxX = Math.max(0, sw - cw);
      const hasY = maxY > 1, hasX = maxX > 1;
      mark("data-has-overflow-y", hasY);
      mark("data-has-overflow-x", hasX);
      mark("data-overflow-y-start", hasY && top > 0.5);
      mark("data-overflow-y-end", hasY && top < maxY - 0.5);
      mark("data-overflow-x-start", hasX && left > 0.5);
      mark("data-overflow-x-end", hasX && left < maxX - 0.5);
      viewport.style.setProperty("--scroll-area-overflow-y-start", top + "px");
      viewport.style.setProperty("--scroll-area-overflow-y-end", maxY - top + "px");
      viewport.style.setProperty("--scroll-area-overflow-x-start", left + "px");
      viewport.style.setProperty("--scroll-area-overflow-x-end", maxX - left + "px");

      let vBar = null, hBar = null;
      bars().forEach((bar) => {
        const vertical = bar.getAttribute("data-orientation") !== "horizontal";
        const has = vertical ? hasY : hasX;
        bar.hidden = !has && !bar.hasAttribute("data-tui-scroll-area-keep-mounted");
        if (bar.hidden) return;
        if (has) {
          if (vertical) vBar = bar;
          else hBar = bar;
        }
        const thumb = bar.querySelector(THUMB);
        if (!thumb) return;
        const track = (vertical ? bar.clientHeight : bar.clientWidth) - padding(bar, vertical);
        if (vertical) {
          const size = has ? Math.max(MIN_THUMB, Math.round((track * ch) / sh)) : track;
          const pos = maxY ? ((track - size) * top) / maxY : 0;
          thumb.style.height = size + "px";
          thumb.style.transform = "translate3d(0," + pos + "px,0)";
          bar.style.setProperty("--scroll-area-thumb-height", size + "px");
        } else {
          const size = has ? Math.max(MIN_THUMB, Math.round((track * cw) / sw)) : track;
          const pos = maxX ? ((track - size) * left) / maxX : 0;
          thumb.style.width = size + "px";
          thumb.style.transform = "translate3d(" + pos + "px,0,0)";
          bar.style.setProperty("--scroll-area-thumb-width", size + "px");
        }
      });

      const both = !!(vBar && hBar);
      root.style.setProperty("--scroll-area-corner-width", both ? vBar.offsetWidth + "px" : "0px");
      root.style.setProperty("--scroll-area-corner-height", both ? hBar.offsetHeight + "px" : "0px");
      const c = corner();
      if (c) c.hidden = !both;
    }
    state.measure = measure;

    // Watch the viewport and its current content for size changes; the
    // content element may be swapped, so re-observe on every init.
    const ro = typeof ResizeObserver !== "undefined" ? new ResizeObserver(measure) : null;
    if (ro) ro.observe(viewport);
    state.observe = function () {
      const c = content();
      if (!ro || !c || c === state.observed) return;
      if (state.observed) ro.unobserve(state.observed);
      state.observed = c;
      ro.observe(c);
    };
    state.observe();

    function scrolling() {
      mark("data-scrolling", true);
      root.querySelectorAll(THUMB).forEach((t) => t.setAttribute("data-scrolling", ""));
      clearTimeout(state.scrollTimer);
      state.scrollTimer = setTimeout(() => {
        mark("data-scrolling", false);
        root.querySelectorAll(THUMB).forEach((t) => t.removeAttribute("data-scrolling"));
      }, SCROLL_END_MS);
    }
    viewport.addEventListener("scroll", () => {
      measure();
      scrolling();
    }, { passive: true });

    function hovering(on) {
      root.toggleAttribute("data-hovering", on);
      bars().forEach((b) => b.toggleAttribute("data-hovering", on));
    }
    root.addEventListener("pointerenter", () => hovering(true));
    root.addEventListener("pointerleave", () => hovering(false));

    // Put the thumb's leading edge at pos along the track by scrolling the
    // viewport the matching share of its range.
    function scrollToThumb(vertical, pos, track, size) {
      const range = track - size;
      if (range <= 0) return;
      const share = Math.min(1, Math.max(0, pos / range));
      if (vertical) viewport.scrollTop = share * (viewport.scrollHeight - viewport.clientHeight);
      else viewport.scrollLeft = share * (viewport.scrollWidth - viewport.clientWidth);
    }

    root.addEventListener("pointerdown", (e) => {
      const bar = e.target.closest(BAR);
      if (!bar || bar.parentElement !== root || (e.button && e.button !== 0)) return;
      const thumb = bar.querySelector(THUMB);
      if (!thumb) return;
      e.preventDefault();
      const vertical = bar.getAttribute("data-orientation") !== "horizontal";
      const tb = thumb.getBoundingClientRect(), bb = bar.getBoundingClientRect();
      const pad = padding(bar, vertical) / 2;
      const track = (vertical ? bb.height : bb.width) - pad * 2;
      const size = vertical ? tb.height : tb.width;
      const start = (vertical ? bb.top : bb.left) + pad;
      const pointer = vertical ? e.clientY : e.clientX;
      let grab = pointer - (vertical ? tb.top : tb.left);
      if (!e.target.closest(THUMB)) {
        // A press on the track: the thumb centres under the pointer first.
        grab = size / 2;
        scrollToThumb(vertical, pointer - grab - start, track, size);
      }
      state.drag = { id: e.pointerId, vertical, grab, track, size, start };
      try {
        bar.setPointerCapture(e.pointerId);
      } catch (err) {
        // A pointer the browser is not tracking (a synthetic event) has
        // nothing to capture; the root's listeners still follow it.
      }
    });
    root.addEventListener("pointermove", (e) => {
      const d = state.drag;
      if (!d || e.pointerId !== d.id) return;
      scrollToThumb(d.vertical, (d.vertical ? e.clientY : e.clientX) - d.grab - d.start, d.track, d.size);
    });
    const release = (e) => {
      if (state.drag && e.pointerId === state.drag.id) state.drag = null;
    };
    root.addEventListener("pointerup", release);
    root.addEventListener("pointercancel", release);

    // The bar sits over the viewport, so a wheel on it scrolls the viewport.
    root.addEventListener("wheel", (e) => {
      const bar = e.target.closest(BAR);
      if (!bar || bar.parentElement !== root) return;
      e.preventDefault();
      viewport.scrollBy(e.deltaX, e.deltaY);
    }, { passive: false });

    measure();
  }

  function init() {
    document.querySelectorAll("[data-tui-scroll-area]").forEach(setup);
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  // Re-init on any childList mutation, directly (never rAF-deferred: rAF
  // does not fire in hidden tabs or throttled iframes): swapped-in markup
  // wires itself and a swapped content is measured again.
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });

  window.tui = window.tui || {};
  window.tui.scrollArea = {
    measure: (root) => root && root._tuiScrollArea && root._tuiScrollArea.measure(),
  };
})();
