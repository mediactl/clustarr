/*
 * The A-Z bar's position thumb, its letter clicks, and the scroll's upward
 * direction (design 2026-09-24, after Radarr's).
 *
 * The thumb is a thin mark along the strip whose place and length follow
 * the range of items on screen over the whole list -- not snapped to a
 * letter, and stable across the pages infinite scroll loads, because it
 * counts items (#library-rows carries the window's offset, total, page,
 * pages and per) rather than pixels. The document's native scrollbar hides
 * while the bar is on the page.
 *
 * A letter click keeps what is loaded: the letter carries the page it
 * begins on and the index of its first title; when that page is inside
 * the loaded window the grid just scrolls there, otherwise the window is
 * widened to reach it (start moved back or span grown, through htmx, the
 * rows swapped whole) and the grid scrolls there once the rows are in.
 * The letter's href is the fallback without JavaScript.
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
  function rows() {
    return document.getElementById('library-rows');
  }
  function num(el, name) {
    return parseInt(el.getAttribute(name) || '0', 10);
  }

  function update() {
    var bar = document.querySelector('[data-jump-bar]');
    if (bar) {
      document.documentElement.setAttribute('data-scrollbar', 'hidden');
    } else {
      document.documentElement.removeAttribute('data-scrollbar');
      return;
    }
    var thumb = bar.querySelector('[data-jump-thumb]');
    var r = rows();
    if (!thumb || !r) return;
    var total = num(r, 'data-total');
    var offset = num(r, 'data-offset');
    var cards = r.querySelectorAll('[data-ref]');
    if (!total || !cards.length) {
      thumb.style.display = 'none';
      return;
    }
    var box = bar.getBoundingClientRect();
    var viewBottom = window.innerHeight;
    var first = -1, last = -1;
    for (var i = 0; i < cards.length; i++) {
      var rect = cards[i].getBoundingClientRect();
      if (first < 0 && rect.bottom > box.top) first = i;
      if (rect.top < viewBottom) last = i;
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

  // Scroll so the card at absolute index sits just under the top bar,
  // the breadcrumb row and the toolbar (where the A-Z bar begins).
  function scrollToIndex(index) {
    var r = rows();
    if (!r) return false;
    var el = r.querySelectorAll('[data-ref]')[index - num(r, 'data-offset')];
    if (!el) return false;
    var bar = document.querySelector('[data-jump-bar]');
    var under = bar ? bar.getBoundingClientRect().top : 0;
    window.scrollTo(0, el.getBoundingClientRect().top + window.scrollY - under - 4);
    lastY = window.scrollY;
    return true;
  }

  // A letter click: scroll if the letter's page is loaded, else widen the
  // window to it and scroll once the rows are in (pendingIndex).
  var pendingIndex = null;
  document.addEventListener('click', function (e) {
    var a = e.target && e.target.closest ? e.target.closest('[data-jump-bar] [data-jump][data-index]') : null;
    var r = rows();
    if (!a || !r || !window.htmx) return;
    e.preventDefault();
    var start = num(r, 'data-page'), span = num(r, 'data-pages');
    var target = num(a, 'data-page'), index = num(a, 'data-index');
    var newStart = Math.min(start, target);
    var newEnd = Math.max(start + span - 1, target);
    if (newStart === start && newEnd === start + span - 1) {
      scrollToIndex(index);
      return;
    }
    var stream = r.getAttribute('sse-connect') || '';
    var url = new URL(stream.replace('/events/library/', '/library/'), window.location.origin);
    url.searchParams.set('page', String(newStart));
    url.searchParams.set('pages', String(newEnd - newStart + 1));
    pendingIndex = index;
    window.htmx.ajax('GET', url.pathname + url.search, { target: '#library-rows', select: '#library-rows', swap: 'outerHTML' });
  });

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
  // element the request came from). A pending letter jump scrolls to its
  // title the same way.
  var anchor = null;
  document.addEventListener('htmx:beforeRequest', function (e) {
    var src = e.detail && e.detail.elt;
    if (!src || !src.hasAttribute || !src.hasAttribute('data-load-prev')) return;
    var r = rows();
    var first = r && r.querySelector('[data-ref]');
    if (!first) return;
    anchor = { ref: first.getAttribute('data-ref'), top: first.getBoundingClientRect().top };
  });
  new MutationObserver(function () {
    if (anchor) {
      var r = rows();
      var el = r && r.querySelector('[data-ref="' + anchor.ref.replace(/"/g, '\\"') + '"]');
      if (el) {
        var delta = el.getBoundingClientRect().top - anchor.top;
        anchor = null;
        if (delta) window.scrollBy(0, delta);
        lastY = window.scrollY;
      }
    }
    if (pendingIndex !== null && scrollToIndex(pendingIndex)) {
      pendingIndex = null;
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
