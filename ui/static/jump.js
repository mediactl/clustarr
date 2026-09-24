/*
 * The A-Z bar's position thumb (design 2026-09-24, after Radarr's): a thin
 * mark along the strip whose place and length follow the range of items on
 * screen over the whole list -- not snapped to a letter, and stable across
 * the pages infinite scroll loads, because it counts items (#library-rows
 * carries the window's offset and the list's total) rather than pixels.
 * The document's native scrollbar hides while the bar is on the page. It
 * binds to the window and the document once and re-reads the DOM on every
 * run, so htmx swaps (a wider window, a filter, another tab) need nothing
 * more.
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
  window.addEventListener('scroll', schedule, { passive: true });
  window.addEventListener('resize', schedule);
  document.addEventListener('htmx:afterSettle', schedule);
  document.addEventListener('htmx:afterSwap', schedule);
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', update);
  } else {
    update();
  }
})();
