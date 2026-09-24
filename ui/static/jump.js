/*
 * The A-Z bar's position thumb, its drag, its letter clicks, and the
 * scroll's upward direction (design 2026-09-24, after Radarr's).
 *
 * The thumb is a thin mark along the strip whose place and length follow
 * the range of items on screen over the whole list -- not snapped to a
 * letter, and stable across the pages infinite scroll loads, because it
 * counts items (#library-rows carries the window's offset, total, page,
 * pages and per) rather than pixels. The document's native scrollbar hides
 * while the bar is on the page.
 *
 * The thumb drags like a native scrollbar's: it follows the pointer from
 * where it was grabbed, and the grid follows the thumb -- the thumb's top
 * over the strip's height, times the total, is the item to put under the
 * toolbar. An item outside the loaded window is reached the way a letter
 * is (below), after a short pause so a sweep across the strip asks for
 * one window rather than one per pixel; letting go asks at once. The
 * thumb is left where the pointer put it while it is held, and returns to
 * its measured place when let go.
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
  function bar() {
    return document.querySelector('[data-jump-bar]');
  }

  // drag is the thumb's grab while it is held: the pointer that holds it
  // and how far below the thumb's top it took hold.
  var drag = null;

  function update() {
    var b = bar();
    if (b) {
      document.documentElement.setAttribute('data-scrollbar', 'hidden');
    } else {
      document.documentElement.removeAttribute('data-scrollbar');
      return;
    }
    var thumb = b.querySelector('[data-jump-thumb]');
    var r = rows();
    if (!thumb || !r || drag) return;
    var total = num(r, 'data-total');
    var offset = num(r, 'data-offset');
    var cards = r.querySelectorAll('[data-ref]');
    if (!total || !cards.length) {
      thumb.style.display = 'none';
      return;
    }
    var box = b.getBoundingClientRect();
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
    var b = bar();
    var under = b ? b.getBoundingClientRect().top : 0;
    window.scrollTo(0, el.getBoundingClientRect().top + window.scrollY - under - 4);
    lastY = window.scrollY;
    return true;
  }

  // Reach the card at index on page target: scroll if the page is loaded,
  // else widen the window to it and scroll once the rows are in
  // (pendingIndex).
  var pendingIndex = null;
  function reach(target, index) {
    var r = rows();
    if (!r || !window.htmx) return;
    var start = num(r, 'data-page'), span = num(r, 'data-pages');
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
  }

  // A letter click.
  document.addEventListener('click', function (e) {
    var a = e.target && e.target.closest ? e.target.closest('[data-jump-bar] [data-jump][data-index]') : null;
    if (!a || !rows() || !window.htmx) return;
    e.preventDefault();
    reach(num(a, 'data-page'), num(a, 'data-index'));
  });

  // The drag. wanted is the item the thumb last pointed at outside the
  // loaded window, asked for after a pause (wantTimer) or on release.
  var wanted = null, wantTimer = null;
  function pageOf(r, index) {
    return Math.floor(index / Math.max(1, num(r, 'data-per'))) + 1;
  }
  function askWanted() {
    wantTimer = null;
    if (wanted === null) return;
    var r = rows(), index = wanted;
    wanted = null;
    if (r) reach(pageOf(r, index), index);
  }
  function dragTo(y) {
    var b = bar();
    var thumb = b && b.querySelector('[data-jump-thumb]');
    var r = rows();
    if (!b || !thumb || !r) return;
    var box = b.getBoundingClientRect();
    var len = thumb.getBoundingClientRect().height;
    var top = Math.min(Math.max(0, y - box.top - drag.grab), Math.max(0, box.height - len));
    thumb.style.top = top + 'px';
    var total = num(r, 'data-total');
    if (!total || !box.height) return;
    var index = Math.min(total - 1, Math.max(0, Math.round(top / box.height * total)));
    clearTimeout(wantTimer);
    wantTimer = null;
    if (scrollToIndex(index)) {
      wanted = null;
      return;
    }
    wanted = index;
    wantTimer = setTimeout(askWanted, 150);
  }
  document.addEventListener('pointerdown', function (e) {
    var thumb = e.target && e.target.closest ? e.target.closest('[data-jump-bar] [data-jump-thumb]') : null;
    if (!thumb || (e.button && e.button !== 0)) return;
    e.preventDefault();
    drag = { id: e.pointerId, grab: e.clientY - thumb.getBoundingClientRect().top };
    thumb.setAttribute('data-dragging', '');
    try {
      thumb.setPointerCapture(e.pointerId);
    } catch (err) {
      // A pointer the browser is not tracking (a synthetic event) has
      // nothing to capture; the document listeners still follow it.
    }
  });
  document.addEventListener('pointermove', function (e) {
    if (drag && e.pointerId === drag.id) dragTo(e.clientY);
  });
  function release(e) {
    if (!drag || e.pointerId !== drag.id) return;
    drag = null;
    var thumb = document.querySelector('[data-jump-thumb]');
    if (thumb) thumb.removeAttribute('data-dragging');
    if (wantTimer) {
      clearTimeout(wantTimer);
      askWanted();
    }
    schedule();
  }
  document.addEventListener('pointerup', release);
  document.addEventListener('pointercancel', release);

  // The upward direction: fire the earlier-page sentinel once per window
  // when the reader is at the top and heading up. data-loading holds it
  // until the swap replaces the sentinel. Not during a drag, which
  // reaches earlier pages itself.
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
    if (!drag && y < lastY && y <= nearTop) loadEarlier();
    lastY = y;
    schedule();
  }, { passive: true });
  window.addEventListener('wheel', function (e) {
    if (!drag && e.deltaY < 0 && window.scrollY <= nearTop) loadEarlier();
  }, { passive: true });

  // Hold the reader's place across a prepend: remember where the window's
  // first card sits when the sentinel requests, and put it back there once
  // the new rows are in (a mutation observer, since the swap replaces the
  // element the request came from). A pending letter jump or drag scrolls
  // to its card the same way.
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
