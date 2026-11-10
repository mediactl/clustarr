/*
 * The A-Z bar's position thumb, its drag and its letter clicks (design
 * 2026-09-24, after Radarr's; one page since 2026-11-10, when the library
 * stopped paginating).
 *
 * The thumb is a thin mark along the strip whose place and length follow
 * the range of cards on screen over the whole grid -- not snapped to a
 * letter -- counting cards rather than pixels. The document's native
 * scrollbar hides while the bar is on the page.
 *
 * The thumb drags like a native scrollbar's: it follows the pointer from
 * where it was grabbed, and the grid follows the thumb -- the thumb's top
 * over the strip's height, times the cards, is the card to put under the
 * toolbar. The thumb is left where the pointer put it while it is held,
 * and returns to its measured place when let go.
 *
 * A letter scrolls to its first title's card, which its href names
 * (#<card id>; data-index is the card's place when the stream has since
 * replaced it). The href is the fallback without JavaScript.
 *
 * It binds to the window and the document once and re-reads the DOM on
 * every run, so htmx swaps (a filter, another tab) need nothing more.
 */
(function () {
  function rows() {
    return document.getElementById('library-rows');
  }
  function cards() {
    var r = rows();
    return r ? r.querySelectorAll('[data-library-grid] > [data-ref]') : [];
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
    if (!thumb || drag) return;
    var all = cards();
    var total = all.length;
    if (!total) {
      thumb.style.display = 'none';
      return;
    }
    var box = b.getBoundingClientRect();
    var viewBottom = window.innerHeight;
    // The cards are in rows of equal height: find the first on screen by
    // bisection rather than measuring a thousand of them.
    var lo = 0, hi = total - 1;
    while (lo < hi) {
      var mid = (lo + hi) >> 1;
      if (all[mid].getBoundingClientRect().bottom > box.top) hi = mid;
      else lo = mid + 1;
    }
    var first = lo, last = first;
    while (last + 1 < total && all[last + 1].getBoundingClientRect().top < viewBottom) last++;
    var height = box.height;
    var top = height * first / total;
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

  // Scroll so el sits just under the top bar, the breadcrumb row and the
  // toolbar (where the A-Z bar begins).
  function scrollToCard(el) {
    if (!el) return false;
    var b = bar();
    var under = b ? b.getBoundingClientRect().top : 0;
    window.scrollTo(0, el.getBoundingClientRect().top + window.scrollY - under - 4);
    return true;
  }

  // A letter click.
  document.addEventListener('click', function (e) {
    var a = e.target && e.target.closest ? e.target.closest('[data-jump-bar] [data-jump][data-index]') : null;
    if (!a || !rows()) return;
    e.preventDefault();
    var id = (a.getAttribute('href') || '').replace(/^#/, '');
    if (scrollToCard(id && document.getElementById(id))) return;
    scrollToCard(cards()[parseInt(a.getAttribute('data-index') || '0', 10)]);
  });

  // The drag.
  function dragTo(y) {
    var b = bar();
    var thumb = b && b.querySelector('[data-jump-thumb]');
    if (!b || !thumb) return;
    var box = b.getBoundingClientRect();
    var len = thumb.getBoundingClientRect().height;
    var top = Math.min(Math.max(0, y - box.top - drag.grab), Math.max(0, box.height - len));
    thumb.style.top = top + 'px';
    var all = cards();
    if (!all.length || !box.height) return;
    scrollToCard(all[Math.min(all.length - 1, Math.max(0, Math.round(top / box.height * all.length)))]);
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
    schedule();
  }
  document.addEventListener('pointerup', release);
  document.addEventListener('pointercancel', release);

  window.addEventListener('scroll', schedule, { passive: true });
  window.addEventListener('resize', schedule);
  document.addEventListener('htmx:afterSettle', schedule);
  document.addEventListener('htmx:sseMessage', schedule);
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', update);
  } else {
    update();
  }
})();
