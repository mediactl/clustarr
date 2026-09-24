/*
 * The A-Z bar's scroll tracker (design 2026-09-24, after Radarr's): as the
 * grid scrolls, the letter of the first card still in view beneath the bar's
 * top is marked data-current, and app.css draws Radarr's small line beside
 * it. Cards carry data-letter; the bar's buttons carry data-jump. It binds to
 * the window and the document once and re-reads the DOM on every run, so
 * htmx swaps (a wider window, a filter, another tab) need nothing more.
 */
(function () {
  function update() {
    var bar = document.querySelector('[data-jump-bar]');
    if (!bar) return;
    var top = bar.getBoundingClientRect().top;
    var cards = document.querySelectorAll('[data-letter]');
    var current = null;
    for (var i = 0; i < cards.length; i++) {
      if (cards[i].getBoundingClientRect().bottom > top) {
        current = cards[i].getAttribute('data-letter');
        break;
      }
    }
    var buttons = bar.querySelectorAll('[data-jump]');
    for (var j = 0; j < buttons.length; j++) {
      if (buttons[j].getAttribute('data-jump') === current) {
        buttons[j].setAttribute('data-current', '');
      } else {
        buttons[j].removeAttribute('data-current');
      }
    }
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
