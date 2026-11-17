/*
 * The Settings forms' behaviour (settings CRUD design, 2026-09-24):
 *
 * - [data-add-row] inside a [data-rows] or [data-map] clones the
 *   <template data-row-template>, numbering the clone's inputs with the
 *   next free index in place of "__i__", and appends it to
 *   [data-rows-list]; [data-remove-row] removes its [data-row]. Rows the
 *   object already has render on the server, so a form works without this.
 * - [data-show-when="path=v1|v2"] hides an element until the input named
 *   path has one of the values ("" for unset); "path!=v1|v2" until it has
 *   none of them; clauses joined by "&" must all hold. [data-option-when]
 *   is the same for one option of a select or of a checkbox group. The
 *   inputs they watch are re-read on every change (2026-11-16: per field
 *   and per option, where only whole sections had conditions).
 * - The sections are collapsibles: a closed one holding an input a
 *   submit is refused for is opened first, and the Settings page remembers
 *   which of its sections were left open.
 * - [data-dynamic] is a section the server fills again when the field it
 *   names changes (htmx); input[data-fill] in it fills another input.
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

  // The current values of the field named name: a select's value, every
  // checked box of a checkbox group, each line of a list, a text input's
  // value; a lone checkbox reads true or its hidden false. A field a
  // condition hides reads as unset, so what depends on it follows.
  function valuesOf(form, name) {
    var inputs = form.querySelectorAll('[name="' + name.replace(/"/g, '\\"') + '"]');
    var out = [], hiddenFalse = false;
    inputs.forEach(function (el) {
      if (el.closest('[data-cond-hidden]')) return;
      if (el.type === 'checkbox') {
        if (el.checked) out.push(el.value);
      } else if (el.tagName === 'TEXTAREA') {
        el.value.split('\n').forEach(function (line) {
          line = line.trim();
          if (line) out.push(line);
        });
      } else if (el.type === 'hidden' && el.value === 'false') {
        hiddenFalse = true; // the hidden false under a checkbox
      } else if (!el.disabled || el.type === 'hidden') {
        out.push(el.value);
      }
    });
    if (!out.length) out.push(hiddenFalse ? 'false' : '');
    return out;
  }

  // holds evaluates a rule: clauses joined by "&", each "path=v1|v2" (the
  // field has one of the values) or "path!=v1|v2" (it has none of them).
  function holds(form, rule) {
    return rule.split('&').every(function (clause) {
      var m = /^(.*?)(!=|=)(.*)$/.exec(clause);
      if (!m) return true;
      var values = m[3].split('|');
      var hit = valuesOf(form, m[1]).some(function (v) { return values.indexOf(v) >= 0; });
      return m[2] === '=' ? hit : !hit;
    });
  }

  // An opened select's popup is portaled out of its form (to <body>, or
  // into a dialog's portal), so an option finds its form through its
  // trigger.
  function formOf(el) {
    var form = el.closest('form');
    if (!form && el.matches && el.matches('[data-slot="select-item"]')) {
      var trigger = triggerOfOption(el);
      form = trigger && trigger.closest('form');
    }
    return form || document.querySelector('form[data-settings-form]') || document;
  }

  // The select component's trigger for one of its options: the trigger
  // names its popup by id (data-templ-controls), as select.js links them.
  function triggerOfOption(option) {
    var popup = option.closest('[data-slot="select-content"]');
    if (!popup || !popup.id) return null;
    return document.querySelector('[data-slot="select-trigger"][data-templ-controls="' + popup.id + '"]');
  }

  // applyConditions shows what its condition holds for and hides the rest:
  // sections and fields ([data-show-when]) and single options of a choice
  // ([data-option-when]). A hidden field reads as unset, so the pass repeats
  // until nothing moves. A choice left on an option that is no longer
  // offered is cleared, except as the page loads: what an object already
  // holds is shown as it is.
  function applyConditions(initial) {
    initial = initial === true; // an event, or nothing, is a change
    for (var pass = 0, moved = true; moved && pass < 6; pass++) {
      moved = false;
      document.querySelectorAll('[data-show-when], [data-option-when]').forEach(function (el) {
        var rule = el.getAttribute('data-show-when') || el.getAttribute('data-option-when') || '';
        var show = holds(formOf(el), rule);
        if (el.hasAttribute('data-cond-hidden') === !show && el.hidden === !show) return;
        moved = true;
        el.hidden = !show;
        el.toggleAttribute('data-cond-hidden', !show);
      });
    }
    // an option of a select: out of reach while hidden
    document.querySelectorAll('[data-slot="select-item"][data-option-when]').forEach(function (item) {
      var off = item.hasAttribute('data-cond-hidden');
      item.toggleAttribute('data-disabled', off);
      if (off) item.setAttribute('aria-disabled', 'true'); else item.removeAttribute('aria-disabled');
      if (off && !initial && item.hasAttribute('data-selected')) {
        var trigger = triggerOfOption(item);
        if (trigger && trigger.getAttribute('aria-readonly') !== 'true' && window.templ && window.templ.select) {
          window.templ.select.setValue(trigger, '');
        }
      }
    });
    // an option of a checkbox group posts nothing while hidden; and a
    // hidden section must not block submission on its required inputs
    document.querySelectorAll('[data-cond-off]').forEach(function (input) {
      if (input.closest('[data-cond-hidden]')) return;
      input.disabled = false;
      input.removeAttribute('data-cond-off');
    });
    document.querySelectorAll('[data-cond-hidden]').forEach(function (el) {
      var sel = el.hasAttribute('data-option-when') ? 'input[type="checkbox"]' : '[required]';
      el.querySelectorAll(sel).forEach(function (input) {
        if (input.disabled || input.type === 'hidden') return;
        input.disabled = true;
        input.setAttribute('data-cond-off', '');
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
      if (trigger.closest('[data-cond-hidden]')) return;
      if (input.value === '') {
        trigger.setAttribute('aria-invalid', 'true');
        if (!first) first = trigger;
      }
    });
    if (first) {
      openSectionOf(first);
      first.focus();
    }
    return !first;
  }

  // The form's sections and the Settings page's are collapsibles. A
  // closed one hides its inputs, and a browser cannot point at a required
  // input it cannot focus: the submit would simply do nothing. So before a
  // submit is checked, every closed section holding an input the browser
  // will refuse is opened.
  var TRIGGER = '[data-slot="collapsible-trigger"]';
  function isOpen(root) {
    var trigger = root && root.querySelector(TRIGGER);
    return !!trigger && trigger.getAttribute('aria-expanded') === 'true';
  }
  function openItem(root) {
    var trigger = root && root.querySelector(TRIGGER);
    if (trigger && trigger.getAttribute('aria-expanded') !== 'true') trigger.click();
  }
  function openSectionOf(el) {
    openItem(el.closest('[data-slot="collapsible"]'));
  }
  document.addEventListener('click', function (e) {
    var submit = e.target.closest ? e.target.closest('[type="submit"]') : null;
    // A dialog's Save sits in its footer, outside the form it names.
    var form = submit && submit.form;
    if (!form || !form.hasAttribute('data-settings-form')) return;
    form.querySelectorAll(':invalid').forEach(function (input) {
      if (input.tagName !== 'FIELDSET') openSectionOf(input);
    });
  }, true);

  // The Settings page remembers which of its sections the reader left open
  // (theirs alone, in localStorage), and opens the one the address names.
  var OPEN_KEY = 'clustarr.settings.open';
  function settingsRoot() {
    return document.querySelector('[data-settings-sections]');
  }
  function rememberedSections() {
    try {
      var v = JSON.parse(localStorage.getItem(OPEN_KEY) || '[]');
      return Array.isArray(v) ? v : [];
    } catch (err) {
      return [];
    }
  }
  function openNamedSection() {
    var root = settingsRoot();
    var named = (location.hash || '').slice(1);
    var target = root && named && document.getElementById(named);
    if (!target || !target.hasAttribute('data-settings-kind')) return;
    openItem(target);
    target.scrollIntoView({ block: 'start' });
  }
  function restoreSections() {
    var root = settingsRoot();
    if (!root) return;
    var open = rememberedSections();
    root.querySelectorAll('[data-settings-kind]').forEach(function (section) {
      if (open.indexOf(section.getAttribute('data-settings-kind')) >= 0) openItem(section);
    });
    openNamedSection();
  }
  // A dialog's address is the page's hash too (#modal=...): opening or
  // closing one must not scroll to a section.
  window.addEventListener('hashchange', function (e) {
    if ((e.oldURL || '').indexOf('#modal=') >= 0 || (e.newURL || '').indexOf('#modal=') >= 0) return;
    openNamedSection();
  });
  document.addEventListener('click', function (e) {
    var root = settingsRoot();
    if (!root || !e.target.closest || !e.target.closest(TRIGGER)) return;
    if (!(window.templ && window.templ.collapsible)) return; // nothing can have moved yet
    // after the collapsible's own handler has moved the state
    setTimeout(function () {
      var open = [];
      root.querySelectorAll('[data-settings-kind]').forEach(function (section) {
        if (isOpen(section)) open.push(section.getAttribute('data-settings-kind'));
      });
      try { localStorage.setItem(OPEN_KEY, JSON.stringify(open)); } catch (err) { /* private mode */ }
    }, 0);
  });

  // A section the server fills again ([data-dynamic], the Indexer's
  // Definition) may carry values to fill elsewhere in the form
  // (input[data-fill=<name>]): the definition's address into Base URL, when
  // that is empty or still holds what an earlier fill put there.
  function applyFills(initial) {
    document.querySelectorAll('input[data-fill]').forEach(function (fill) {
      var form = formOf(fill);
      var target = form.querySelector('[name="' + fill.getAttribute('data-fill') + '"]');
      if (!target || target.readOnly) return;
      if (initial) {
        if (target.value === fill.value) target.setAttribute('data-filled', fill.value);
        return;
      }
      if (target.value === '' || target.value === target.getAttribute('data-filled')) {
        target.value = fill.value;
        target.setAttribute('data-filled', fill.value);
      }
    });
  }
  document.addEventListener('htmx:afterSwap', function (e) {
    if (e.target && e.target.hasAttribute && e.target.hasAttribute('data-dynamic')) applyFills(false);
  });
  document.addEventListener('input', function (e) {
    if (e.target && e.target.name && e.target.tagName === 'SELECT') applyConditions();
  });

  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (form && form.hasAttribute && form.hasAttribute('data-settings-form') && !requiredSelects(form)) {
      e.preventDefault();
      return;
    }
  });

  // A swap is not a change: a form that lands in the modal region, or is
  // re-rendered after a rejected save, is shown as its object holds it (a
  // stored choice a condition no longer offers is not cleared). A section
  // the server fills again is a change.
  document.addEventListener('htmx:afterSettle', function (e) {
    var t = e.target;
    var whole = !!(t && t.closest && (t.closest('#modal') || (t.matches && t.matches('[data-settings-form]'))) && !(t.hasAttribute && t.hasAttribute('data-dynamic')));
    applyConditions(whole);
    if (whole) applyFills(true);
  });
  // This script is deferred and runs ahead of the components' own: a
  // section is reopened by its trigger, which only works once theirs has
  // run, so the restore waits for the document to be done.
  function start() {
    applyConditions(true);
    applyFills(true);
    if (window.templ && window.templ.collapsible) {
      restoreSections();
    } else {
      document.addEventListener('DOMContentLoaded', restoreSections);
    }
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
