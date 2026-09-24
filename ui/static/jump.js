/*
 * The A-Z bar's position thumb and the scroll's upward direction (design
 * 2026-09-24, after Radarr's).
 *
 * The thumb is a thin mark along the strip whose place and length follow
 * the range of items on screen over the whole list -- not snapped to a
 * letter, and stable across the pages infinite scroll loads, because it
 * counts items (#library-rows carries the window's offset and the list's
 * total) rather than pixels. The document's native scrollbar hides while
 * the bar is on the page.
 *
 * After a jump the window starts partway down the list. The page's
 * [data-load-prev] sentinel fetches the window one page earlier on its
 * loadprev event, which fires here when the reader scrolls up at the top
 * of the page (a wheel up at the very top, or an upward scroll that reaches
 * it); the first card already on screen is held in place across the
 * prepend, so nothing jumps.
 *
 * It binds to the window and the document once and re-reads the DOM on
 * every run, so htmx swaps (a wider window, a filter, another tab) need
 * nothing more.
 */
(function () {
  function update() {
    var bar = document.querySelector('[data-jump-bar]');
    if (bar) {
      document.documentElement.setAttribute('data-scrollbar', 'hidden');
    } else {
      document.documentElement.removeAttribute('data-scrollbar');
      return;
    }
    var thumb = bar.querySelector('[data-jump-thumb]');
    var rows = document.getElementById('library-rows');
    if (!thumb || !rows) return;
    var total = parseInt(rows.getAttribute('data-total') || '0', 10);
    var offset = parseInt(rows.getAttribute('data-offset') || '0', 10);
    var cards = rows.querySelectorAll('[data-ref]');
    if (!total || !cards.length) {
      thumb.style.display = 'none';
      return;
    }
    var box = bar.getBoundingClientRect();
    var viewBottom = window.innerHeight;
    var first = -1, last = -1;
    for (var i = 0; i < cards.length; i++) {
      var r = cards[i].getBoundingClientRect();
      if (first < 0 && r.bottom > box.top) first = i;
      if (r.top < viewBottom) last = i;
    }
    if (first < 0) first = cards.length - 1;
    if (last < first) last = first;
    var height = box.height;
    var top = height * (offset + first) / total;
    var len = Math.max(16, height * (last - first + 1) / total);
    if (top + len > height) top = Math.max(0, height - len);
    thumb.style.display = '';
    thumb.style.top = top + 'px';
    thumb.style.height = len + 'px';
  }

  var queued = false;
  function schedule() {
    if (queued) return;
    queued = true;
    window.requestAnimationFrame(function () {
      queued = false;
      update();
    });
  }

  // The upward direction: fire the earlier-page sentinel once per window
  // when the reader is at the top and heading up. data-loading holds it
  // until the swap replaces the sentinel.
  var nearTop = 120;
  function loadEarlier() {
    var el = document.querySelector('[data-load-prev]');
    if (!el || el.hasAttribute('data-loading') || !window.htmx) return;
    el.setAttribute('data-loading', '');
    window.htmx.trigger(el, 'loadprev');
  }
  var lastY = window.scrollY;
  window.addEventListener('scroll', function () {
    var y = window.scrollY;
    if (y < lastY && y <= nearTop) loadEarlier();
    lastY = y;
    schedule();
  }, { passive: true });
  window.addEventListener('wheel', function (e) {
    if (e.deltaY < 0 && window.scrollY <= nearTop) loadEarlier();
  }, { passive: true });

  // Hold the reader's place across a prepend: remember where the window's
  // first card sits when the sentinel requests, and put it back there once
  // the new rows are in (a mutation observer, since the swap replaces the
  // element the request came from).
  var anchor = null;
  document.addEventListener('htmx:beforeRequest', function (e) {
    var src = e.detail && e.detail.elt;
    if (!src || !src.hasAttribute || !src.hasAttribute('data-load-prev')) return;
    var rows = document.getElementById('library-rows');
    var first = rows && rows.querySelector('[data-ref]');
    if (!first) return;
    anchor = { ref: first.getAttribute('data-ref'), top: first.getBoundingClientRect().top };
  });
  new MutationObserver(function () {
    if (anchor) {
      var rows = document.getElementById('library-rows');
      var el = rows && rows.querySelector('[data-ref="' + anchor.ref.replace(/"/g, '\\"') + '"]');
      if (el) {
        var delta = el.getBoundingClientRect().top - anchor.top;
        anchor = null;
        if (delta) window.scrollBy(0, delta);
        lastY = window.scrollY;
      }
    }
    schedule();
  }).observe(document.body, { childList: true, subtree: true });

  window.addEventListener('resize', schedule);
  document.addEventListener('htmx:afterSettle', schedule);
  document.addEventListener('htmx:afterSwap', schedule);
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', update);
  } else {
    update();
  }
})();
