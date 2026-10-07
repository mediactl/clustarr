/*
 * The Settings forms' behaviour (settings CRUD design, 2026-09-24):
 *
 * - [data-add-row] inside a [data-rows] or [data-map] clones the
 *   <template data-row-template>, numbering the clone's inputs with the
 *   next free index in place of "__i__", and appends it to
 *   [data-rows-list]; [data-remove-row] removes its [data-row]. Rows the
 *   object already has render on the server, so a form works without this.
 * - [data-show-when="path=v1|v2"] hides an element until the input named
 *   path has one of the values ("" for unset); the inputs it watches are
 *   re-read on every change.
 * - A form with [data-confirm] asks before it submits.
 *
 * Bound once by delegation; re-run on htmx swaps.
 */
(function () {
  function nextIndex(list) {
    var n = 0;
    list.querySelectorAll(':scope > [data-row]').forEach(function (row) {
      row.querySelectorAll('[name]').forEach(function (input) {
        var m = /(?:^|\.)(\d+)(?:\.|$)/.exec(input.getAttribute('name') || '');
        if (m) n = Math.max(n, parseInt(m[1], 10) + 1);
      });
    });
    return Math.max(n, list.querySelectorAll(':scope > [data-row]').length);
  }

  document.addEventListener('click', function (e) {
    var add = e.target.closest ? e.target.closest('[data-add-row]') : null;
    if (add) {
      var box = add.closest('[data-rows], [data-map]');
      var tpl = box && box.querySelector('template[data-row-template]');
      var list = box && box.querySelector('[data-rows-list]');
      if (!tpl || !list) return;
      var i = String(nextIndex(list));
      var frag = tpl.content.cloneNode(true);
      // aria-controls too: a select component finds its popup by it, so a
      // clone that kept the template's would open the template's list.
      frag.querySelectorAll('[name], [id], [for], [aria-controls]').forEach(function (el) {
        ['name', 'id', 'for', 'aria-controls'].forEach(function (attr) {
          var v = el.getAttribute(attr);
          if (v && v.indexOf('__i__') >= 0) el.setAttribute(attr, v.split('__i__').join(i));
        });
      });
      list.appendChild(frag);
      applyConditions();
      return;
    }
    var remove = e.target.closest ? e.target.closest('[data-remove-row]') : null;
    if (remove) {
      var row = remove.closest('[data-row]');
      if (row) row.remove();
    }
  });

  // The current value of the input(s) named name: a select's value, the
  // checked box's value or the hidden false, a text input's value.
  function valueOf(form, name) {
    var inputs = form.querySelectorAll('[name="' + name.replace(/"/g, '\\"') + '"]');
    var v = '';
    inputs.forEach(function (el) {
      if (el.type === 'checkbox') {
        if (el.checked) v = el.value;
      } else if (el.type === 'hidden' && v !== '' && el.value === 'false') {
        // a hidden false under a checked box does not win
      } else if (!el.disabled || el.type === 'hidden') {
        v = el.value;
      }
    });
    return v;
  }

  function applyConditions() {
    document.querySelectorAll('[data-show-when]').forEach(function (el) {
      var form = el.closest('form') || document;
      var rule = el.getAttribute('data-show-when') || '';
      var eq = rule.indexOf('=');
      if (eq < 0) return;
      var path = rule.slice(0, eq), values = rule.slice(eq + 1).split('|');
      var current = valueOf(form, path);
      var show = values.indexOf(current) >= 0;
      el.hidden = !show;
      // a hidden section must not block submission on its required inputs
      el.querySelectorAll('[required]').forEach(function (input) {
        if (show) input.removeAttribute('data-was-required');
        input.disabled = !show && input.type !== 'hidden' ? true : input.disabled && !show;
        if (show) input.disabled = false;
      });
    });
  }

  document.addEventListener('change', function (e) {
    if (e.target && e.target.name) applyConditions();
    // a select component posts through its visually hidden input; a
    // choice clears the mark requiredSelects set on its trigger
    var trigger = e.target && selectTriggerOf(e.target);
    if (trigger) trigger.removeAttribute('aria-invalid');
  });

  // The select component renders its hidden input after its children, so
  // after the trigger among its siblings (select.js' inputFor and
  // triggerOfInput walk the same way).
  var SELECT_INPUT = 'input[aria-hidden="true"][tabindex="-1"]';
  function selectInputOf(trigger) {
    var el = trigger.nextElementSibling;
    while (el && !(el.matches && el.matches(SELECT_INPUT))) el = el.nextElementSibling;
    return el;
  }
  function selectTriggerOf(input) {
    if (!input.matches || !input.matches(SELECT_INPUT)) return null;
    var el = input.previousElementSibling;
    while (el && !(el.matches && el.matches('[data-slot="select-trigger"]'))) el = el.previousElementSibling;
    return el;
  }

  // requiredSelects marks the select component's required check on its
  // trigger: an empty required choice in a shown section marks its trigger
  // invalid and takes focus, and the form does not submit.
  function requiredSelects(form) {
    var first = null;
    // templ renders the flag bare (aria-required, no value)
    form.querySelectorAll('[data-slot="select-trigger"][aria-required]:not([aria-required="false"])').forEach(function (trigger) {
      var input = selectInputOf(trigger);
      if (!input) return;
      if (trigger.closest('[hidden]')) return;
      if (input.value === '') {
        trigger.setAttribute('aria-invalid', 'true');
        if (!first) first = trigger;
      }
    });
    if (first) first.focus();
    return !first;
  }
  document.addEventListener('input', function (e) {
    if (e.target && e.target.name && e.target.tagName === 'SELECT') applyConditions();
  });

  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (form && form.hasAttribute && form.hasAttribute('data-settings-form') && !requiredSelects(form)) {
      e.preventDefault();
      return;
    }
    if (form && form.hasAttribute && form.hasAttribute('data-confirm')) {
      if (!window.confirm(form.getAttribute('data-confirm'))) e.preventDefault();
    }
  });

  document.addEventListener('htmx:afterSettle', applyConditions);
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', applyConditions);
  } else {
    applyConditions();
  }
})();
