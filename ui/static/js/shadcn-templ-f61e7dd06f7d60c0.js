// components/baseui/scroll_lock.js
// Port of @base-ui/utils/useScrollLock.ts. Helpers from useTimeout.ts,
// useAnimationFrame.ts, platform/os.ts, platform/engine.ts, @floating-ui/utils/dom,
// and @base-ui/react/utils/useAnchoredPopupScrollLock.ts live here as well.
(function () {
  "use strict";

  // @base-ui/utils/platform/{os,engine}.ts
  const lowerPlatform = navigator.platform.toLowerCase();
  const ios = /^i(os$|p)/.test(lowerPlatform) ||
    (lowerPlatform === "macintel" && navigator.maxTouchPoints > 1);
  const webkit = typeof CSS !== "undefined" && !!CSS.supports?.("-webkit-backdrop-filter:none");

  const ownerDocument = (referenceElement) => referenceElement?.ownerDocument || document;
  const ownerWindow = (referenceElement) =>
    (referenceElement?.nodeType === 9 ? referenceElement : ownerDocument(referenceElement)).defaultView || window;

  // @floating-ui/utils/dom: isOverflowElement
  function isOverflowElement(element) {
    const { overflow, overflowX, overflowY, display } = ownerWindow(element).getComputedStyle(element);
    return /auto|scroll|overlay|hidden|clip/.test(overflow + overflowY + overflowX) &&
      display !== "inline" && display !== "contents";
  }

  // @base-ui/utils/useTimeout.ts (the imperative helper; no React lifecycle).
  class Timeout {
    static create() { return new Timeout(); }
    currentId = 0;
    start(delay, fn) {
      this.clear();
      this.currentId = setTimeout(() => {
        this.currentId = 0;
        fn();
      }, delay);
    }
    isStarted() { return this.currentId !== 0; }
    clear = () => {
      if (this.currentId !== 0) {
        clearTimeout(this.currentId);
        this.currentId = 0;
      }
    };
  }

  // @base-ui/utils/useAnimationFrame.ts, including its production scheduler.
  class Scheduler {
    callbacks = [];
    callbacksCount = 0;
    nextId = 1;
    startId = 1;
    isScheduled = false;
    tick = (timestamp) => {
      this.isScheduled = false;
      const currentCallbacks = this.callbacks;
      const currentCallbacksCount = this.callbacksCount;
      this.callbacks = [];
      this.callbacksCount = 0;
      this.startId = this.nextId;
      if (currentCallbacksCount > 0) {
        for (let i = 0; i < currentCallbacks.length; i += 1) {
          currentCallbacks[i]?.(timestamp);
        }
      }
    };
    request(fn) {
      const id = this.nextId;
      this.nextId += 1;
      this.callbacks.push(fn);
      this.callbacksCount += 1;
      if (!this.isScheduled) {
        requestAnimationFrame(this.tick);
        this.isScheduled = true;
      }
      return id;
    }
    cancel(id) {
      const index = id - this.startId;
      if (index < 0 || index >= this.callbacks.length || this.callbacks[index] === null) return;
      this.callbacks[index] = null;
      this.callbacksCount -= 1;
    }
  }
  const scheduler = new Scheduler();
  class AnimationFrame {
    static create() { return new AnimationFrame(); }
    static request(fn) { return scheduler.request(fn); }
    static cancel(id) { scheduler.cancel(id); }
    currentId = null;
    request(fn) {
      this.cancel();
      this.currentId = scheduler.request(() => {
        this.currentId = null;
        fn();
      });
    }
    cancel = () => {
      if (this.currentId !== null) {
        scheduler.cancel(this.currentId);
        this.currentId = null;
      }
    };
  }

  let originalHtmlStyles = {};
  let originalBodyStyles = {};
  let originalHtmlScrollBehavior = '';

  // The viewport's overflow comes from <html> when it establishes its own scroll container, and
  // propagates from <body> otherwise. An `overflow` style on the other element doesn't lock the page.
  function getViewportScroller(html, body) {
    return isOverflowElement(html) ? html : body;
  }

  function isPageScrollLocked(win, html, body) {
    return /hidden|clip/.test(win.getComputedStyle(getViewportScroller(html, body)).overflowY);
  }

  function hasInsetScrollbars(referenceElement) {
    if (typeof document === 'undefined') {
      return false;
    }
    const doc = ownerDocument(referenceElement);
    const win = ownerWindow(doc);
    return win.innerWidth - doc.documentElement.clientWidth > 0;
  }

  function supportsStableScrollbarGutter(referenceElement) {
    const supported =
      typeof CSS !== 'undefined' && CSS.supports && CSS.supports('scrollbar-gutter', 'stable');

    if (!supported || typeof document === 'undefined') {
      return false;
    }

    const doc = ownerDocument(referenceElement);
    const html = doc.documentElement;
    const body = doc.body;

    const scrollContainer = getViewportScroller(html, body);

    const originalScrollContainerOverflowY = scrollContainer.style.overflowY;
    const originalHtmlStyleGutter = html.style.scrollbarGutter;

    html.style.scrollbarGutter = 'stable';

    scrollContainer.style.overflowY = 'scroll';
    const before = scrollContainer.offsetWidth;

    scrollContainer.style.overflowY = 'hidden';
    const after = scrollContainer.offsetWidth;

    scrollContainer.style.overflowY = originalScrollContainerOverflowY;
    html.style.scrollbarGutter = originalHtmlStyleGutter;

    return before === after;
  }

  function preventScrollOverlayScrollbars(referenceElement) {
    const doc = ownerDocument(referenceElement);
    const html = doc.documentElement;
    const body = doc.body;

    // If an `overflow` style is present on <html>, we need to lock it, because a lock on <body>
    // won't have any effect.
    // But if <body> has an `overflow` style (like `overflow-x: hidden`), we need to lock it
    // instead, as sticky elements shift otherwise.
    const elementToLock = getViewportScroller(html, body);
    const originalElementToLockStyles = {
      overflowY: elementToLock.style.overflowY,
      overflowX: elementToLock.style.overflowX,
    };

    Object.assign(elementToLock.style, {
      overflowY: 'hidden',
      overflowX: 'hidden',
    });

    return () => {
      Object.assign(elementToLock.style, originalElementToLockStyles);
    };
  }

  function preventScrollInsetScrollbars(referenceElement) {
    const doc = ownerDocument(referenceElement);
    const html = doc.documentElement;
    const body = doc.body;
    const win = ownerWindow(html);

    let scrollTop = 0;
    let scrollLeft = 0;
    let updateGutterOnly = false;
    const resizeFrame = AnimationFrame.create();

    // Pinch-zoom in Safari causes a shift. Just don't lock scroll if there's any pinch-zoom.
    if (webkit && (win.visualViewport?.scale ?? 1) !== 1) {
      return () => {};
    }

    function lockScroll() {
      /* DOM reads: */

      const htmlStyles = win.getComputedStyle(html);
      const bodyStyles = win.getComputedStyle(body);
      const htmlScrollbarGutterValue = htmlStyles.scrollbarGutter || '';
      const hasBothEdges = htmlScrollbarGutterValue.includes('both-edges');
      const scrollbarGutterValue = hasBothEdges ? 'stable both-edges' : 'stable';

      scrollTop = html.scrollTop;
      scrollLeft = html.scrollLeft;

      originalHtmlStyles = {
        scrollbarGutter: html.style.scrollbarGutter,
        overflowY: html.style.overflowY,
        overflowX: html.style.overflowX,
      };
      originalHtmlScrollBehavior = html.style.scrollBehavior;

      originalBodyStyles = {
        position: body.style.position,
        height: body.style.height,
        width: body.style.width,
        boxSizing: body.style.boxSizing,
        overflowY: body.style.overflowY,
        overflowX: body.style.overflowX,
        scrollBehavior: body.style.scrollBehavior,
      };

      const isScrollableY = html.scrollHeight > html.clientHeight;
      const isScrollableX = html.scrollWidth > html.clientWidth;
      const hasConstantOverflowY =
        htmlStyles.overflowY === 'scroll' || bodyStyles.overflowY === 'scroll';
      const hasConstantOverflowX =
        htmlStyles.overflowX === 'scroll' || bodyStyles.overflowX === 'scroll';

      // Values can be negative in Firefox
      const scrollbarWidth = Math.max(0, win.innerWidth - body.clientWidth);
      const scrollbarHeight = Math.max(0, win.innerHeight - body.clientHeight);

      // Avoid shift due to the default <body> margin. This does cause elements to be clipped
      // with whitespace. Warn if <body> has margins?
      const marginY = parseFloat(bodyStyles.marginTop) + parseFloat(bodyStyles.marginBottom);
      const marginX = parseFloat(bodyStyles.marginLeft) + parseFloat(bodyStyles.marginRight);
      const elementToLock = getViewportScroller(html, body);

      updateGutterOnly = supportsStableScrollbarGutter(referenceElement);

      /*
       * DOM writes:
       * Do not read the DOM past this point!
       */

      if (updateGutterOnly) {
        html.style.scrollbarGutter = scrollbarGutterValue;
        elementToLock.style.overflowY = 'hidden';
        elementToLock.style.overflowX = 'hidden';
        return;
      }

      Object.assign(html.style, {
        scrollbarGutter: scrollbarGutterValue,
        overflowY: 'hidden',
        overflowX: 'hidden',
      });

      if (isScrollableY || hasConstantOverflowY) {
        html.style.overflowY = 'scroll';
      }
      if (isScrollableX || hasConstantOverflowX) {
        html.style.overflowX = 'scroll';
      }

      Object.assign(body.style, {
        position: 'relative',
        height:
          marginY || scrollbarHeight ? `calc(100dvh - ${marginY + scrollbarHeight}px)` : '100dvh',
        width: marginX || scrollbarWidth ? `calc(100vw - ${marginX + scrollbarWidth}px)` : '100vw',
        boxSizing: 'border-box',
        // Assign the longhands that `cleanup` restores, so nothing is left behind.
        overflowY: 'hidden',
        overflowX: 'hidden',
        scrollBehavior: 'unset',
      });

      body.scrollTop = scrollTop;
      body.scrollLeft = scrollLeft;
      html.setAttribute('data-tui-scroll-locked', '');
      html.style.scrollBehavior = 'unset';
    }

    function cleanup() {
      Object.assign(html.style, originalHtmlStyles);
      Object.assign(body.style, originalBodyStyles);

      if (!updateGutterOnly) {
        html.scrollTop = scrollTop;
        html.scrollLeft = scrollLeft;
        html.removeAttribute('data-tui-scroll-locked');
        html.style.scrollBehavior = originalHtmlScrollBehavior;
      }
    }

    function handleResize() {
      cleanup();
      resizeFrame.request(lockScroll);
    }

    lockScroll();
    win.addEventListener('resize', handleResize);

    return () => {
      resizeFrame.cancel();
      cleanup();
      // Sometimes this cleanup can run after test teardown because it is called
      // in a `setTimeout(fn, 0)`. Guard the returned cleanup to avoid calling
      // `removeEventListener` when it is no longer available in tests.
      if (typeof win.removeEventListener === 'function') {
        win.removeEventListener('resize', handleResize);
      }
    };
  }

  class ScrollLocker {
    lockCount = 0;
    restore = null;
    timeoutLock = Timeout.create();
    timeoutUnlock = Timeout.create();

    acquire(referenceElement) {
      this.lockCount += 1;
      if (this.lockCount === 1 && this.restore === null) {
        this.timeoutLock.start(0, () => this.lock(referenceElement));
      }
      return this.release;
    }

    release = () => {
      this.lockCount -= 1;
      if (this.lockCount === 0 && this.restore) {
        this.timeoutUnlock.start(0, this.unlock);
      }
    };

    unlock = () => {
      if (this.lockCount === 0 && this.restore) {
        this.restore?.();
        this.restore = null;
      }
    };

    lock(referenceElement) {
      if (this.lockCount === 0 || this.restore !== null) {
        return;
      }

      const doc = ownerDocument(referenceElement);
      const html = doc.documentElement;
      const body = doc.body;
      const win = ownerWindow(html);

      // The page is already locked, either by the site author or by a non-Base UI overlay that
      // hasn't cleaned up yet. Leave it alone and wait for the lock to clear before taking over,
      // otherwise we'd snapshot the locked state and restore it after our own lock is released.
      if (isPageScrollLocked(win, html, body)) {
        const observer = new win.MutationObserver(() => {
          if (isPageScrollLocked(win, html, body)) {
            return;
          }
          observer.disconnect();
          this.restore = null;
          this.lock(referenceElement);
        });

        // Watch every attribute: locks are applied through inline styles, classes, or attributes
        // paired with a stylesheet (`data-scroll-locked` in react-remove-scroll, for example).
        const options = { attributes: true };

        observer.observe(html, options);
        observer.observe(body, options);

        this.restore = () => observer.disconnect();
        return;
      }

      const hasOverlayScrollbars = ios || !hasInsetScrollbars(referenceElement);

      // On iOS, scroll locking does not work if the navbar is collapsed. Due to numerous
      // side effects and bugs that arise on iOS, it must be researched extensively before
      // being enabled to ensure it doesn't cause the following issues:
      // - Textboxes must scroll into view when focused, nor cause a glitchy scroll animation.
      // - The navbar must not force itself into view and cause layout shift.
      // - Scroll containers must not flicker upon closing a popup when it has an exit animation.
      this.restore = hasOverlayScrollbars
        ? preventScrollOverlayScrollbars(referenceElement)
        : preventScrollInsetScrollbars(referenceElement);
    }
  }

  const SCROLL_LOCKER = new ScrollLocker();

  // @base-ui/react/utils/useAnchoredPopupScrollLock.ts: run after positioning.
  const VIEWPORT_WIDTH_TOLERANCE_PX = 20;
  function anchoredPopupScrollLock(enabled, touchOpen, positionerElement, referenceElement) {
    let touchOpenShouldLockScroll = false;
    if (enabled && touchOpen && positionerElement != null) {
      const viewportWidth = ownerDocument(positionerElement).documentElement.clientWidth;
      const popupWidth = positionerElement.offsetWidth;
      touchOpenShouldLockScroll = viewportWidth > 0 && popupWidth > 0 &&
        popupWidth >= viewportWidth - VIEWPORT_WIDTH_TOLERANCE_PX;
    }
    return enabled && (!touchOpen || touchOpenShouldLockScroll)
      ? SCROLL_LOCKER.acquire(referenceElement)
      : () => {};
  }

  window.tui = window.tui || {};
  window.tui.scrollLock = {
    acquire: (referenceElement) => SCROLL_LOCKER.acquire(referenceElement),
    anchoredPopup: anchoredPopupScrollLock,
  };
})();

// components/checkbox/checkbox.js
(function () {
  "use strict";

  // Vanilla port of Base UI's checkbox: the root span behavior comes from
  // checkbox/root/CheckboxRoot.tsx, the non-native button keyboard semantics
  // from internals/use-button/useButton.ts. Clicks and Space forward to the
  // visually hidden native input beside the root; the input's change event
  // syncs the state attributes back onto the root and indicator.

  function inputOf(root) {
    const next = root.nextElementSibling;
    return next && next.matches("[data-tui-checkbox-input]") ? next : null;
  }

  function rootOf(input) {
    const prev = input.previousElementSibling;
    return prev && prev.matches("[data-tui-checkbox]") ? prev : null;
  }

  function isDisabled(root, input) {
    return (input && input.disabled) || root.getAttribute("aria-disabled") === "true";
  }

  function isReadOnly(root) {
    return root.getAttribute("aria-readonly") === "true";
  }

  // Port of utils/dispatchClickWithModifiers.ts: the constructed click keeps
  // the source event's modifier state and still runs native activation
  // behavior (toggling the input).
  function forwardClick(target, sourceEvent) {
    target.dispatchEvent(
      new PointerEvent("click", {
        bubbles: true,
        cancelable: true,
        composed: true,
        detail: 0,
        shiftKey: sourceEvent.shiftKey,
        ctrlKey: sourceEvent.ctrlKey,
        altKey: sourceEvent.altKey,
        metaKey: sourceEvent.metaKey,
      }),
    );
  }

  function sync(root, input) {
    const checked = input.checked;
    // The input's indeterminate IDL property is the source of truth for the
    // mixed state (Base UI's indeterminate prop): a user click clears it
    // natively, scripts set it and dispatch change.
    const indeterminate = input.indeterminate;
    root.setAttribute("aria-checked", indeterminate ? "mixed" : String(checked));
    root.toggleAttribute("data-indeterminate", indeterminate);
    root.toggleAttribute("data-checked", checked);
    root.toggleAttribute("data-unchecked", !checked);
    const indicator = root.querySelector('[data-slot="checkbox-indicator"]');
    if (indicator) {
      // Base UI unmounts the indicator while unchecked; we toggle [hidden].
      indicator.hidden = !checked && !indeterminate;
      indicator.toggleAttribute("data-indeterminate", indeterminate);
      indicator.toggleAttribute("data-checked", checked);
      indicator.toggleAttribute("data-unchecked", !checked);
    }
  }

  function requestCheckedChange(root, input, sourceEvent) {
    const nextChecked = !input.checked;
    const change = new CustomEvent("checkbox-change", {
      bubbles: true,
      cancelable: true,
      detail: { checked: nextChecked },
    });
    root.dispatchEvent(change);
    if (change.defaultPrevented || root.hasAttribute("data-tui-checkbox-controlled")) return;
    forwardClick(input, sourceEvent);
  }

  // CheckboxRoot onClick: cancel the click's default (a wrapping label would
  // otherwise forward it to the input a second time) and toggle through the
  // hidden input so the native change event fires.
  document.addEventListener("click", (e) => {
    const root = e.target.closest && e.target.closest("[data-tui-checkbox]");
    if (!root) return;
    const input = inputOf(root);
    if (!input) return;
    if (isDisabled(root, input)) {
      // useButton prevents clicks on disabled non-native buttons.
      e.preventDefault();
      return;
    }
    if (isReadOnly(root)) return;
    e.preventDefault();
    requestCheckedChange(root, input, e);
  });

  document.addEventListener("change", (e) => {
    const input = e.target;
    if (!input.matches || !input.matches("[data-tui-checkbox-input]")) return;
    const root = rootOf(input);
    if (root) sync(root, input);
  });

  document.addEventListener("keydown", (e) => {
    const root = e.target;
    if (!root.matches || !root.matches("[data-tui-checkbox]")) return;
    const input = inputOf(root);
    if (isDisabled(root, input)) return;
    if (e.key === "Enter") {
      // CheckboxRoot onKeyDown: Enter never toggles the checkbox, it submits
      // the owning form through its default submitter
      // (@base-ui/utils/getDefaultFormSubmitter.ts).
      if (e.defaultPrevented) return;
      e.preventDefault();
      const form = input && input.form;
      if (!form) return;
      for (const candidate of form.elements) {
        const tagName = candidate.tagName;
        if ((tagName === "BUTTON" || tagName === "INPUT") && candidate.type === "submit") {
          candidate.click();
          return;
        }
      }
    } else if (e.key === " ") {
      // useButton: Space activates on keyup; prevent the page scroll.
      e.preventDefault();
    }
  });

  // useButton keyup: Space dispatches the click on the root itself, which the
  // click handler above forwards to the input.
  document.addEventListener("keyup", (e) => {
    const root = e.target;
    if (!root.matches || !root.matches("[data-tui-checkbox]")) return;
    if (e.key !== " " || e.defaultPrevented) return;
    if (isDisabled(root, inputOf(root))) return;
    forwardClick(root, e);
  });

  // Focus on the hidden input (label clicks, programmatic focus) belongs on
  // the root (CheckboxRoot's input onFocus).
  document.addEventListener("focusin", (e) => {
    const input = e.target;
    if (!input.matches || !input.matches("[data-tui-checkbox-input]")) return;
    const root = rootOf(input);
    if (root) root.focus();
  });

  let labelId = 0;

  function setup(root) {
    if (root.hasAttribute("data-tui-checkbox-initialized")) return;
    root.setAttribute("data-tui-checkbox-initialized", "");
    const input = inputOf(root);
    if (!input) return;
    // SSR'd mixed state: the input element has no indeterminate attribute,
    // so the root's data-indeterminate seeds the IDL property.
    if (root.hasAttribute("data-indeterminate")) {
      input.indeterminate = true;
    }
    // The clicks dispatched on the hidden input are an implementation detail
    // and must not reach ancestors, which already receive the original click
    // (CheckboxRoot's input onClick).
    input.addEventListener("click", (e) => e.stopPropagation());
    // useAriaLabelledBy fallback: the span control is labelled by the native
    // label associated with the hidden input.
    if (!root.hasAttribute("aria-labelledby") && !root.hasAttribute("aria-label")) {
      const label =
        input.parentElement && input.parentElement.tagName === "LABEL"
          ? input.parentElement
          : input.labels && input.labels[0];
      if (label) {
        if (!label.id) {
          labelId += 1;
          label.id = (input.id || "tui-checkbox-" + labelId) + "-label";
        }
        root.setAttribute("aria-labelledby", label.id);
      }
    }
    sync(root, input);
  }

  function init() {
    document.querySelectorAll("[data-tui-checkbox]").forEach(setup);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });
})();

// components/dialog/dialog.js
(function () {
  "use strict";

  // Vanilla port of Base UI's Dialog (packages/react/src/dialog): the portal
  // node, backdrop and popup are SSRd divs; this script drives Base UI's
  // data-open/data-closed/data-starting-style/data-ending-style transition
  // lifecycle, the FloatingFocusManager focus trap (guards, initial focus,
  // return focus), useDismiss's escape/outside-press semantics, markOthers'
  // aria-hidden application to outside content and useScrollLock's deferred
  // body lock. "Unmount" is the portal node getting [hidden] again.

  // ----- registry ------------------------------------------------------------

  // Popup element -> per-dialog state. The open stack orders open dialogs by
  // open time (last = topmost), like Base UI's nested dialog counts.
  const dialogs = new Map();
  const openStack = [];

  function getDialog(target) {
    if (!target) return null;
    if (typeof target === "string") {
      const el = document.getElementById(target);
      return el && el.matches("[data-tui-dialog-content]") ? el : null;
    }
    if (target.matches?.("[data-tui-dialog-content]")) return target;
    return target.closest?.("[data-tui-dialog-content]") || null;
  }

  function stateOf(target) {
    const popup = getDialog(target);
    return popup ? dialogs.get(popup) : null;
  }

  function dialogFor(element) {
    const id =
      element.getAttribute("aria-controls") || element.getAttribute("data-tui-dialog-target");
    if (id) return getDialog(id);
    return getDialog(element);
  }

  function triggersFor(popup) {
    if (!popup.id) return [];
    return document.querySelectorAll(
      '[data-tui-dialog-trigger][aria-controls="' + popup.id + '"]',
    );
  }

  function isModal(state) {
    return state.popup.getAttribute("data-tui-dialog-show-modal") !== "false";
  }

  // ----- interaction type ----------------------------------------------------

  // FloatingFocusManager tracks the last pointer/keyboard interaction to pick
  // touch initial focus and keyboard-visible return focus.
  let lastInteractionType = "";
  document.addEventListener(
    "pointerdown",
    (event) => {
      lastInteractionType = event.pointerType || "mouse";
    },
    true,
  );
  document.addEventListener(
    "keydown",
    () => {
      lastInteractionType = "keyboard";
    },
    true,
  );

  // ----- tabbable (floating-ui-react/utils/tabbable.ts) ----------------------

  const CANDIDATE_SELECTOR =
    'a[href],button,input,select,textarea,summary,details,iframe,object,embed,[tabindex],[contenteditable]:not([contenteditable="false"]),audio[controls],video[controls]';

  function isFocusableElement(element) {
    if (
      !element.matches(CANDIDATE_SELECTOR) ||
      !element.isConnected ||
      element.matches(":disabled") ||
      (element.localName === "input" && element.type === "hidden")
    ) {
      return false;
    }
    for (let current = element; current; current = current.parentElement) {
      const isAncestor = current !== element;
      if (current.hasAttribute("inert") || current.hasAttribute("hidden")) return false;
      const style = getComputedStyle(current);
      if (style.display === "none") return false;
      if (!isAncestor && (style.visibility === "hidden" || style.visibility === "collapse")) {
        return false;
      }
      if (
        isAncestor &&
        current.localName === "details" &&
        !current.open &&
        !(current.querySelector(":scope > summary")?.contains(element))
      ) {
        return false;
      }
    }
    return true;
  }

  function getTabIndex(element) {
    const tabIndex = element.tabIndex;
    if (tabIndex < 0) {
      const name = element.localName;
      if (name === "details" || name === "audio" || name === "video" || element.isContentEditable) {
        return 0;
      }
    }
    return tabIndex;
  }

  function getNamedRadioInput(element) {
    return element.localName === "input" && element.type === "radio" && element.name !== ""
      ? element
      : null;
  }

  function isTabbableRadio(element, candidates) {
    const input = getNamedRadioInput(element);
    if (!input) return true;
    const group = candidates.filter((candidate) => {
      const radio = getNamedRadioInput(candidate);
      return radio && radio.name === input.name && radio.form === input.form;
    });
    const checked = group.find((radio) => radio.checked);
    return checked ? checked === input : group[0] === input;
  }

  function focusable(container) {
    return Array.from(container.querySelectorAll(CANDIDATE_SELECTOR)).filter(isFocusableElement);
  }

  function tabbable(container) {
    const candidates = focusable(container);
    return candidates.filter(
      (element) => getTabIndex(element) >= 0 && isTabbableRadio(element, candidates),
    );
  }

  function isTabbable(element) {
    return isFocusableElement(element) && getTabIndex(element) >= 0;
  }

  // FloatingFocusManager.getFirstTabbableElement: the element if it is
  // tabbable, otherwise its first tabbable child, otherwise itself.
  // (handleTabIndex is not ported: it early-returns for elements with an
  // authored tabindex, and FOCUSABLE_POPUP_PROPS always renders the dialog
  // popup with tabindex="-1" — ours is SSRd the same way and never changes.)
  function getFirstTabbableElement(container) {
    if (!container) return null;
    if (isTabbable(container)) return container;
    return tabbable(container)[0] || container;
  }

  // floating-ui-react/utils/enqueueFocus: focus lands on the next frame; a
  // newer enqueue cancels the previous one.
  let focusFrame = 0;
  function enqueueFocus(el, options = {}) {
    if (!el) return;
    cancelAnimationFrame(focusFrame);
    focusFrame = requestAnimationFrame(() => {
      if (options.shouldFocus && !options.shouldFocus()) return;
      el.focus(options);
    });
  }

  // ----- markOthers (floating-ui-react/utils/markOthers.ts) ------------------

  // Applies aria-hidden="true" to everything outside the open dialogs, with
  // reference counting so nested opens undo cleanly. aria-live regions are
  // kept, like Base UI. (Base UI's modal dialogs use aria-hidden, not inert:
  // pointer interaction is blocked by the full-viewport backdrop.)
  const ariaHiddenCounts = new WeakMap();
  const ariaHiddenUncontrolled = new WeakSet();

  function collectOutsideElements(keepElements, stopElements) {
    const outside = [];
    const walk = (parent) => {
      if (!parent || stopElements.has(parent)) return;
      for (const node of parent.children) {
        if (node.localName === "script") continue;
        if (keepElements.has(node)) {
          walk(node);
        } else {
          outside.push(node);
        }
      }
    };
    walk(document.body);
    return outside;
  }

  function buildKeepSet(targets) {
    const keep = new Set();
    targets.forEach((target) => {
      let node = target;
      while (node && !keep.has(node)) {
        keep.add(node);
        node = node.parentElement;
      }
    });
    return keep;
  }

  function markOthers(avoidElements) {
    const controlElements = avoidElements.concat(
      Array.from(document.body.querySelectorAll("[aria-live]")),
    );
    const targets = collectOutsideElements(
      buildKeepSet(controlElements),
      new Set(controlElements),
    );
    const hiddenElements = [];

    targets.forEach((node) => {
      const attr = node.getAttribute("aria-hidden");
      const alreadyHidden = attr !== null && attr !== "false";
      const count = (ariaHiddenCounts.get(node) || 0) + 1;
      ariaHiddenCounts.set(node, count);
      hiddenElements.push(node);
      if (count === 1 && alreadyHidden) ariaHiddenUncontrolled.add(node);
      if (!alreadyHidden) node.setAttribute("aria-hidden", "true");
    });

    return () => {
      hiddenElements.forEach((node) => {
        const count = (ariaHiddenCounts.get(node) || 0) - 1;
        ariaHiddenCounts.set(node, count);
        if (count <= 0) {
          if (!ariaHiddenUncontrolled.has(node)) node.removeAttribute("aria-hidden");
          ariaHiddenUncontrolled.delete(node);
        }
      });
    };
  }

  // ----- aria wiring (useDialogTitle/-Description registration) --------------

  function wireAria(state) {
    const popup = state.popup;
    const title = popup.querySelector("[data-tui-dialog-title]");
    if (title) {
      if (!title.id) title.id = popup.id + "-title";
      popup.setAttribute("aria-labelledby", title.id);
    } else {
      popup.removeAttribute("aria-labelledby");
    }
    const description = popup.querySelector("[data-tui-dialog-description]");
    if (description) {
      if (!description.id) description.id = popup.id + "-description";
      popup.setAttribute("aria-describedby", description.id);
    } else {
      popup.removeAttribute("aria-describedby");
    }
  }

  // ----- transition lifecycle ------------------------------------------------

  function setTransitionAttributes(state, attrs) {
    [state.backdrop, state.popup].forEach((el) => {
      if (!el) return;
      ["data-open", "data-closed", "data-starting-style", "data-ending-style"].forEach((name) => {
        if (attrs.includes(name)) {
          el.setAttribute(name, "");
        } else {
          el.removeAttribute(name);
        }
      });
    });
  }

  // useOpenChangeComplete/useAnimationsFinished: wait for every animation and
  // transition on the popup to finish, then run fn (a resolved microtask runs
  // before the browser paints the post-animation frame, so hiding here never
  // flashes the natural styles, like Base UI's flushSync unmount).
  function whenAnimationsFinish(state, fn) {
    const token = {};
    state.finishToken = token;
    const popup = state.popup;
    if (typeof popup.getAnimations !== "function") {
      fn();
      return;
    }
    // Base UI waits on the popup's animations only (useOpenChangeComplete's
    // ref is the popup); the backdrop uses the same durations.
    Promise.allSettled(popup.getAnimations().map((animation) => animation.finished)).then(() => {
      if (state.finishToken === token) fn();
    });
  }

  // ----- nested dialog bookkeeping ------------------------------------------

  // A dialog is nested when its hidden portal node was SSRd inside another
  // dialog's content — the DOM pendant of Base UI's parent DialogRootContext. The
  // relation is recorded at registration (see ensureDialog); parentOf resolves
  // it to the parent's live state.
  function parentOf(state) {
    const parentId = state.root.getAttribute("data-tui-dialog-parent");
    return parentId ? stateOf(parentId) : null;
  }

  function nestedOpenCount(state) {
    return openStack.filter((other) => {
      for (let p = parentOf(other); p; p = parentOf(p)) {
        if (p === state) return true;
      }
      return false;
    }).length;
  }

  function updateNestedAttributes() {
    openStack.forEach((state) => {
      const count = nestedOpenCount(state);
      state.popup.style.setProperty("--nested-dialogs", String(count));
      state.popup.toggleAttribute("data-nested-dialog-open", count > 0);
    });
  }

  function isTopmost(state) {
    return nestedOpenCount(state) === 0;
  }

  // ----- open / close --------------------------------------------------------

  function updateTriggers(state, isOpen) {
    triggersFor(state.popup).forEach((trigger) => {
      trigger.setAttribute("aria-expanded", isOpen ? "true" : "false");
      trigger.toggleAttribute("data-popup-open", isOpen);
    });
  }

  function openDialog(target, trigger) {
    const state = stateOf(target);
    if (!state || state.open) return;
    state.finishToken = null; // cancel a pending exit unmount

    const popup = state.popup;
    state.openType = trigger ? lastInteractionType || "mouse" : null;
    state.trigger =
      trigger && trigger instanceof Element ? trigger : triggersFor(popup)[0] || null;
    state.previouslyFocused = document.activeElement;

    state.open = true;
    openStack.push(state);
    updateNestedAttributes();

    // FloatingPortal appends at open time, keeping paint order = open order.
    document.body.appendChild(state.root);
    state.root.hidden = false;

    wireAria(state);

    // useTransitionStatus: mount with data-open + data-starting-style, drop
    // the starting style a frame later so CSS transitions see the start
    // values (the reflow guarantees they were computed).
    setTransitionAttributes(state, ["data-open", "data-starting-style"]);
    void popup.offsetWidth;
    requestAnimationFrame(() => {
      if (state.open) setTransitionAttributes(state, ["data-open"]);
    });

    if (isModal(state)) {
      state.releaseScroll = window.tui.scrollLock.acquire(popup);
      state.undoMarkOthers = markOthers([state.root]);
    }

    updateTriggers(state, true);

    // FloatingFocusManager initial focus: first tabbable element, or the
    // popup itself — also when opened by touch, so the virtual keyboard
    // stays closed (createDefaultInitialFocus).
    queueMicrotask(() => {
      if (!state.open) return;
      if (popup.contains(document.activeElement)) return;
      const elToFocus =
        state.openType === "touch" ? popup : tabbable(popup)[0] || popup;
      enqueueFocus(elToFocus, {
        preventScroll: elToFocus === popup,
        shouldFocus() {
          if (!state.open) return false;
          const active = document.activeElement;
          return !(active !== elToFocus && popup.contains(active));
        },
      });
    });
  }

  function closeDialog(target) {
    const state = stateOf(target);
    if (!state || !state.open) return;

    const popup = state.popup;
    state.open = false;
    state.closeType = lastInteractionType;
    const index = openStack.indexOf(state);
    if (index !== -1) openStack.splice(index, 1);
    updateNestedAttributes();

    // Base UI order on open=false: the transition status flips to ending,
    // aria-hidden marking and the scroll lock release immediately, the
    // popup unmounts (and focus returns) once the exit animation finishes.
    setTransitionAttributes(state, ["data-closed", "data-ending-style"]);
    if (state.undoMarkOthers) {
      state.undoMarkOthers();
      state.undoMarkOthers = null;
    }
    state.releaseScroll?.();
    state.releaseScroll = null;
    updateTriggers(state, false);

    whenAnimationsFinish(state, () => {
      state.root.hidden = true;
      setTransitionAttributes(state, []);
      popup.style.removeProperty("--nested-dialogs");
      popup.removeAttribute("data-nested-dialog-open");
      returnFocus(state);
      // onOpenChangeComplete(false) pendant: fires once the exit animation
      // finished and the dialog unmounted (command.js resets its palette on
      // this).
      popup.dispatchEvent(new CustomEvent("dialog-close", { bubbles: true }));
    });
  }

  // FloatingFocusManager return focus: the trigger (or the previously
  // focused element for programmatic opens), resolved to its first tabbable,
  // focused without scrolling — visibly when the dialog was closed with the
  // keyboard. Focus that legitimately moved elsewhere is respected.
  function returnFocus(state) {
    const referenceReturn = state.trigger?.isConnected ? state.trigger : null;
    const previousReturn =
      state.previouslyFocused?.isConnected &&
      state.previouslyFocused.localName !== "body"
        ? state.previouslyFocused
        : null;
    const preferPreviousFocus = state.openType == null;
    const returnElement = preferPreviousFocus
      ? previousReturn || referenceReturn
      : referenceReturn || previousReturn;

    queueMicrotask(() => {
      const tabbableReturnElement = getFirstTabbableElement(returnElement);
      if (!tabbableReturnElement) return;
      const active = document.activeElement;
      const focusMovedElsewhere =
        tabbableReturnElement !== active &&
        active !== document.body &&
        !state.popup.contains(active) &&
        !state.root.contains(active);
      if (focusMovedElsewhere) return;
      const options = { preventScroll: true };
      if (state.closeType === "keyboard") options.focusVisible = true;
      tabbableReturnElement.focus(options);
    });
  }

  function isDialogOpen(target) {
    return stateOf(target)?.open || false;
  }

  function requestOpenChange(target, nextOpen, trigger) {
    const state = stateOf(target);
    if (!state || state.open === nextOpen) return false;
    const accepted = state.popup.dispatchEvent(
      new CustomEvent("dialog-open-change", {
        bubbles: true,
        cancelable: true,
        detail: { open: nextOpen },
      }),
    );
    if (!accepted || state.popup.hasAttribute("data-tui-dialog-controlled")) return false;
    if (nextOpen) openDialog(state.popup, trigger);
    else closeDialog(state.popup);
    return true;
  }

  function toggleDialog(target, trigger) {
    requestOpenChange(target, !isDialogOpen(target), trigger);
  }

  // ----- dismissal (useDismiss + DialogInteractions) -------------------------

  // With a rendered backdrop, Base UI's outsidePressEvent is 'intentional':
  // the dismissal fires on the click that completes a press on the dialog's
  // owning backdrop, only for the topmost dialog, only for the main button.
  // A press that starts inside the popup and is released over the backdrop
  // (text selection drag-out) never dismisses.
  let pressStartedInPopup = null;
  document.addEventListener(
    "pointerdown",
    (event) => {
      pressStartedInPopup =
        event.target instanceof Element
          ? event.target.closest("[data-tui-dialog-content]")
          : null;
    },
    true,
  );

  function handleBackdropClick(backdrop, event) {
    const state = stateOf(backdrop.parentElement?.querySelector("[data-tui-dialog-content]"));
    if (!state || !state.open) return;
    if (state.popup.hasAttribute("data-tui-dialog-disable-dismissible")) return;
    if (!isTopmost(state)) return;
    if (event.button !== 0) return;
    if (pressStartedInPopup === state.popup) return;
    requestOpenChange(state.popup, false);
  }

  // useDismiss escape key: closes the topmost dialog, ignoring presses that
  // settle an IME composition (Safari fires compositionend before keydown,
  // so the flag is cleared a few ms later there).
  let isComposing = false;
  let compositionTimer;
  const isWebkit =
    typeof navigator !== "undefined" && /AppleWebKit/.test(navigator.userAgent) && !/Chrome/.test(navigator.userAgent);
  document.addEventListener("compositionstart", () => {
    window.clearTimeout(compositionTimer);
    isComposing = true;
  });
  document.addEventListener("compositionend", () => {
    compositionTimer = window.setTimeout(
      () => {
        isComposing = false;
      },
      isWebkit ? 5 : 0,
    );
  });

  const escapeTargets = new WeakSet();
  function listenForEscape(element) {
    if (!element || escapeTargets.has(element)) return;
    element.addEventListener("keydown", closeOnEscapeKeyDown);
    escapeTargets.add(element);
  }

  // useDismiss installs the same handler on the popup, reference and document.
  function closeOnEscapeKeyDown(event) {
    if (event.key !== "Escape" || isComposing) return;
    const top = openStack[openStack.length - 1];
    const state = event.currentTarget === document
      ? top
      : stateOf(dialogFor(event.currentTarget));
    // A nested open dialog blocks its parent's useDismiss handler.
    if (!state?.open || state !== top) return;
    if (requestOpenChange(state.popup, false)) event.preventDefault();
    event.stopPropagation();
    return true;
  }

  document.addEventListener("keydown", (event) => {
    if (closeOnEscapeKeyDown(event)) return;
    // FloatingFocusManager: prevent Tab from escaping the modal when the
    // popup has no tabbable elements (the guards would have nothing to
    // focus).
    if (event.key === "Tab") {
      const state = openStack.find(
        (other) => isModal(other) && other.popup.contains(document.activeElement),
      );
      if (state && tabbable(state.popup).length === 0) {
        event.preventDefault();
        event.stopPropagation();
      }
    }
  });

  // ----- initialization ------------------------------------------------------

  // FocusGuard: visually hidden tabbable sentinels around the popup; focusing
  // one wraps focus to the other end of the popup's tab cycle.
  function createFocusGuard() {
    const guard = document.createElement("span");
    guard.setAttribute("tabindex", "0");
    guard.setAttribute("aria-hidden", "true");
    guard.setAttribute("data-tui-dialog-focus-guard", "");
    guard.style.cssText =
      "clip-path:inset(50%);overflow:hidden;white-space:nowrap;border:0;padding:0;width:1px;height:1px;margin:-1px;position:fixed;top:0;left:0;";
    return guard;
  }

  function ensureDialog(root) {
    const popup = root.querySelector("[data-tui-dialog-content]");
    if (!popup || dialogs.has(popup)) return dialogs.get(popup) || null;

    const parentPopup = root.parentElement?.closest("[data-tui-dialog-content]");
    if (parentPopup?.id) root.setAttribute("data-tui-dialog-parent", parentPopup.id);
    if (!root._tuiPortalOwner) root._tuiPortalOwner = root.parentElement;

    const state = {
      root,
      popup,
      backdrop: root.querySelector("[data-tui-dialog-backdrop]"),
      open: false,
      trigger: null,
      previouslyFocused: null,
      openType: null,
      closeType: "",
      undoMarkOthers: null,
      releaseScroll: null,
      finishToken: null,
    };
    dialogs.set(popup, state);
    listenForEscape(popup);

    // A nested dialog renders no backdrop in Base UI (DialogBackdrop's
    // enabled: !nested); the parent's backdrop keeps covering the page.
    if (root.hasAttribute("data-tui-dialog-parent")) {
      popup.setAttribute("data-nested", "");
      if (state.backdrop) state.backdrop.hidden = true;
    }

    const beforeGuard = createFocusGuard();
    const afterGuard = createFocusGuard();
    if (!isModal(state)) {
      // Non-modal dialogs do not trap focus: the guards stay out of the tab
      // order (Base UI renders different non-modal guard behavior; without a
      // React portal boundary the natural tab order is the equivalent).
      beforeGuard.setAttribute("tabindex", "-1");
      afterGuard.setAttribute("tabindex", "-1");
    }
    popup.before(beforeGuard);
    popup.after(afterGuard);
    beforeGuard.addEventListener("focus", () => {
      if (!isModal(state)) return;
      const els = tabbable(popup);
      enqueueFocus(els[els.length - 1] || popup, { preventScroll: els.length === 0 });
    });
    afterGuard.addEventListener("focus", () => {
      if (!isModal(state)) return;
      const els = tabbable(popup);
      enqueueFocus(els[0] || popup, { preventScroll: els.length === 0 });
    });

    // FloatingFocusManager restoreFocus="popup": when the focused element is
    // removed from inside the popup (e.g. an htmx swap of the dialog body),
    // focus falls back to the popup instead of escaping to <body>.
    popup.addEventListener("focusout", (event) => {
      const target = event.target;
      queueMicrotask(() => {
        if (!state.open) return;
        if (target instanceof Element && target.isConnected) return;
        if (document.activeElement === document.body) {
          popup.focus();
          requestAnimationFrame(() => {
            if (state.open && document.activeElement === document.body) popup.focus();
          });
        }
      });
    });

    wireAria(state);
    return state;
  }

  // Fully retire a dialog: undo aria-hidden marking, release the scroll
  // lock and remove the portaled DOM. Used when an htmx/datastar swap
  // removed the dialog's source from the page or replaced it with a fresh
  // hidden portal node.
  function destroyDialog(popup) {
    const state = dialogs.get(popup);
    if (!state) {
      popup.closest("[data-tui-dialog-root]")?.remove();
      return;
    }
    state.finishToken = null;
    if (state.undoMarkOthers) {
      state.undoMarkOthers();
      state.undoMarkOthers = null;
    }
    const index = openStack.indexOf(state);
    if (index !== -1) openStack.splice(index, 1);
    const wasOpen = state.open;
    state.open = false;
    updateNestedAttributes();
    state.releaseScroll?.();
    state.releaseScroll = null;
    if (wasOpen) popup.dispatchEvent(new CustomEvent("dialog-close", { bubbles: true }));
    state.root.remove();
    dialogs.delete(popup);
  }

  function init() {
    document.querySelectorAll("[data-tui-dialog-trigger]").forEach(listenForEscape);
    // A dialog lives as long as its SSR declaration site (_tuiPortalOwner)
    // stays in the document, including trigger-less programmatic dialogs.
    // Retire registered dialogs even when their root itself was removed.
    dialogs.forEach((state, popup) => {
      if (!state.root.isConnected || (state.root._tuiPortalOwner && !state.root._tuiPortalOwner.isConnected)) {
        destroyDialog(popup);
      }
    });
    document.querySelectorAll("[data-tui-dialog-root]").forEach((root) => {
      const popup = root.querySelector("[data-tui-dialog-content]");
      if (!popup) {
        root.remove();
        return;
      }

      if (dialogs.has(popup)) return;

      const fresh = ensureDialog(root);
      if (!fresh) return;

      if (popup.getAttribute("data-tui-dialog-initial-open") === "true") {
        // One-shot: consume the attribute so a later re-init never re-opens
        // a closed dialog.
        popup.removeAttribute("data-tui-dialog-initial-open");
        openDialog(popup);
      }
    });
  }

  document.addEventListener("click", (event) => {
    if (!(event.target instanceof Element)) return;
    const trigger = event.target.closest("[data-tui-dialog-trigger]");
    if (trigger) {
      toggleDialog(dialogFor(trigger), trigger);
      return;
    }
    const closeButton = event.target.closest("[data-tui-dialog-close]");
    if (closeButton) {
      requestOpenChange(dialogFor(closeButton), false);
      return;
    }
    const backdrop = event.target.closest("[data-tui-dialog-backdrop]");
    if (backdrop) {
      handleBackdropClick(backdrop, event);
    }
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => init());
  } else {
    init();
  }

  // Initialize dialogs added later (e.g. swapped in via htmx), so a
  // server-rendered dialog with Open true still opens. Also retire dialogs
  // whose source got swapped out of the DOM (releasing the scroll lock and
  // the aria-hidden marking).
  new MutationObserver(() => {
    init();
  }).observe(document.body, {
    childList: true,
    subtree: true,
  });

  window.tui = window.tui || {};
  window.tui.dialog = {
    open: openDialog,
    close: closeDialog,
    toggle: toggleDialog,
    isOpen: isDialogOpen,
  };
})();

// components/dropdownmenu/dropdownmenu.js
// Uses window.FloatingUIDOM from components/floatingui (loaded in the same bundle).
(function () {
  const EXIT_MS = 120; // exit animation (duration-100) + slack
  const COLLISION_PADDING = 5;
  // Submenu hover intent, like Base UI: open fast, close with a grace delay so
  // moving the mouse diagonally into the submenu does not flicker.
  const SUB_OPEN_DELAY = 100;
  const SUB_CLOSE_DELAY = 300;

  const escapeTargets = new WeakSet();
  function listenForEscape(element) {
    if (!element || escapeTargets.has(element)) return;
    element.addEventListener("keydown", closeOnEscapeKeyDown);
    escapeTargets.add(element);
  }

  // useDismiss: popup/reference listeners stop Escape before outer document handlers.
  function closeOnEscapeKeyDown(event) {
    if (event.key !== "Escape") return;
    const contents = event.currentTarget === document
      ? allContents()
      : [event.currentTarget.hasAttribute("data-tui-dropdownmenu-content")
        ? event.currentTarget
        : contentFor(event.currentTarget)];
    let handled = false;
    for (const content of contents) {
      if (!content?.hasAttribute("data-open")) continue;
      if (requestOpenChange(content, false, false, true)) event.preventDefault();
      event.stopPropagation();
      handled = true;
    }
    return handled;
  }

  function allContents() {
    return document.querySelectorAll("[data-tui-dropdownmenu-content]");
  }

  function triggerFor(content) {
    return document.querySelector(
      '[data-tui-dropdownmenu-trigger][aria-controls="' + content.id + '"]',
    );
  }

  function contentFor(trigger) {
    return document.getElementById(trigger.getAttribute("aria-controls"));
  }

  function popupFor(content) {
    return content.querySelector("[data-tui-dropdownmenu-popup]");
  }

  function setState(content, state) {
    const open = state === "open";
    content.toggleAttribute("data-open", open);
    content.toggleAttribute("data-closed", !open);
    const popup = popupFor(content);
    if (popup) {
      popup.toggleAttribute("data-open", open);
      popup.toggleAttribute("data-closed", !open);
    }
  }

  function isOpen(el) {
    return !!el && el.hasAttribute("data-open");
  }

  function setTransitionAttribute(content, name, present) {
    content.toggleAttribute(name, present);
    const popup = popupFor(content);
    if (popup) popup.toggleAttribute(name, present);
  }

  function startTransition(content) {
    setTransitionAttribute(content, "data-ending-style", false);
    setTransitionAttribute(content, "data-starting-style", true);
    requestAnimationFrame(() => {
      requestAnimationFrame(() => setTransitionAttribute(content, "data-starting-style", false));
    });
  }

  function setChecked(item, checked) {
    item.toggleAttribute("data-checked", checked);
    item.toggleAttribute("data-unchecked", !checked);
    item.setAttribute("aria-checked", checked ? "true" : "false");
  }

  function setSide(content, side) {
    content.setAttribute("data-side", side);
    const popup = popupFor(content);
    if (popup) popup.setAttribute("data-side", side);
  }

  // Base UI zooms the popup out of the anchor's center point (e.g.
  // "96px -4px"), not out of a placement corner.
  function anchorOrigin(result, anchorRect, positionerRect, sideOffset) {
    const side = result.placement.split("-")[0];
    const centerX = anchorRect.left + anchorRect.width / 2 - positionerRect.left + "px";
    const centerY = anchorRect.top + anchorRect.height / 2 - positionerRect.top + "px";
    if (side === "bottom") return centerX + " " + -sideOffset + "px";
    if (side === "top") return centerX + " calc(100% + " + sideOffset + "px)";
    if (side === "right") return -sideOffset + "px " + centerY;
    return "calc(100% + " + sideOffset + "px) " + centerY;
  }

  // Moves the content to <body> (shadcn portals it the same way).
  // The unmount half of the React portal pendant: a portaled content lives
  // as long as its SSR declaration site (_tuiPortalOwner) stays in the
  // document. Trigger-presence heuristics judged mid-swap moments wrongly -
  // multi-phase swap layers briefly disconnect the new triggers.
  function removeOrphanedContents(content) {
    document.querySelectorAll("body > [data-tui-dropdownmenu-content]").forEach((c) => {
      if (c !== content && c._tuiPortalOwner && !c._tuiPortalOwner.isConnected) {
        stopAutoPositioning(c);
        c._tuiReleaseScroll?.();
        c._tuiReleaseScroll = null;
        c.remove();
      }
    });
  }

  function portal(content) {
    listenForEscape(content);
    removeOrphanedContents(content);
    if (content.parentElement !== document.body) {
      if (!content._tuiPortalOwner) content._tuiPortalOwner = content.parentElement;
      document.body.appendChild(content);
    }
  }

  function positionMenu(content, trigger) {
    const { computePosition, offset, flip, shift, size } = window.FloatingUIDOM;
    const mobile = window.matchMedia("(max-width: 767px)").matches;
    const side =
      (mobile && content.getAttribute("data-tui-dropdownmenu-mobile-side")) ||
      content.getAttribute("data-tui-dropdownmenu-side") ||
      "bottom";
    const align =
      (mobile && content.getAttribute("data-tui-dropdownmenu-mobile-align")) ||
      content.getAttribute("data-tui-dropdownmenu-align") ||
      "start";
    const sideOffset =
      parseInt(content.getAttribute("data-tui-dropdownmenu-side-offset"), 10) || 4;
    const alignOffset =
      parseInt(content.getAttribute("data-tui-dropdownmenu-align-offset"), 10) || 0;
    const placement = align === "center" ? side : side + "-" + align;

    return computePosition(trigger, content, {
      placement: placement,
      strategy: "absolute",
      middleware: [
        offset({ mainAxis: sideOffset, alignmentAxis: alignOffset }),
        flip({ padding: COLLISION_PADDING }),
        shift({ padding: COLLISION_PADDING }),
        size({
          padding: COLLISION_PADDING,
          apply(args) {
            content.style.setProperty(
              "--available-height",
              args.availableHeight + "px",
            );
            content.style.setProperty(
              "--anchor-width",
              args.rects.reference.width + "px",
            );
          },
        }),
      ],
    }).then((result) => {
      content.style.left = result.x + "px";
      content.style.top = result.y + "px";
      setSide(content, result.placement.split("-")[0]);
      const popup = popupFor(content);
      if (popup) {
        popup.style.setProperty(
          "--transform-origin",
          anchorOrigin(
            result,
            trigger.getBoundingClientRect(),
            content.getBoundingClientRect(),
            sideOffset,
          ),
        );
      }
    });
  }

  // Base UI keeps mounted popups attached to their anchors while ancestors
  // move, resize, scroll, or shift layout. This also tracks a mobile sidebar
  // while its opening transform is still settling.
  function startAutoPositioning(content, trigger) {
    if (content._tuiPositionCleanup) content._tuiPositionCleanup();
    let resolveFirst;
    const firstPosition = new Promise((resolve) => {
      resolveFirst = resolve;
    });
    const update = () => positionMenu(content, trigger).then(resolveFirst, resolveFirst);
    content._tuiPositionCleanup = window.FloatingUIDOM.autoUpdate(trigger, content, update, {
      elementResize: typeof ResizeObserver !== "undefined",
      layoutShift: typeof IntersectionObserver !== "undefined",
    });
    return firstPosition;
  }

  function stopAutoPositioning(content) {
    if (!content._tuiPositionCleanup) return;
    content._tuiPositionCleanup();
    content._tuiPositionCleanup = null;
  }

  // ----- focus highlighting (Base UI moves real focus to menu items) --------

  const ITEM_SELECTOR = '[role="menuitem"], [role="menuitemcheckbox"], [role="menuitemradio"]';

  // The menu container the keyboard navigates in: the deepest open submenu
  // holding focus, otherwise the root popup.
  function containerOf(el) {
    return el.closest("[data-tui-dropdownmenu-sub-content], [data-tui-dropdownmenu-popup]");
  }

  function itemsIn(container) {
    return [...container.querySelectorAll(ITEM_SELECTOR)].filter(
      (item) =>
        containerOf(item) === container &&
        !item.disabled &&
        item.getAttribute("aria-disabled") !== "true",
    );
  }

  // Focus waits until after the input task:
  // Chromium's mousedown default focuses the trigger, WebKit's clears focus.
  // One frame, like Base UI, with a guard for a popup that closed meanwhile.
  function enqueueFocus(el, shouldFocus) {
    if (!el) return;
    requestAnimationFrame(() => {
      if (shouldFocus && !shouldFocus()) return;
      el.focus({ preventScroll: true });
    });
  }

  function focusItem(item) {
    if (item && document.activeElement !== item) item.focus({ preventScroll: false });
  }

  // Wraps at both ends, the pendant of Menu.Root's loopFocus, which the
  // reference defaults to true: ArrowDown on the last item returns to the
  // first and ArrowUp on the first goes to the last. Disabled items stay out
  // of the walk — itemsIn filters them, because ours are natively disabled
  // buttons rather than the aria-disabled ones the reference keeps focusable.
  function moveFocus(container, delta) {
    const items = itemsIn(container);
    if (!items.length) return;
    const index = items.indexOf(document.activeElement);
    if (index === -1) {
      focusItem(delta > 0 ? items[0] : items[items.length - 1]);
      return;
    }
    focusItem(items[(index + delta + items.length) % items.length]);
  }

  // ----- open / close --------------------------------------------------------

  // focusOn: "first" or "last" lands focus on that item once the menu is in
  // place, anything falsy focuses the popup. `true` still means "first".
  function open(content, trigger, focusOn) {
    allContents().forEach((c) => {
      if (c !== content) close(c);
    });
    clearTimeout(content._tuiHide);
    content._tuiOpenMethod = trigger._tuiOpenMethod || "programmatic";
    portal(content);
    // z-index portal like shadcn (no native top layer); re-append
    // keeps paint order = open order.
    document.body.appendChild(content);
    content.hidden = false;

    // Position it invisibly first, then play the enter animation in place.
    content.style.visibility = "hidden";
    const finish = () => {
      // duration-100 transitions `all`; a visibility transition would
      // freeze at hidden in background tabs - flip suppressed.
      const popup = popupFor(content);
      content.style.transitionProperty = "none";
      if (popup) popup.style.transitionProperty = "none";
      content.style.visibility = "";
      void content.offsetWidth;
      content.style.transitionProperty = "";
      if (popup) popup.style.transitionProperty = "";
      if (content.hidden || !content.isConnected) return;
      // useAnchoredPopupScrollLock measures the positioned popup for touch opens.
      content._tuiReleaseScroll?.();
      content._tuiReleaseScroll = window.tui.scrollLock.anchoredPopup(
        true, content._tuiOpenMethod === "touch", content, trigger,
      );
      setState(content, "open");
      startTransition(content);
      trigger.setAttribute("aria-expanded", "true");
      trigger.setAttribute("data-popup-open", "");
      trigger.setAttribute("data-pressed", "");
      if (!popup) return;
      syncSubState(popup);
      // The guard is the same one the reference uses: do not pull focus back
      // into a popup that closed while the frame was queued.
      const stillOpen = () => isOpen(content);
      if (focusOn) {
        const items = itemsIn(popup);
        enqueueFocus((focusOn === "last" ? items[items.length - 1] : items[0]) || popup, stillOpen);
      } else {
        enqueueFocus(popup, stillOpen);
      }
    };
    startAutoPositioning(content, trigger).then(finish, finish);
  }

  function close(content, refocusTrigger) {
    if (content.hidden) return;
    stopAutoPositioning(content);
    setTransitionAttribute(content, "data-starting-style", false);
    setState(content, "closed");
    setTransitionAttribute(content, "data-ending-style", true);
    content.querySelectorAll("[data-tui-dropdownmenu-sub]").forEach(closeSubNow);
    const trigger = triggerFor(content);
    if (trigger) {
      trigger.setAttribute("aria-expanded", "false");
      trigger.removeAttribute("data-popup-open");
      trigger.removeAttribute("data-pressed");
      if (refocusTrigger) trigger.focus({ preventScroll: true });
    }
    clearTimeout(content._tuiHide);
    content._tuiHide = setTimeout(() => {
      if (content.hasAttribute("data-closed") && !content.hidden) {
        content.hidden = true;
        setTransitionAttribute(content, "data-ending-style", false);
      }
    }, EXIT_MS);
    content._tuiReleaseScroll?.();
    content._tuiReleaseScroll = null;
  }

  function closeAll(refocusTrigger) {
    allContents().forEach((content) => close(content, refocusTrigger));
  }

  function requestOpenChange(content, nextOpen, focusOn, refocusTrigger) {
    if (!content || isOpen(content) === nextOpen) return false;
    const accepted = content.dispatchEvent(
      new CustomEvent("dropdownmenu-open-change", {
        bubbles: true,
        cancelable: true,
        detail: { open: nextOpen },
      }),
    );
    if (!accepted || content.hasAttribute("data-tui-dropdownmenu-controlled")) return false;
    const trigger = triggerFor(content);
    if (nextOpen && trigger) open(content, trigger, focusOn);
    else if (!nextOpen) close(content, refocusTrigger);
    return true;
  }

  function requestCloseAll(refocusTrigger) {
    allContents().forEach((content) =>
      requestOpenChange(content, false, false, refocusTrigger),
    );
  }

  function anyOpen() {
    return [...allContents()].find(isOpen) || null;
  }

  // ----- submenus -------------------------------------------------------------

  function subParts(sub) {
    return {
      trigger: sub.querySelector("[data-tui-dropdownmenu-sub-trigger]"),
      content: sub.querySelector("[data-tui-dropdownmenu-sub-content]"),
    };
  }

  function openSub(sub, focusFirst) {
    const { trigger, content } = subParts(sub);
    if (!trigger || !content) return;
    content.classList.remove("hidden");
    content.style.visibility = "hidden";

    const { computePosition, offset, flip, shift } = window.FloatingUIDOM;
    // Base UI submenu placement: right-start, sideOffset 0, alignOffset -3.
    computePosition(trigger, content, {
      placement: "right-start",
      strategy: "fixed",
      middleware: [
        offset({ mainAxis: 0, alignmentAxis: -3 }),
        flip({ padding: COLLISION_PADDING }),
        shift({ padding: COLLISION_PADDING }),
      ],
    }).then((result) => {
      if (content.classList.contains("hidden")) return; // closed meanwhile
      content.style.transition = "none";
      content.style.left = result.x + "px";
      content.style.top = result.y + "px";
      content.setAttribute("data-side", result.placement.split("-")[0]);
      content.style.setProperty(
        "--transform-origin",
        anchorOrigin(result, trigger.getBoundingClientRect(), content.getBoundingClientRect(), 0),
      );
      content.offsetHeight; // flush styles before re-enabling transitions
      content.style.transition = "";
      // duration-100 transitions `all`; a visibility transition would
      // freeze at hidden in background tabs - flip suppressed.
      content.style.transitionProperty = "none";
      content.style.visibility = "";
      void content.offsetWidth;
      content.style.transitionProperty = "";
      content.setAttribute("data-open", "");
      content.removeAttribute("data-closed");
      startTransition(content);
      trigger.setAttribute("data-popup-open", "");
	  trigger.setAttribute("aria-expanded", "true");
      if (focusFirst) focusItem(itemsIn(content)[0] || content);
    });
  }

  // Closes with the exit animation.
  function closeSub(sub) {
    const { trigger, content } = subParts(sub);
    if (!trigger || !content) return;
    content.removeAttribute("data-open");
    content.setAttribute("data-closed", "");
    setTransitionAttribute(content, "data-starting-style", false);
    setTransitionAttribute(content, "data-ending-style", true);
    trigger.removeAttribute("data-popup-open");
	trigger.setAttribute("aria-expanded", "false");
    setTimeout(() => {
      if (content.hasAttribute("data-closed")) {
        content.classList.add("hidden");
        setTransitionAttribute(content, "data-ending-style", false);
      }
    }, EXIT_MS);
  }

  // Closes immediately (used when the whole menu goes away).
  function closeSubNow(sub) {
    clearTimeout(sub._tuiOpen);
    clearTimeout(sub._tuiClose);
    sub._tuiOpen = null;
    sub._tuiClose = null;
    const { trigger, content } = subParts(sub);
    if (!trigger || !content) return;
    content.classList.add("hidden");
    content.removeAttribute("data-open");
    content.setAttribute("data-closed", "");
    setTransitionAttribute(content, "data-starting-style", false);
    setTransitionAttribute(content, "data-ending-style", false);
    trigger.removeAttribute("data-popup-open");
	trigger.setAttribute("aria-expanded", "false");
  }

  function requestSubOpenChange(sub, nextOpen, focusFirst) {
	const { trigger, content } = subParts(sub);
	if (!trigger || !content || content.hasAttribute("data-open") === nextOpen) return;
	const accepted = trigger.dispatchEvent(
	  new CustomEvent("dropdownmenu-sub-open-change", {
		bubbles: true,
		cancelable: true,
		detail: { open: nextOpen },
	  }),
	);
	if (!accepted || sub.hasAttribute("data-tui-dropdownmenu-sub-controlled")) return;
	sub.setAttribute("data-tui-dropdownmenu-sub-open", String(nextOpen));
	if (nextOpen) openSub(sub, focusFirst);
	else closeSub(sub);
  }

  function syncSubState(menu) {
	menu.querySelectorAll("[data-tui-dropdownmenu-sub]").forEach((sub) => {
	  const { content } = subParts(sub);
	  if (!content) return;
	  const shouldOpen = sub.getAttribute("data-tui-dropdownmenu-sub-open") === "true";
	  if (shouldOpen && !content.hasAttribute("data-open")) openSub(sub, false);
	  else if (!shouldOpen && content.hasAttribute("data-open")) closeSubNow(sub);
	});
  }

  // Hover intent: while the pointer is over a sub (trigger or its content),
  // keep it open; everything else in the menu schedules its subs to close.
  document.addEventListener("mouseover", (e) => {
    if (!(e.target instanceof Element)) return;
    const menu = e.target.closest("[data-tui-dropdownmenu-content]");
    if (!menu) return;
    const hovered = e.target.closest("[data-tui-dropdownmenu-sub]");

    menu.querySelectorAll("[data-tui-dropdownmenu-sub]").forEach((sub) => {
      const { content } = subParts(sub);
      if (!content) return;
      const isOpen = content.hasAttribute("data-open");
      const onPath = hovered && (sub === hovered || sub.contains(hovered));

      if (onPath) {
        clearTimeout(sub._tuiClose);
        sub._tuiClose = null;
        if (!isOpen && !sub._tuiOpen) {
          sub._tuiOpen = setTimeout(() => {
            sub._tuiOpen = null;
			requestSubOpenChange(sub, true);
          }, SUB_OPEN_DELAY);
        }
      } else {
        clearTimeout(sub._tuiOpen);
        sub._tuiOpen = null;
        if (isOpen && !sub._tuiClose) {
          sub._tuiClose = setTimeout(() => {
            sub._tuiClose = null;
			requestSubOpenChange(sub, false);
          }, SUB_CLOSE_DELAY);
        }
      }
    });
  });

  // The highlight follows the pointer: focus the item under it, fall back to
  // the menu container when the pointer sits on empty menu space.
  document.addEventListener("pointermove", (e) => {
    if (!(e.target instanceof Element)) return;
    const content = e.target.closest("[data-tui-dropdownmenu-content]");
    if (!isOpen(content)) return;
    const item = e.target.closest(ITEM_SELECTOR);
    if (item && containerOf(item)) {
      focusItem(item);
    } else {
      const container = containerOf(e.target) || popupFor(content);
      if (container && !container.contains(document.activeElement)) return;
      if (container && document.activeElement !== container) {
        container.focus({ preventScroll: true });
      }
    }
  });

  // ----- init (portal on open) --------------------

  function init() {
    document.querySelectorAll("[data-tui-dropdownmenu-trigger]").forEach(listenForEscape);
    removeOrphanedContents();
    document.querySelectorAll("[data-tui-dropdownmenu-trigger]").forEach((trigger) => {
      const content = contentFor(trigger);
      if (!content) return;
      if (content.getAttribute("data-tui-dropdownmenu-initial-open") === "true") {
        content.removeAttribute("data-tui-dropdownmenu-initial-open");
        open(content, trigger, false);
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  // Re-init on any childList mutation, directly (never rAF-deferred: rAF
  // does not fire in hidden tabs or throttled iframes): swapped-in markup
  // wires itself, removals release portaled content through the
  // ownership sweep.
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });

  // ----- events ---------------------------------------------------------------

  // Pointer interactions toggle and dismiss on PRESS, exactly like Base UI.
  // Click is never used for open/close, so the stray click the browser fires
  // on <body> after the menu opened over the trigger is naturally harmless.
  function toggle(trigger, focusOn) {
    const content = contentFor(trigger);
    if (!content) return;
    requestOpenChange(content, !isOpen(content), focusOn);
  }

  // The menu-button pattern: ArrowDown opens on the first item, ArrowUp on
  // the last. Only the arrows are taken here — Enter and Space arrive as the
  // detail-0 click a native button synthesises and are handled there, the way
  // useClick and useListNavigation split it in the reference.
  const OPEN_KEYS = { ArrowDown: "first", ArrowUp: "last" };
  document.addEventListener("keydown", (e) => {
    if (!(e.target instanceof Element)) return;
    const focusOn = OPEN_KEYS[e.key];
    if (!focusOn) return;
    const trigger = e.target.closest("[data-tui-dropdownmenu-trigger]");
    if (!trigger || trigger.disabled) return;
    const content = contentFor(trigger);
    // Already open: leave it to the handlers that navigate and close.
    if (!content || isOpen(content)) return;
    e.preventDefault(); // the arrows would otherwise scroll the page
    // Opening consumes the key. Without this the navigation handler below
    // sees the menu already open in the same dispatch and walks the highlight
    // a second time, so ArrowDown would land on the second item.
    e.stopImmediatePropagation();
    // Through requestOpenChange, not open, so a controlled menu still gets to
    // veto the open and the change event still fires.
    trigger._tuiOpenMethod = "keyboard";
    requestOpenChange(content, true, focusOn);
  });

  document.addEventListener("pointerdown", (e) => {
    if (e.button !== 0 || !(e.target instanceof Element)) return;
    const trigger = e.target.closest("[data-tui-dropdownmenu-trigger]");
    if (trigger) {
      trigger._tuiOpenMethod = e.pointerType;
      if (!trigger.disabled) toggle(trigger, false);
      return;
    }
    if (!e.target.closest("[data-tui-dropdownmenu-content]")) requestCloseAll(false);
  });

  document.addEventListener("click", (e) => {
    if (!(e.target instanceof Element)) return;
    const trigger = e.target.closest("[data-tui-dropdownmenu-trigger]");
    if (trigger) {
      // Keyboard activation only (Enter/Space fire a detail-0 click without
      // a preceding pointerdown); pointer presses are handled on pointerdown.
      if (e.detail === 0 && !trigger.disabled) {
        trigger._tuiOpenMethod = "keyboard";
        toggle(trigger, true);
      }
      return;
    }

    // Clicking a submenu trigger opens it right away.
    const subTrigger = e.target.closest("[data-tui-dropdownmenu-sub-trigger]");
    if (subTrigger) {
      const sub = subTrigger.closest("[data-tui-dropdownmenu-sub]");
      if (sub) {
        clearTimeout(sub._tuiOpen);
        sub._tuiOpen = null;
		requestSubOpenChange(sub, true, e.detail === 0);
      }
      return;
    }

    // Checkbox items toggle and keep the menu open.
    const checkbox = e.target.closest("[data-tui-dropdownmenu-checkbox-item]");
    if (checkbox) {
      if (!checkbox.disabled) {
        const on = checkbox.hasAttribute("data-checked");
    const change = new CustomEvent("dropdownmenu-checked-change", {
      bubbles: true,
      cancelable: true,
      detail: { checked: !on },
    });
    const accepted = checkbox.dispatchEvent(change);
    if (accepted && !checkbox.hasAttribute("data-tui-dropdownmenu-checkbox-controlled")) {
      setChecked(checkbox, !on);
    }
      }
      return;
    }

    // Radio items select within their group and keep the menu open.
    const radio = e.target.closest("[data-tui-dropdownmenu-radio-item]");
    if (radio) {
      if (!radio.disabled) {
        const group = radio.closest("[data-tui-dropdownmenu-radio-group]");
    const change = new CustomEvent("dropdownmenu-value-change", {
      bubbles: true,
      cancelable: true,
      detail: { value: radio.getAttribute("data-tui-dropdownmenu-radio-value") },
    });
    const accepted = (group || radio).dispatchEvent(change);
    if (accepted && group && !group.hasAttribute("data-tui-dropdownmenu-radio-controlled")) {
          group.querySelectorAll("[data-tui-dropdownmenu-radio-item]").forEach((r) => {
            setChecked(r, false);
          });
      setChecked(radio, true);
        }
      }
      return;
    }

    const item = e.target.closest("[data-tui-dropdownmenu-item]");
    if (item) {
      if (
        item.getAttribute("aria-disabled") !== "true" &&
        item.getAttribute("data-tui-dropdownmenu-disable-close-on-click") !== "true"
      ) {
        const content = item.closest("[data-tui-dropdownmenu-content]");
        if (content) requestOpenChange(content, false, false, true);
      }
      return;
    }
  });

  document.addEventListener("keydown", (e) => {
    if (closeOnEscapeKeyDown(e)) return;
    const content = anyOpen();
    if (!content) return;

    if (e.key === "Tab") {
      requestCloseAll(false);
      return;
    }

    const active = document.activeElement;
    if (!content.contains(active)) return;
    const container = containerOf(active) || popupFor(content);
    if (!container) return;

    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        moveFocus(container, 1);
        break;
      case "ArrowUp":
        e.preventDefault();
        moveFocus(container, -1);
        break;
      case "Home": {
        e.preventDefault();
        const items = itemsIn(container);
        focusItem(items[0]);
        break;
      }
      case "End": {
        e.preventDefault();
        const items = itemsIn(container);
        focusItem(items[items.length - 1]);
        break;
      }
      case "ArrowRight": {
        const subTrigger = active.closest("[data-tui-dropdownmenu-sub-trigger]");
        if (subTrigger) {
          e.preventDefault();
          const sub = subTrigger.closest("[data-tui-dropdownmenu-sub]");
		  if (sub) requestSubOpenChange(sub, true, true);
        }
        break;
      }
      case "ArrowLeft": {
        const subContent = active.closest("[data-tui-dropdownmenu-sub-content]");
        if (subContent) {
          e.preventDefault();
          const sub = subContent.closest("[data-tui-dropdownmenu-sub]");
          if (sub) {
            const { trigger } = subParts(sub);
			requestSubOpenChange(sub, false);
            if (trigger) trigger.focus({ preventScroll: true });
          }
        }
        break;
      }
    }
  });

})();

// components/floatingui/floating_ui_core.js
// https://cdn.jsdelivr.net/npm/@floating-ui/core@1.7.0
!(function (t, e) {
  "object" == typeof exports && "undefined" != typeof module
    ? e(exports)
    : "function" == typeof define && define.amd
    ? define(["exports"], e)
    : e(
        ((t =
          "undefined" != typeof globalThis
            ? globalThis
            : t || self).FloatingUICore = {})
      );
})(this, function (t) {
  "use strict";
  const e = ["top", "right", "bottom", "left"],
    n = ["start", "end"],
    i = e.reduce((t, e) => t.concat(e, e + "-" + n[0], e + "-" + n[1]), []),
    o = Math.min,
    r = Math.max,
    a = { left: "right", right: "left", bottom: "top", top: "bottom" },
    l = { start: "end", end: "start" };
  function s(t, e, n) {
    return r(t, o(e, n));
  }
  function f(t, e) {
    return "function" == typeof t ? t(e) : t;
  }
  function c(t) {
    return t.split("-")[0];
  }
  function u(t) {
    return t.split("-")[1];
  }
  function m(t) {
    return "x" === t ? "y" : "x";
  }
  function d(t) {
    return "y" === t ? "height" : "width";
  }
  function g(t) {
    return ["top", "bottom"].includes(c(t)) ? "y" : "x";
  }
  function p(t) {
    return m(g(t));
  }
  function h(t, e, n) {
    void 0 === n && (n = !1);
    const i = u(t),
      o = p(t),
      r = d(o);
    let a =
      "x" === o
        ? i === (n ? "end" : "start")
          ? "right"
          : "left"
        : "start" === i
        ? "bottom"
        : "top";
    return e.reference[r] > e.floating[r] && (a = w(a)), [a, w(a)];
  }
  function y(t) {
    return t.replace(/start|end/g, (t) => l[t]);
  }
  function w(t) {
    return t.replace(/left|right|bottom|top/g, (t) => a[t]);
  }
  function x(t) {
    return "number" != typeof t
      ? (function (t) {
          return { top: 0, right: 0, bottom: 0, left: 0, ...t };
        })(t)
      : { top: t, right: t, bottom: t, left: t };
  }
  function v(t) {
    const { x: e, y: n, width: i, height: o } = t;
    return {
      width: i,
      height: o,
      top: n,
      left: e,
      right: e + i,
      bottom: n + o,
      x: e,
      y: n,
    };
  }
  function b(t, e, n) {
    let { reference: i, floating: o } = t;
    const r = g(e),
      a = p(e),
      l = d(a),
      s = c(e),
      f = "y" === r,
      m = i.x + i.width / 2 - o.width / 2,
      h = i.y + i.height / 2 - o.height / 2,
      y = i[l] / 2 - o[l] / 2;
    let w;
    switch (s) {
      case "top":
        w = { x: m, y: i.y - o.height };
        break;
      case "bottom":
        w = { x: m, y: i.y + i.height };
        break;
      case "right":
        w = { x: i.x + i.width, y: h };
        break;
      case "left":
        w = { x: i.x - o.width, y: h };
        break;
      default:
        w = { x: i.x, y: i.y };
    }
    switch (u(e)) {
      case "start":
        w[a] -= y * (n && f ? -1 : 1);
        break;
      case "end":
        w[a] += y * (n && f ? -1 : 1);
    }
    return w;
  }
  async function A(t, e) {
    var n;
    void 0 === e && (e = {});
    const { x: i, y: o, platform: r, rects: a, elements: l, strategy: s } = t,
      {
        boundary: c = "clippingAncestors",
        rootBoundary: u = "viewport",
        elementContext: m = "floating",
        altBoundary: d = !1,
        padding: g = 0,
      } = f(e, t),
      p = x(g),
      h = l[d ? ("floating" === m ? "reference" : "floating") : m],
      y = v(
        await r.getClippingRect({
          element:
            null ==
              (n = await (null == r.isElement ? void 0 : r.isElement(h))) || n
              ? h
              : h.contextElement ||
                (await (null == r.getDocumentElement
                  ? void 0
                  : r.getDocumentElement(l.floating))),
          boundary: c,
          rootBoundary: u,
          strategy: s,
        })
      ),
      w =
        "floating" === m
          ? { x: i, y: o, width: a.floating.width, height: a.floating.height }
          : a.reference,
      b = await (null == r.getOffsetParent
        ? void 0
        : r.getOffsetParent(l.floating)),
      A = ((await (null == r.isElement ? void 0 : r.isElement(b))) &&
        (await (null == r.getScale ? void 0 : r.getScale(b)))) || {
        x: 1,
        y: 1,
      },
      R = v(
        r.convertOffsetParentRelativeRectToViewportRelativeRect
          ? await r.convertOffsetParentRelativeRectToViewportRelativeRect({
              elements: l,
              rect: w,
              offsetParent: b,
              strategy: s,
            })
          : w
      );
    return {
      top: (y.top - R.top + p.top) / A.y,
      bottom: (R.bottom - y.bottom + p.bottom) / A.y,
      left: (y.left - R.left + p.left) / A.x,
      right: (R.right - y.right + p.right) / A.x,
    };
  }
  function R(t, e) {
    return {
      top: t.top - e.height,
      right: t.right - e.width,
      bottom: t.bottom - e.height,
      left: t.left - e.width,
    };
  }
  function P(t) {
    return e.some((e) => t[e] >= 0);
  }
  function D(t) {
    const e = o(...t.map((t) => t.left)),
      n = o(...t.map((t) => t.top));
    return {
      x: e,
      y: n,
      width: r(...t.map((t) => t.right)) - e,
      height: r(...t.map((t) => t.bottom)) - n,
    };
  }
  (t.arrow = (t) => ({
    name: "arrow",
    options: t,
    async fn(e) {
      const {
          x: n,
          y: i,
          placement: r,
          rects: a,
          platform: l,
          elements: c,
          middlewareData: m,
        } = e,
        { element: g, padding: h = 0 } = f(t, e) || {};
      if (null == g) return {};
      const y = x(h),
        w = { x: n, y: i },
        v = p(r),
        b = d(v),
        A = await l.getDimensions(g),
        R = "y" === v,
        P = R ? "top" : "left",
        D = R ? "bottom" : "right",
        T = R ? "clientHeight" : "clientWidth",
        O = a.reference[b] + a.reference[v] - w[v] - a.floating[b],
        E = w[v] - a.reference[v],
        L = await (null == l.getOffsetParent ? void 0 : l.getOffsetParent(g));
      let k = L ? L[T] : 0;
      (k && (await (null == l.isElement ? void 0 : l.isElement(L)))) ||
        (k = c.floating[T] || a.floating[b]);
      const C = O / 2 - E / 2,
        B = k / 2 - A[b] / 2 - 1,
        H = o(y[P], B),
        S = o(y[D], B),
        F = H,
        j = k - A[b] - S,
        z = k / 2 - A[b] / 2 + C,
        M = s(F, z, j),
        V =
          !m.arrow &&
          null != u(r) &&
          z !== M &&
          a.reference[b] / 2 - (z < F ? H : S) - A[b] / 2 < 0,
        W = V ? (z < F ? z - F : z - j) : 0;
      return {
        [v]: w[v] + W,
        data: {
          [v]: M,
          centerOffset: z - M - W,
          ...(V && { alignmentOffset: W }),
        },
        reset: V,
      };
    },
  })),
    (t.autoPlacement = function (t) {
      return (
        void 0 === t && (t = {}),
        {
          name: "autoPlacement",
          options: t,
          async fn(e) {
            var n, o, r;
            const {
                rects: a,
                middlewareData: l,
                placement: s,
                platform: m,
                elements: d,
              } = e,
              {
                crossAxis: g = !1,
                alignment: p,
                allowedPlacements: w = i,
                autoAlignment: x = !0,
                ...v
              } = f(t, e),
              b =
                void 0 !== p || w === i
                  ? (function (t, e, n) {
                      return (
                        t
                          ? [
                              ...n.filter((e) => u(e) === t),
                              ...n.filter((e) => u(e) !== t),
                            ]
                          : n.filter((t) => c(t) === t)
                      ).filter((n) => !t || u(n) === t || (!!e && y(n) !== n));
                    })(p || null, x, w)
                  : w,
              R = await A(e, v),
              P = (null == (n = l.autoPlacement) ? void 0 : n.index) || 0,
              D = b[P];
            if (null == D) return {};
            const T = h(
              D,
              a,
              await (null == m.isRTL ? void 0 : m.isRTL(d.floating))
            );
            if (s !== D) return { reset: { placement: b[0] } };
            const O = [R[c(D)], R[T[0]], R[T[1]]],
              E = [
                ...((null == (o = l.autoPlacement) ? void 0 : o.overflows) ||
                  []),
                { placement: D, overflows: O },
              ],
              L = b[P + 1];
            if (L)
              return {
                data: { index: P + 1, overflows: E },
                reset: { placement: L },
              };
            const k = E.map((t) => {
                const e = u(t.placement);
                return [
                  t.placement,
                  e && g
                    ? t.overflows.slice(0, 2).reduce((t, e) => t + e, 0)
                    : t.overflows[0],
                  t.overflows,
                ];
              }).sort((t, e) => t[1] - e[1]),
              C =
                (null ==
                (r = k.filter((t) =>
                  t[2].slice(0, u(t[0]) ? 2 : 3).every((t) => t <= 0)
                )[0])
                  ? void 0
                  : r[0]) || k[0][0];
            return C !== s
              ? {
                  data: { index: P + 1, overflows: E },
                  reset: { placement: C },
                }
              : {};
          },
        }
      );
    }),
    (t.computePosition = async (t, e, n) => {
      const {
          placement: i = "bottom",
          strategy: o = "absolute",
          middleware: r = [],
          platform: a,
        } = n,
        l = r.filter(Boolean),
        s = await (null == a.isRTL ? void 0 : a.isRTL(e));
      let f = await a.getElementRects({
          reference: t,
          floating: e,
          strategy: o,
        }),
        { x: c, y: u } = b(f, i, s),
        m = i,
        d = {},
        g = 0;
      for (let n = 0; n < l.length; n++) {
        const { name: r, fn: p } = l[n],
          {
            x: h,
            y: y,
            data: w,
            reset: x,
          } = await p({
            x: c,
            y: u,
            initialPlacement: i,
            placement: m,
            strategy: o,
            middlewareData: d,
            rects: f,
            platform: a,
            elements: { reference: t, floating: e },
          });
        (c = null != h ? h : c),
          (u = null != y ? y : u),
          (d = { ...d, [r]: { ...d[r], ...w } }),
          x &&
            g <= 50 &&
            (g++,
            "object" == typeof x &&
              (x.placement && (m = x.placement),
              x.rects &&
                (f =
                  !0 === x.rects
                    ? await a.getElementRects({
                        reference: t,
                        floating: e,
                        strategy: o,
                      })
                    : x.rects),
              ({ x: c, y: u } = b(f, m, s))),
            (n = -1));
      }
      return { x: c, y: u, placement: m, strategy: o, middlewareData: d };
    }),
    (t.detectOverflow = A),
    (t.flip = function (t) {
      return (
        void 0 === t && (t = {}),
        {
          name: "flip",
          options: t,
          async fn(e) {
            var n, i;
            const {
                placement: o,
                middlewareData: r,
                rects: a,
                initialPlacement: l,
                platform: s,
                elements: m,
              } = e,
              {
                mainAxis: d = !0,
                crossAxis: p = !0,
                fallbackPlacements: x,
                fallbackStrategy: v = "bestFit",
                fallbackAxisSideDirection: b = "none",
                flipAlignment: R = !0,
                ...P
              } = f(t, e);
            if (null != (n = r.arrow) && n.alignmentOffset) return {};
            const D = c(o),
              T = g(l),
              O = c(l) === l,
              E = await (null == s.isRTL ? void 0 : s.isRTL(m.floating)),
              L =
                x ||
                (O || !R
                  ? [w(l)]
                  : (function (t) {
                      const e = w(t);
                      return [y(t), e, y(e)];
                    })(l)),
              k = "none" !== b;
            !x &&
              k &&
              L.push(
                ...(function (t, e, n, i) {
                  const o = u(t);
                  let r = (function (t, e, n) {
                    const i = ["left", "right"],
                      o = ["right", "left"],
                      r = ["top", "bottom"],
                      a = ["bottom", "top"];
                    switch (t) {
                      case "top":
                      case "bottom":
                        return n ? (e ? o : i) : e ? i : o;
                      case "left":
                      case "right":
                        return e ? r : a;
                      default:
                        return [];
                    }
                  })(c(t), "start" === n, i);
                  return (
                    o &&
                      ((r = r.map((t) => t + "-" + o)),
                      e && (r = r.concat(r.map(y)))),
                    r
                  );
                })(l, R, b, E)
              );
            const C = [l, ...L],
              B = await A(e, P),
              H = [];
            let S = (null == (i = r.flip) ? void 0 : i.overflows) || [];
            if ((d && H.push(B[D]), p)) {
              const t = h(o, a, E);
              H.push(B[t[0]], B[t[1]]);
            }
            if (
              ((S = [...S, { placement: o, overflows: H }]),
              !H.every((t) => t <= 0))
            ) {
              var F, j;
              const t = ((null == (F = r.flip) ? void 0 : F.index) || 0) + 1,
                e = C[t];
              if (e) {
                var z;
                const n = "alignment" === p && T !== g(e),
                  i = (null == (z = S[0]) ? void 0 : z.overflows[0]) > 0;
                if (!n || i)
                  return {
                    data: { index: t, overflows: S },
                    reset: { placement: e },
                  };
              }
              let n =
                null ==
                (j = S.filter((t) => t.overflows[0] <= 0).sort(
                  (t, e) => t.overflows[1] - e.overflows[1]
                )[0])
                  ? void 0
                  : j.placement;
              if (!n)
                switch (v) {
                  case "bestFit": {
                    var M;
                    const t =
                      null ==
                      (M = S.filter((t) => {
                        if (k) {
                          const e = g(t.placement);
                          return e === T || "y" === e;
                        }
                        return !0;
                      })
                        .map((t) => [
                          t.placement,
                          t.overflows
                            .filter((t) => t > 0)
                            .reduce((t, e) => t + e, 0),
                        ])
                        .sort((t, e) => t[1] - e[1])[0])
                        ? void 0
                        : M[0];
                    t && (n = t);
                    break;
                  }
                  case "initialPlacement":
                    n = l;
                }
              if (o !== n) return { reset: { placement: n } };
            }
            return {};
          },
        }
      );
    }),
    (t.hide = function (t) {
      return (
        void 0 === t && (t = {}),
        {
          name: "hide",
          options: t,
          async fn(e) {
            const { rects: n } = e,
              { strategy: i = "referenceHidden", ...o } = f(t, e);
            switch (i) {
              case "referenceHidden": {
                const t = R(
                  await A(e, { ...o, elementContext: "reference" }),
                  n.reference
                );
                return {
                  data: { referenceHiddenOffsets: t, referenceHidden: P(t) },
                };
              }
              case "escaped": {
                const t = R(await A(e, { ...o, altBoundary: !0 }), n.floating);
                return { data: { escapedOffsets: t, escaped: P(t) } };
              }
              default:
                return {};
            }
          },
        }
      );
    }),
    (t.inline = function (t) {
      return (
        void 0 === t && (t = {}),
        {
          name: "inline",
          options: t,
          async fn(e) {
            const {
                placement: n,
                elements: i,
                rects: a,
                platform: l,
                strategy: s,
              } = e,
              { padding: u = 2, x: m, y: d } = f(t, e),
              p = Array.from(
                (await (null == l.getClientRects
                  ? void 0
                  : l.getClientRects(i.reference))) || []
              ),
              h = (function (t) {
                const e = t.slice().sort((t, e) => t.y - e.y),
                  n = [];
                let i = null;
                for (let t = 0; t < e.length; t++) {
                  const o = e[t];
                  !i || o.y - i.y > i.height / 2
                    ? n.push([o])
                    : n[n.length - 1].push(o),
                    (i = o);
                }
                return n.map((t) => v(D(t)));
              })(p),
              y = v(D(p)),
              w = x(u);
            const b = await l.getElementRects({
              reference: {
                getBoundingClientRect: function () {
                  if (
                    2 === h.length &&
                    h[0].left > h[1].right &&
                    null != m &&
                    null != d
                  )
                    return (
                      h.find(
                        (t) =>
                          m > t.left - w.left &&
                          m < t.right + w.right &&
                          d > t.top - w.top &&
                          d < t.bottom + w.bottom
                      ) || y
                    );
                  if (h.length >= 2) {
                    if ("y" === g(n)) {
                      const t = h[0],
                        e = h[h.length - 1],
                        i = "top" === c(n),
                        o = t.top,
                        r = e.bottom,
                        a = i ? t.left : e.left,
                        l = i ? t.right : e.right;
                      return {
                        top: o,
                        bottom: r,
                        left: a,
                        right: l,
                        width: l - a,
                        height: r - o,
                        x: a,
                        y: o,
                      };
                    }
                    const t = "left" === c(n),
                      e = r(...h.map((t) => t.right)),
                      i = o(...h.map((t) => t.left)),
                      a = h.filter((n) => (t ? n.left === i : n.right === e)),
                      l = a[0].top,
                      s = a[a.length - 1].bottom;
                    return {
                      top: l,
                      bottom: s,
                      left: i,
                      right: e,
                      width: e - i,
                      height: s - l,
                      x: i,
                      y: l,
                    };
                  }
                  return y;
                },
              },
              floating: i.floating,
              strategy: s,
            });
            return a.reference.x !== b.reference.x ||
              a.reference.y !== b.reference.y ||
              a.reference.width !== b.reference.width ||
              a.reference.height !== b.reference.height
              ? { reset: { rects: b } }
              : {};
          },
        }
      );
    }),
    (t.limitShift = function (t) {
      return (
        void 0 === t && (t = {}),
        {
          options: t,
          fn(e) {
            const { x: n, y: i, placement: o, rects: r, middlewareData: a } = e,
              { offset: l = 0, mainAxis: s = !0, crossAxis: u = !0 } = f(t, e),
              d = { x: n, y: i },
              p = g(o),
              h = m(p);
            let y = d[h],
              w = d[p];
            const x = f(l, e),
              v =
                "number" == typeof x
                  ? { mainAxis: x, crossAxis: 0 }
                  : { mainAxis: 0, crossAxis: 0, ...x };
            if (s) {
              const t = "y" === h ? "height" : "width",
                e = r.reference[h] - r.floating[t] + v.mainAxis,
                n = r.reference[h] + r.reference[t] - v.mainAxis;
              y < e ? (y = e) : y > n && (y = n);
            }
            if (u) {
              var b, A;
              const t = "y" === h ? "width" : "height",
                e = ["top", "left"].includes(c(o)),
                n =
                  r.reference[p] -
                  r.floating[t] +
                  ((e && (null == (b = a.offset) ? void 0 : b[p])) || 0) +
                  (e ? 0 : v.crossAxis),
                i =
                  r.reference[p] +
                  r.reference[t] +
                  (e ? 0 : (null == (A = a.offset) ? void 0 : A[p]) || 0) -
                  (e ? v.crossAxis : 0);
              w < n ? (w = n) : w > i && (w = i);
            }
            return { [h]: y, [p]: w };
          },
        }
      );
    }),
    (t.offset = function (t) {
      return (
        void 0 === t && (t = 0),
        {
          name: "offset",
          options: t,
          async fn(e) {
            var n, i;
            const { x: o, y: r, placement: a, middlewareData: l } = e,
              s = await (async function (t, e) {
                const { placement: n, platform: i, elements: o } = t,
                  r = await (null == i.isRTL ? void 0 : i.isRTL(o.floating)),
                  a = c(n),
                  l = u(n),
                  s = "y" === g(n),
                  m = ["left", "top"].includes(a) ? -1 : 1,
                  d = r && s ? -1 : 1,
                  p = f(e, t);
                let {
                  mainAxis: h,
                  crossAxis: y,
                  alignmentAxis: w,
                } = "number" == typeof p
                  ? { mainAxis: p, crossAxis: 0, alignmentAxis: null }
                  : {
                      mainAxis: p.mainAxis || 0,
                      crossAxis: p.crossAxis || 0,
                      alignmentAxis: p.alignmentAxis,
                    };
                return (
                  l && "number" == typeof w && (y = "end" === l ? -1 * w : w),
                  s ? { x: y * d, y: h * m } : { x: h * m, y: y * d }
                );
              })(e, t);
            return a === (null == (n = l.offset) ? void 0 : n.placement) &&
              null != (i = l.arrow) &&
              i.alignmentOffset
              ? {}
              : { x: o + s.x, y: r + s.y, data: { ...s, placement: a } };
          },
        }
      );
    }),
    (t.rectToClientRect = v),
    (t.shift = function (t) {
      return (
        void 0 === t && (t = {}),
        {
          name: "shift",
          options: t,
          async fn(e) {
            const { x: n, y: i, placement: o } = e,
              {
                mainAxis: r = !0,
                crossAxis: a = !1,
                limiter: l = {
                  fn: (t) => {
                    let { x: e, y: n } = t;
                    return { x: e, y: n };
                  },
                },
                ...u
              } = f(t, e),
              d = { x: n, y: i },
              p = await A(e, u),
              h = g(c(o)),
              y = m(h);
            let w = d[y],
              x = d[h];
            if (r) {
              const t = "y" === y ? "bottom" : "right";
              w = s(w + p["y" === y ? "top" : "left"], w, w - p[t]);
            }
            if (a) {
              const t = "y" === h ? "bottom" : "right";
              x = s(x + p["y" === h ? "top" : "left"], x, x - p[t]);
            }
            const v = l.fn({ ...e, [y]: w, [h]: x });
            return {
              ...v,
              data: { x: v.x - n, y: v.y - i, enabled: { [y]: r, [h]: a } },
            };
          },
        }
      );
    }),
    (t.size = function (t) {
      return (
        void 0 === t && (t = {}),
        {
          name: "size",
          options: t,
          async fn(e) {
            var n, i;
            const { placement: a, rects: l, platform: s, elements: m } = e,
              { apply: d = () => {}, ...p } = f(t, e),
              h = await A(e, p),
              y = c(a),
              w = u(a),
              x = "y" === g(a),
              { width: v, height: b } = l.floating;
            let R, P;
            "top" === y || "bottom" === y
              ? ((R = y),
                (P =
                  w ===
                  ((await (null == s.isRTL ? void 0 : s.isRTL(m.floating)))
                    ? "start"
                    : "end")
                    ? "left"
                    : "right"))
              : ((P = y), (R = "end" === w ? "top" : "bottom"));
            const D = b - h.top - h.bottom,
              T = v - h.left - h.right,
              O = o(b - h[R], D),
              E = o(v - h[P], T),
              L = !e.middlewareData.shift;
            let k = O,
              C = E;
            if (
              (null != (n = e.middlewareData.shift) && n.enabled.x && (C = T),
              null != (i = e.middlewareData.shift) && i.enabled.y && (k = D),
              L && !w)
            ) {
              const t = r(h.left, 0),
                e = r(h.right, 0),
                n = r(h.top, 0),
                i = r(h.bottom, 0);
              x
                ? (C =
                    v - 2 * (0 !== t || 0 !== e ? t + e : r(h.left, h.right)))
                : (k =
                    b - 2 * (0 !== n || 0 !== i ? n + i : r(h.top, h.bottom)));
            }
            await d({ ...e, availableWidth: C, availableHeight: k });
            const B = await s.getDimensions(m.floating);
            return v !== B.width || b !== B.height
              ? { reset: { rects: !0 } }
              : {};
          },
        }
      );
    });
});

// components/floatingui/floating_ui_dom.js
// https://cdn.jsdelivr.net/npm/@floating-ui/dom@1.7.0
!(function (t, e) {
  "object" == typeof exports && "undefined" != typeof module
    ? e(exports, require("./floating_ui_core"))
    : "function" == typeof define && define.amd
    ? define(["exports", "./floatingUICore"], e)
    : e(
        ((t =
          "undefined" != typeof globalThis
            ? globalThis
            : t || self).FloatingUIDOM = {}),
        t.FloatingUICore
      );
})(this, function (t, e) {
  "use strict";
  const n = Math.min,
    o = Math.max,
    i = Math.round,
    r = Math.floor,
    c = (t) => ({ x: t, y: t });
  function l() {
    return "undefined" != typeof window;
  }
  function s(t) {
    return a(t) ? (t.nodeName || "").toLowerCase() : "#document";
  }
  function f(t) {
    var e;
    return (
      (null == t || null == (e = t.ownerDocument) ? void 0 : e.defaultView) ||
      window
    );
  }
  function u(t) {
    var e;
    return null ==
      (e = (a(t) ? t.ownerDocument : t.document) || window.document)
      ? void 0
      : e.documentElement;
  }
  function a(t) {
    return !!l() && (t instanceof Node || t instanceof f(t).Node);
  }
  function d(t) {
    return !!l() && (t instanceof Element || t instanceof f(t).Element);
  }
  function h(t) {
    return !!l() && (t instanceof HTMLElement || t instanceof f(t).HTMLElement);
  }
  function p(t) {
    return (
      !(!l() || "undefined" == typeof ShadowRoot) &&
      (t instanceof ShadowRoot || t instanceof f(t).ShadowRoot)
    );
  }
  function g(t) {
    const { overflow: e, overflowX: n, overflowY: o, display: i } = b(t);
    return (
      /auto|scroll|overlay|hidden|clip/.test(e + o + n) &&
      !["inline", "contents"].includes(i)
    );
  }
  function m(t) {
    return ["table", "td", "th"].includes(s(t));
  }
  function y(t) {
    return [":popover-open", ":modal"].some((e) => {
      try {
        return t.matches(e);
      } catch (t) {
        return !1;
      }
    });
  }
  function w(t) {
    const e = x(),
      n = d(t) ? b(t) : t;
    return (
      ["transform", "translate", "scale", "rotate", "perspective"].some(
        (t) => !!n[t] && "none" !== n[t]
      ) ||
      (!!n.containerType && "normal" !== n.containerType) ||
      (!e && !!n.backdropFilter && "none" !== n.backdropFilter) ||
      (!e && !!n.filter && "none" !== n.filter) ||
      [
        "transform",
        "translate",
        "scale",
        "rotate",
        "perspective",
        "filter",
      ].some((t) => (n.willChange || "").includes(t)) ||
      ["paint", "layout", "strict", "content"].some((t) =>
        (n.contain || "").includes(t)
      )
    );
  }
  function x() {
    return (
      !("undefined" == typeof CSS || !CSS.supports) &&
      CSS.supports("-webkit-backdrop-filter", "none")
    );
  }
  function v(t) {
    return ["html", "body", "#document"].includes(s(t));
  }
  function b(t) {
    return f(t).getComputedStyle(t);
  }
  function T(t) {
    return d(t)
      ? { scrollLeft: t.scrollLeft, scrollTop: t.scrollTop }
      : { scrollLeft: t.scrollX, scrollTop: t.scrollY };
  }
  function L(t) {
    if ("html" === s(t)) return t;
    const e = t.assignedSlot || t.parentNode || (p(t) && t.host) || u(t);
    return p(e) ? e.host : e;
  }
  function R(t) {
    const e = L(t);
    return v(e)
      ? t.ownerDocument
        ? t.ownerDocument.body
        : t.body
      : h(e) && g(e)
      ? e
      : R(e);
  }
  function C(t, e, n) {
    var o;
    void 0 === e && (e = []), void 0 === n && (n = !0);
    const i = R(t),
      r = i === (null == (o = t.ownerDocument) ? void 0 : o.body),
      c = f(i);
    if (r) {
      const t = E(c);
      return e.concat(
        c,
        c.visualViewport || [],
        g(i) ? i : [],
        t && n ? C(t) : []
      );
    }
    return e.concat(i, C(i, [], n));
  }
  function E(t) {
    return t.parent && Object.getPrototypeOf(t.parent) ? t.frameElement : null;
  }
  function S(t) {
    const e = b(t);
    let n = parseFloat(e.width) || 0,
      o = parseFloat(e.height) || 0;
    const r = h(t),
      c = r ? t.offsetWidth : n,
      l = r ? t.offsetHeight : o,
      s = i(n) !== c || i(o) !== l;
    return s && ((n = c), (o = l)), { width: n, height: o, $: s };
  }
  function F(t) {
    return d(t) ? t : t.contextElement;
  }
  function O(t) {
    const e = F(t);
    if (!h(e)) return c(1);
    const n = e.getBoundingClientRect(),
      { width: o, height: r, $: l } = S(e);
    let s = (l ? i(n.width) : n.width) / o,
      f = (l ? i(n.height) : n.height) / r;
    return (
      (s && Number.isFinite(s)) || (s = 1),
      (f && Number.isFinite(f)) || (f = 1),
      { x: s, y: f }
    );
  }
  const D = c(0);
  function H(t) {
    const e = f(t);
    return x() && e.visualViewport
      ? { x: e.visualViewport.offsetLeft, y: e.visualViewport.offsetTop }
      : D;
  }
  function P(t, n, o, i) {
    void 0 === n && (n = !1), void 0 === o && (o = !1);
    const r = t.getBoundingClientRect(),
      l = F(t);
    let s = c(1);
    n && (i ? d(i) && (s = O(i)) : (s = O(t)));
    const u = (function (t, e, n) {
      return void 0 === e && (e = !1), !(!n || (e && n !== f(t))) && e;
    })(l, o, i)
      ? H(l)
      : c(0);
    let a = (r.left + u.x) / s.x,
      h = (r.top + u.y) / s.y,
      p = r.width / s.x,
      g = r.height / s.y;
    if (l) {
      const t = f(l),
        e = i && d(i) ? f(i) : i;
      let n = t,
        o = E(n);
      for (; o && i && e !== n; ) {
        const t = O(o),
          e = o.getBoundingClientRect(),
          i = b(o),
          r = e.left + (o.clientLeft + parseFloat(i.paddingLeft)) * t.x,
          c = e.top + (o.clientTop + parseFloat(i.paddingTop)) * t.y;
        (a *= t.x),
          (h *= t.y),
          (p *= t.x),
          (g *= t.y),
          (a += r),
          (h += c),
          (n = f(o)),
          (o = E(n));
      }
    }
    return e.rectToClientRect({ width: p, height: g, x: a, y: h });
  }
  function W(t, e) {
    const n = T(t).scrollLeft;
    return e ? e.left + n : P(u(t)).left + n;
  }
  function M(t, e, n) {
    void 0 === n && (n = !1);
    const o = t.getBoundingClientRect();
    return {
      x: o.left + e.scrollLeft - (n ? 0 : W(t, o)),
      y: o.top + e.scrollTop,
    };
  }
  function z(t, n, i) {
    let r;
    if ("viewport" === n)
      r = (function (t, e) {
        const n = f(t),
          o = u(t),
          i = n.visualViewport;
        let r = o.clientWidth,
          c = o.clientHeight,
          l = 0,
          s = 0;
        if (i) {
          (r = i.width), (c = i.height);
          const t = x();
          (!t || (t && "fixed" === e)) &&
            ((l = i.offsetLeft), (s = i.offsetTop));
        }
        return { width: r, height: c, x: l, y: s };
      })(t, i);
    else if ("document" === n)
      r = (function (t) {
        const e = u(t),
          n = T(t),
          i = t.ownerDocument.body,
          r = o(e.scrollWidth, e.clientWidth, i.scrollWidth, i.clientWidth),
          c = o(e.scrollHeight, e.clientHeight, i.scrollHeight, i.clientHeight);
        let l = -n.scrollLeft + W(t);
        const s = -n.scrollTop;
        return (
          "rtl" === b(i).direction &&
            (l += o(e.clientWidth, i.clientWidth) - r),
          { width: r, height: c, x: l, y: s }
        );
      })(u(t));
    else if (d(n))
      r = (function (t, e) {
        const n = P(t, !0, "fixed" === e),
          o = n.top + t.clientTop,
          i = n.left + t.clientLeft,
          r = h(t) ? O(t) : c(1);
        return {
          width: t.clientWidth * r.x,
          height: t.clientHeight * r.y,
          x: i * r.x,
          y: o * r.y,
        };
      })(n, i);
    else {
      const e = H(t);
      r = { x: n.x - e.x, y: n.y - e.y, width: n.width, height: n.height };
    }
    return e.rectToClientRect(r);
  }
  function A(t, e) {
    const n = L(t);
    return (
      !(n === e || !d(n) || v(n)) && ("fixed" === b(n).position || A(n, e))
    );
  }
  function B(t, e, n) {
    const o = h(e),
      i = u(e),
      r = "fixed" === n,
      l = P(t, !0, r, e);
    let f = { scrollLeft: 0, scrollTop: 0 };
    const a = c(0);
    function d() {
      a.x = W(i);
    }
    if (o || (!o && !r))
      if ((("body" !== s(e) || g(i)) && (f = T(e)), o)) {
        const t = P(e, !0, r, e);
        (a.x = t.x + e.clientLeft), (a.y = t.y + e.clientTop);
      } else i && d();
    r && !o && i && d();
    const p = !i || o || r ? c(0) : M(i, f);
    return {
      x: l.left + f.scrollLeft - a.x - p.x,
      y: l.top + f.scrollTop - a.y - p.y,
      width: l.width,
      height: l.height,
    };
  }
  function V(t) {
    return "static" === b(t).position;
  }
  function N(t, e) {
    if (!h(t) || "fixed" === b(t).position) return null;
    if (e) return e(t);
    let n = t.offsetParent;
    return u(t) === n && (n = n.ownerDocument.body), n;
  }
  function I(t, e) {
    const n = f(t);
    if (y(t)) return n;
    if (!h(t)) {
      let e = L(t);
      for (; e && !v(e); ) {
        if (d(e) && !V(e)) return e;
        e = L(e);
      }
      return n;
    }
    let o = N(t, e);
    for (; o && m(o) && V(o); ) o = N(o, e);
    return o && v(o) && V(o) && !w(o)
      ? n
      : o ||
          (function (t) {
            let e = L(t);
            for (; h(e) && !v(e); ) {
              if (w(e)) return e;
              if (y(e)) return null;
              e = L(e);
            }
            return null;
          })(t) ||
          n;
  }
  const k = {
    convertOffsetParentRelativeRectToViewportRelativeRect: function (t) {
      let { elements: e, rect: n, offsetParent: o, strategy: i } = t;
      const r = "fixed" === i,
        l = u(o),
        f = !!e && y(e.floating);
      if (o === l || (f && r)) return n;
      let a = { scrollLeft: 0, scrollTop: 0 },
        d = c(1);
      const p = c(0),
        m = h(o);
      if (
        (m || (!m && !r)) &&
        (("body" !== s(o) || g(l)) && (a = T(o)), h(o))
      ) {
        const t = P(o);
        (d = O(o)), (p.x = t.x + o.clientLeft), (p.y = t.y + o.clientTop);
      }
      const w = !l || m || r ? c(0) : M(l, a, !0);
      return {
        width: n.width * d.x,
        height: n.height * d.y,
        x: n.x * d.x - a.scrollLeft * d.x + p.x + w.x,
        y: n.y * d.y - a.scrollTop * d.y + p.y + w.y,
      };
    },
    getDocumentElement: u,
    getClippingRect: function (t) {
      let { element: e, boundary: i, rootBoundary: r, strategy: c } = t;
      const l = [
          ...("clippingAncestors" === i
            ? y(e)
              ? []
              : (function (t, e) {
                  const n = e.get(t);
                  if (n) return n;
                  let o = C(t, [], !1).filter((t) => d(t) && "body" !== s(t)),
                    i = null;
                  const r = "fixed" === b(t).position;
                  let c = r ? L(t) : t;
                  for (; d(c) && !v(c); ) {
                    const e = b(c),
                      n = w(c);
                    n || "fixed" !== e.position || (i = null),
                      (
                        r
                          ? !n && !i
                          : (!n &&
                              "static" === e.position &&
                              i &&
                              ["absolute", "fixed"].includes(i.position)) ||
                            (g(c) && !n && A(t, c))
                      )
                        ? (o = o.filter((t) => t !== c))
                        : (i = e),
                      (c = L(c));
                  }
                  return e.set(t, o), o;
                })(e, this._c)
            : [].concat(i)),
          r,
        ],
        f = l[0],
        u = l.reduce((t, i) => {
          const r = z(e, i, c);
          return (
            (t.top = o(r.top, t.top)),
            (t.right = n(r.right, t.right)),
            (t.bottom = n(r.bottom, t.bottom)),
            (t.left = o(r.left, t.left)),
            t
          );
        }, z(e, f, c));
      return {
        width: u.right - u.left,
        height: u.bottom - u.top,
        x: u.left,
        y: u.top,
      };
    },
    getOffsetParent: I,
    getElementRects: async function (t) {
      const e = this.getOffsetParent || I,
        n = this.getDimensions,
        o = await n(t.floating);
      return {
        reference: B(t.reference, await e(t.floating), t.strategy),
        floating: { x: 0, y: 0, width: o.width, height: o.height },
      };
    },
    getClientRects: function (t) {
      return Array.from(t.getClientRects());
    },
    getDimensions: function (t) {
      const { width: e, height: n } = S(t);
      return { width: e, height: n };
    },
    getScale: O,
    isElement: d,
    isRTL: function (t) {
      return "rtl" === b(t).direction;
    },
  };
  function q(t, e) {
    return (
      t.x === e.x && t.y === e.y && t.width === e.width && t.height === e.height
    );
  }
  const U = e.detectOverflow,
    j = e.offset,
    X = e.autoPlacement,
    Y = e.shift,
    $ = e.flip,
    _ = e.size,
    G = e.hide,
    J = e.arrow,
    K = e.inline,
    Q = e.limitShift;
  (t.arrow = J),
    (t.autoPlacement = X),
    (t.autoUpdate = function (t, e, i, c) {
      void 0 === c && (c = {});
      const {
          ancestorScroll: l = !0,
          ancestorResize: s = !0,
          elementResize: f = "function" == typeof ResizeObserver,
          layoutShift: a = "function" == typeof IntersectionObserver,
          animationFrame: d = !1,
        } = c,
        h = F(t),
        p = l || s ? [...(h ? C(h) : []), ...C(e)] : [];
      p.forEach((t) => {
        l && t.addEventListener("scroll", i, { passive: !0 }),
          s && t.addEventListener("resize", i);
      });
      const g =
        h && a
          ? (function (t, e) {
              let i,
                c = null;
              const l = u(t);
              function s() {
                var t;
                clearTimeout(i), null == (t = c) || t.disconnect(), (c = null);
              }
              return (
                (function f(u, a) {
                  void 0 === u && (u = !1), void 0 === a && (a = 1), s();
                  const d = t.getBoundingClientRect(),
                    { left: h, top: p, width: g, height: m } = d;
                  if ((u || e(), !g || !m)) return;
                  const y = {
                    rootMargin:
                      -r(p) +
                      "px " +
                      -r(l.clientWidth - (h + g)) +
                      "px " +
                      -r(l.clientHeight - (p + m)) +
                      "px " +
                      -r(h) +
                      "px",
                    threshold: o(0, n(1, a)) || 1,
                  };
                  let w = !0;
                  function x(e) {
                    const n = e[0].intersectionRatio;
                    if (n !== a) {
                      if (!w) return f();
                      n
                        ? f(!1, n)
                        : (i = setTimeout(() => {
                            f(!1, 1e-7);
                          }, 1e3));
                    }
                    1 !== n || q(d, t.getBoundingClientRect()) || f(), (w = !1);
                  }
                  try {
                    c = new IntersectionObserver(x, {
                      ...y,
                      root: l.ownerDocument,
                    });
                  } catch (t) {
                    c = new IntersectionObserver(x, y);
                  }
                  c.observe(t);
                })(!0),
                s
              );
            })(h, i)
          : null;
      let m,
        y = -1,
        w = null;
      f &&
        ((w = new ResizeObserver((t) => {
          let [n] = t;
          n &&
            n.target === h &&
            w &&
            (w.unobserve(e),
            cancelAnimationFrame(y),
            (y = requestAnimationFrame(() => {
              var t;
              null == (t = w) || t.observe(e);
            }))),
            i();
        })),
        h && !d && w.observe(h),
        w.observe(e));
      let x = d ? P(t) : null;
      return (
        d &&
          (function e() {
            const n = P(t);
            x && !q(x, n) && i();
            (x = n), (m = requestAnimationFrame(e));
          })(),
        i(),
        () => {
          var t;
          p.forEach((t) => {
            l && t.removeEventListener("scroll", i),
              s && t.removeEventListener("resize", i);
          }),
            null == g || g(),
            null == (t = w) || t.disconnect(),
            (w = null),
            d && cancelAnimationFrame(m);
        }
      );
    }),
    (t.computePosition = (t, n, o) => {
      const i = new Map(),
        r = { platform: k, ...o },
        c = { ...r.platform, _c: i };
      return e.computePosition(t, n, { ...r, platform: c });
    }),
    (t.detectOverflow = U),
    (t.flip = $),
    (t.getOverflowAncestors = C),
    (t.hide = G),
    (t.inline = K),
    (t.limitShift = Q),
    (t.offset = j),
    (t.platform = k),
    (t.shift = Y),
    (t.size = _);

  // We put this manually here because we need to make sure it's available
  // before the popover component is initialized.
  window.FloatingUIDOM = t;
  return t;
});

// components/navigationmenu/navigationmenu.js
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

// components/scrollarea/scrollarea.js
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

// components/select/select.js
// Uses window.FloatingUIDOM from components/floatingui (loaded in the same bundle).
(function () {
  // Constants from Base UI's select, shadcn's reference implementation.
  const EXIT_MS = 120; // popper exit animation (duration-100) + slack
  const SIDE_OFFSET = 4;
  const COLLISION_PADDING = 5;
  const MARGIN = 10; // aligned mode: minimum distance to the viewport edges
  const MIN_HEIGHT = 100; // less room than this -> fall back to popper
  const TRIGGER_COLLISION = 20; // trigger this close to an edge -> popper
  const TOL = 1; // scroll edge tolerance
  const ARROW_TICK_MS = 40; // hovering a scroll arrow scrolls one item per tick
  const SELECTED_DELAY = 400; // mouseup selection stays disabled this long after open

  const escapeTargets = new WeakSet();
  function listenForEscape(element) {
    if (!element || escapeTargets.has(element)) return;
    element.addEventListener("keydown", closeOnEscapeKeyDown);
    escapeTargets.add(element);
  }

  // useDismiss: popup/reference listeners stop Escape before outer document handlers.
  function closeOnEscapeKeyDown(event) {
    if (event.key !== "Escape") return;
    const contents = event.currentTarget === document
      ? allContents()
      : [event.currentTarget.hasAttribute("data-tui-select-content")
        ? event.currentTarget
        : contentFor(event.currentTarget)];
    let handled = false;
    for (const content of contents) {
      if (!content?.hasAttribute("data-open")) continue;
      const trigger = triggerFor(content);
      if (requestOpenChange(content, false)) event.preventDefault();
      if (trigger) trigger.focus();
      event.stopPropagation();
      handled = true;
    }
    return handled;
  }

  function allContents() {
    return document.querySelectorAll("[data-tui-select-content]");
  }

  function triggerFor(content) {
    return document.querySelector(
      '[data-tui-select-trigger][aria-controls="' + content.id + '"]',
    );
  }

  function contentFor(trigger) {
    return document.getElementById(trigger.getAttribute("aria-controls"));
  }

  // The hidden form input sits right before the trigger button.
  function inputFor(trigger) {
    const prev = trigger.previousElementSibling;
    return prev && prev.hasAttribute("data-tui-select-input") ? prev : null;
  }

  // Focus waits until after the input task:
  // Chromium's mousedown default focuses the trigger, WebKit's clears focus.
  // One frame, like Base UI, with a guard for a popup that closed meanwhile.
  function enqueueFocus(el, shouldFocus) {
    if (!el) return;
    requestAnimationFrame(() => {
      if (shouldFocus && !shouldFocus()) return;
      el.focus({ preventScroll: true });
    });
  }

  function valueSpanFor(trigger) {
    return trigger.querySelector("[data-tui-select-value]");
  }

  function popupFor(content) {
    return content.querySelector("[data-tui-select-popup]");
  }

  function viewportFor(content) {
    return content.querySelector("[data-tui-select-viewport]");
  }

  function clamp(value, min, max) {
    return Math.min(Math.max(value, min), max);
  }

  function maxScrollTop(el) {
    return Math.max(0, el.scrollHeight - el.clientHeight);
  }

  function isAlignMode(content) {
    return !content.hasAttribute("data-tui-select-disable-align-item-with-trigger");
  }

  function setState(content, state) {
    const open = state === "open";
    content.toggleAttribute("data-open", open);
    content.toggleAttribute("data-closed", !open);
    const popup = popupFor(content);
    if (popup) {
      popup.toggleAttribute("data-open", open);
      popup.toggleAttribute("data-closed", !open);
    }
  }

  function isOpen(content) {
    return !!content && content.hasAttribute("data-open");
  }

  function setTransitionAttribute(content, name, present) {
    content.toggleAttribute(name, present);
    const popup = popupFor(content);
    if (popup) popup.toggleAttribute(name, present);
  }

  function startTransition(content) {
    setTransitionAttribute(content, "data-ending-style", false);
    setTransitionAttribute(content, "data-starting-style", true);
    requestAnimationFrame(() => {
      requestAnimationFrame(() => setTransitionAttribute(content, "data-starting-style", false));
    });
  }

  function setSide(content, side) {
    content.setAttribute("data-side", side);
    const popup = popupFor(content);
    if (popup) popup.setAttribute("data-side", side);
    const trigger = triggerFor(content);
    if (trigger) trigger.setAttribute("data-popup-side", side);
  }

  // Base UI zooms the popup out of the anchor's center point (e.g.
  // "96px -4px"), not out of a placement corner.
  function anchorOrigin(result, anchorRect, positionerRect) {
    const side = result.placement.split("-")[0];
    const centerX = anchorRect.left + anchorRect.width / 2 - positionerRect.left + "px";
    const centerY = anchorRect.top + anchorRect.height / 2 - positionerRect.top + "px";
    if (side === "bottom") return centerX + " " + -SIDE_OFFSET + "px";
    if (side === "top") return centerX + " calc(100% + " + SIDE_OFFSET + "px)";
    if (side === "right") return -SIDE_OFFSET + "px " + centerY;
    return "calc(100% + " + SIDE_OFFSET + "px) " + centerY;
  }

  // Moves the content to <body> (shadcn portals it the same way).
  // The unmount half of the React portal pendant: a portaled content lives
  // as long as its SSR declaration site (_tuiPortalOwner) stays in the
  // document. Trigger-presence heuristics judged mid-swap moments wrongly -
  // multi-phase swap layers briefly disconnect the new triggers.
  function removeOrphanedContents(content) {
    document.querySelectorAll("body > [data-tui-select-content]").forEach((c) => {
      if (c !== content && c._tuiPortalOwner && !c._tuiPortalOwner.isConnected) {
        stopAutoPositioning(c);
        c._tuiReleaseScroll?.();
        c._tuiReleaseScroll = null;
        c.remove();
      }
    });
  }

  function portal(content) {
    listenForEscape(content);
    removeOrphanedContents(content);
    if (content.parentElement !== document.body) {
      if (!content._tuiPortalOwner) content._tuiPortalOwner = content.parentElement;
      document.body.appendChild(content);
    }
  }

  // Clears everything a previous open left behind on the positioner and popup.
  function resetInlineStyles(content) {
    ["left", "right", "top", "bottom", "height", "maxHeight", "marginTop", "marginBottom"].forEach(
      (prop) => (content.style[prop] = ""),
    );
    const popup = popupFor(content);
    if (popup) popup.style.height = "";
  }

  // Regular anchored placement below/above the trigger (Base UI's positioner).
  function positionPopper(content, trigger, strategy) {
    const { computePosition, offset, flip, shift, size } = window.FloatingUIDOM;
    const align = content.getAttribute("data-tui-select-align") || "center";
    const placement = align === "center" ? "bottom" : "bottom-" + align;

    content.style.position = strategy;

    return computePosition(trigger, content, {
      placement: placement,
      strategy: strategy,
      middleware: [
        offset(SIDE_OFFSET),
        flip({ padding: COLLISION_PADDING }),
        shift({ padding: COLLISION_PADDING }),
        size({
          padding: COLLISION_PADDING,
          apply(args) {
            content.style.setProperty(
              "--available-height",
              args.availableHeight + "px",
            );
            content.style.setProperty(
              "--anchor-width",
              args.rects.reference.width + "px",
            );
          },
        }),
      ],
    }).then((result) => {
      content.style.left = result.x + "px";
      content.style.top = result.y + "px";
      setSide(content, result.placement.split("-")[0]);
      const popup = popupFor(content);
      if (popup) {
        popup.style.setProperty(
          "--transform-origin",
          anchorOrigin(result, trigger.getBoundingClientRect(), content.getBoundingClientRect()),
        );
      }
    });
  }

  // Overlays the menu so the selected item sits on the trigger with its text
  // aligned to the trigger text. Port of Base UI's SelectPopup align logic.
  // Runs after the popper pass (which sets the CSS vars and fallback coords);
  // returns false when Base UI would fall back to popper positioning.
  function positionAligned(content, trigger) {
    const popup = popupFor(content);
    const viewport = viewportFor(content);
    const valueEl = valueSpanFor(trigger);
    const textEl =
      content.querySelector('[data-tui-select-item][data-selected] [data-tui-select-item-text]') ||
      content.querySelector("[data-tui-select-item] [data-tui-select-item-text]");

    const docEl = document.documentElement;
    const triggerRect = trigger.getBoundingClientRect();
    const positionerRect = content.getBoundingClientRect(); // natural size from the popper pass
    // The list's natural height, measured before the aligned styles stretch
    // the popup (scrollHeight can never report less than the client height).
    const naturalScrollHeight = viewport.scrollHeight;
    const popupStyles = window.getComputedStyle(popup);
    const borderBottom = parseFloat(popupStyles.borderBottomWidth) || 0;
    const maxPopupHeight = parseFloat(popupStyles.maxHeight) || Infinity;
    const viewportHeight = docEl.clientHeight - MARGIN * 2;
    const viewportWidth = docEl.clientWidth;
    const availableSpaceBeneathTrigger = viewportHeight - triggerRect.bottom + triggerRect.height;

    let alignedLeft = triggerRect.left;
    let offsetY = 0;
    let textRect = null;
    if (textEl && valueEl) {
      const valueRect = valueEl.getBoundingClientRect();
      textRect = textEl.getBoundingClientRect();
      alignedLeft = positionerRect.left + (valueRect.left - textRect.left);
      offsetY =
        textRect.top - positionerRect.top + textRect.height / 2 -
        (valueRect.top - triggerRect.top + valueRect.height / 2);
    }

    const idealHeight = availableSpaceBeneathTrigger + offsetY + MARGIN + borderBottom;
    let height = Math.min(viewportHeight, idealHeight);
    const maxHeight = viewportHeight - MARGIN * 2;
    const scrollTop = idealHeight - height;

    content.style.left =
      clamp(
        alignedLeft,
        COLLISION_PADDING,
        Math.max(COLLISION_PADDING, viewportWidth - COLLISION_PADDING - positionerRect.width),
      ) + "px";
    content.style.height = height + "px";
    content.style.maxHeight = "none";
    content.style.marginTop = MARGIN + "px";
    content.style.marginBottom = MARGIN + "px";
    popup.style.height = "100%";

    const max = maxScrollTop(viewport);
    const isTopPositioned = scrollTop >= max - TOL;

    if (isTopPositioned) {
      height = Math.min(viewportHeight, positionerRect.height) - (scrollTop - max);
    }

    if (
      triggerRect.top < TRIGGER_COLLISION ||
      triggerRect.bottom > viewportHeight - TRIGGER_COLLISION ||
      Math.ceil(height) + TOL < Math.min(naturalScrollHeight, MIN_HEIGHT)
    ) {
      return false;
    }

    content._tuiReachedMax = false;

    if (isTopPositioned) {
      const topOffset = Math.max(0, viewportHeight - idealHeight);
      content.style.top = (positionerRect.height >= maxHeight ? 0 : topOffset) + "px";
      content.style.height = height + "px";
      viewport.scrollTop = maxScrollTop(viewport);
    } else {
      content.style.top = "auto";
      content.style.bottom = "0px";
      viewport.scrollTop = scrollTop;
    }

    if (textRect) {
      const clampedY = clamp(
        positionerRect.height > 0
          ? ((textRect.top + textRect.height / 2 - positionerRect.top) / positionerRect.height) * 100
          : 50,
        0,
        100,
      );
      popup.style.setProperty("--transform-origin", "50% " + clampedY + "%");
    }

    setSide(content, "none");
    if (height >= viewportHeight || height >= maxPopupHeight) {
      content._tuiReachedMax = true;
    }
    return true;
  }

  function position(content, trigger) {
    const popup = popupFor(content);
    const viewport = viewportFor(content);
    if (!popup || !viewport) return Promise.resolve();
    // Base UI uses viewport positioning while the selected item is aligned
    // with the trigger. Touch and regular popper positioning use Floating
    // UI's standard absolute positioning instead.
    const alignMode = isAlignMode(content) && content._tuiOpenMethod !== "touch";
    popup.setAttribute("data-align-trigger", alignMode ? "true" : "false");
    resetInlineStyles(content);
    content._tuiAligned = false;

    return positionPopper(content, trigger, alignMode ? "fixed" : "absolute")
      .then(() => {
        if (!alignMode) return undefined;
        if (positionAligned(content, trigger)) {
          content._tuiAligned = true;
          return undefined;
        }
        // Not enough room: redo the plain popper pass (the aligned attempt
        // dirtied the inline styles).
        popup.setAttribute("data-align-trigger", "false");
        resetInlineStyles(content);
        return positionPopper(content, trigger, "absolute");
      })
      .then(() => updateScrollArrows(content));
  }

  function startAutoPositioning(content, trigger) {
    if (content._tuiPositionCleanup) content._tuiPositionCleanup();
    let resolveFirst;
    const firstPosition = new Promise((resolve) => {
      resolveFirst = resolve;
    });
    const update = () => position(content, trigger).then(resolveFirst, resolveFirst);
    content._tuiPositionCleanup = window.FloatingUIDOM.autoUpdate(trigger, content, update, {
      elementResize: typeof ResizeObserver !== "undefined",
      layoutShift: typeof IntersectionObserver !== "undefined",
    });
    return firstPosition;
  }

  function stopAutoPositioning(content) {
    if (!content._tuiPositionCleanup) return;
    content._tuiPositionCleanup();
    content._tuiPositionCleanup = null;
  }

  // ----- scroll arrows + capped grow-on-scroll (Base UI behavior) -----------

  function updateScrollArrows(content) {
    const viewport = viewportFor(content);
    const up = content.querySelector("[data-tui-select-scroll-up]");
    const down = content.querySelector("[data-tui-select-scroll-down]");
    if (!viewport || !up || !down) return;
    const max = maxScrollTop(viewport);
    up.classList.toggle("hidden", max <= 0 || viewport.scrollTop <= TOL);
    down.classList.toggle("hidden", max <= 0 || viewport.scrollTop >= max - TOL);
  }

  // In aligned mode scrolling first consumes the remaining space toward the
  // viewport edge (capped by the popup's max-height), then scrolls the list.
  function handleAlignedScroll(content) {
    const viewport = viewportFor(content);
    const popup = popupFor(content);
    if (!viewport || !popup) return;

    const isTopPositioned = content.style.top === "0px";
    const isBottomPositioned = content.style.bottom === "0px";

    if (content._tuiReachedMax || !content._tuiAligned || (!isTopPositioned && !isBottomPositioned)) {
      updateScrollArrows(content);
      return;
    }

    const currentHeight = content.getBoundingClientRect().height;
    const maxPopupHeight = parseFloat(window.getComputedStyle(popup).maxHeight) || Infinity;
    const maxAvailableHeight = Math.min(
      document.documentElement.clientHeight - MARGIN * 2,
      maxPopupHeight,
    );

    const scrollTop = viewport.scrollTop;
    const max = maxScrollTop(viewport);

    let nextScrollTop = null;
    const diff = isTopPositioned ? max - scrollTop : scrollTop;
    const nextHeight = Math.min(currentHeight + diff, maxAvailableHeight);

    if (diff <= TOL) {
      const heightDelta = clamp(diff, 0, maxAvailableHeight - currentHeight);
      if (heightDelta > 0) {
        content.style.height = currentHeight + heightDelta + "px";
      }
      viewport.scrollTop = isTopPositioned ? maxScrollTop(viewport) : 0;
      if (maxAvailableHeight - (currentHeight + heightDelta) <= TOL) {
        content._tuiReachedMax = true;
      }
      updateScrollArrows(content);
      return;
    }

    if (maxAvailableHeight - nextHeight > TOL) {
      nextScrollTop = isTopPositioned ? Infinity : 0;
    } else if (isBottomPositioned && scrollTop < max) {
      const overshoot = currentHeight + diff - maxAvailableHeight;
      nextScrollTop = scrollTop - (diff - overshoot);
    }

    const nextPositionerHeight = Math.ceil(nextHeight);
    if (nextPositionerHeight !== 0) {
      content.style.height = nextPositionerHeight + "px";
    }

    if (nextScrollTop != null) {
      const target = clamp(nextScrollTop, 0, maxScrollTop(viewport));
      if (Math.abs(viewport.scrollTop - target) > TOL) {
        viewport.scrollTop = target;
      }
    }

    if (nextPositionerHeight >= maxAvailableHeight - TOL) {
      content._tuiReachedMax = true;
    }
    updateScrollArrows(content);
  }

  // Hovering a scroll arrow scrolls one item per tick, keeping the next item
  // clear of the arrow overlay (Base UI's getTargetScrollTop).
  function targetScrollTop(items, isUp, scrollTop, clientHeight, arrowHeight, max) {
    if (isUp) {
      let firstVisibleIndex = 0;
      const visibleTop = scrollTop + arrowHeight - TOL;
      for (let i = 0; i < items.length; i += 1) {
        if (items[i].offsetTop >= visibleTop) {
          firstVisibleIndex = i;
          break;
        }
      }
      const targetIndex = Math.max(0, firstVisibleIndex - 1);
      const target = items[targetIndex];
      return targetIndex < firstVisibleIndex && target
        ? clamp(target.offsetTop - arrowHeight, 0, max)
        : 0;
    }

    let lastVisibleIndex = items.length - 1;
    const visibleBottom = scrollTop + clientHeight - arrowHeight + TOL;
    for (let i = 0; i < items.length; i += 1) {
      if (items[i].offsetTop + items[i].offsetHeight > visibleBottom) {
        lastVisibleIndex = Math.max(0, i - 1);
        break;
      }
    }
    const targetIndex = Math.min(items.length - 1, lastVisibleIndex + 1);
    const target = items[targetIndex];
    return targetIndex > lastVisibleIndex && target
      ? clamp(target.offsetTop + target.offsetHeight - clientHeight + arrowHeight, 0, max)
      : max;
  }

  let arrowTimer = null;

  function stopArrowScroll() {
    clearTimeout(arrowTimer);
    arrowTimer = null;
  }

  function arrowScrollStep(content, isUp, arrow) {
    const viewport = viewportFor(content);
    if (!viewport) return;
    updateScrollArrows(content);
    const max = maxScrollTop(viewport);
    const scrollTop = clamp(viewport.scrollTop, 0, max);
    if (scrollTop === (isUp ? 0 : max)) {
      stopArrowScroll();
      return;
    }
    const items = [...content.querySelectorAll("[data-tui-select-item]")];
    viewport.scrollTop = targetScrollTop(
      items,
      isUp,
      scrollTop,
      viewport.clientHeight,
      arrow.offsetHeight || 0,
      max,
    );
    arrowTimer = setTimeout(() => arrowScrollStep(content, isUp, arrow), ARROW_TICK_MS);
  }

  document.addEventListener("mouseover", (e) => {
    if (!(e.target instanceof Element)) return;
    const arrow = e.target.closest("[data-tui-select-scroll-up], [data-tui-select-scroll-down]");
    if (!arrow || arrowTimer) return;
    const content = arrow.closest("[data-tui-select-content]");
    if (content) arrowScrollStep(content, arrow.hasAttribute("data-tui-select-scroll-up"), arrow);
  });

  document.addEventListener("mouseout", (e) => {
    if (!(e.target instanceof Element)) return;
    if (e.target.closest("[data-tui-select-scroll-up], [data-tui-select-scroll-down]")) {
      stopArrowScroll();
    }
  });

  // ----- open / close -------------------------------------------------------

  function open(content, trigger, openMethod) {
    allContents().forEach((c) => {
      if (c !== content) close(c);
    });
    clearTimeout(content._tuiHide);
    content._tuiOpenMethod = openMethod || "programmatic";
    // A press on the trigger can open the popup under the pointer (aligned
    // mode). Mouseup selection stays disabled briefly so releasing over the
    // selected item or a neighboring item doesn't commit an accidental
    // selection (Base UI's selectionRef + SELECTED_DELAY). Dragging can
    // re-arm unselected mouseup sooner, see the pointermove handler.
    content._tuiSelection = {
      allowSelectedMouseUp: false,
      allowUnselectedMouseUp: false,
      dragY: 0,
    };
    clearTimeout(content._tuiSelectedDelay);
    content._tuiSelectedDelay = setTimeout(() => {
      content._tuiSelection.allowSelectedMouseUp = true;
      content._tuiSelection.allowUnselectedMouseUp = true;
    }, SELECTED_DELAY);
    portal(content);
    // z-index portal like shadcn (no native top layer); re-append
    // keeps paint order = open order.
    document.body.appendChild(content);
    content.hidden = false;

    // Position it invisibly first, then play the enter animation in place.
    content.style.visibility = "hidden";
    const finish = () => {
      // The popup transitions `all` (duration-100), so clearing the
      // measuring visibility would animate visibility itself - and in
      // background tabs and throttled iframes that transition freezes at
      // its hidden start value. Flip with transitions suppressed.
      const popup = popupFor(content);
      content.style.transitionProperty = "none";
      if (popup) popup.style.transitionProperty = "none";
      content.style.visibility = "";
      void content.offsetWidth;
      content.style.transitionProperty = "";
      if (popup) {
        void popup.offsetWidth;
        popup.style.transitionProperty = "";
      }
      if (content.hidden || !content.isConnected) return;
      // useAnchoredPopupScrollLock measures the positioned popup for touch opens.
      content._tuiReleaseScroll?.();
      content._tuiReleaseScroll = window.tui.scrollLock.anchoredPopup(
        true, content._tuiOpenMethod === "touch", content, trigger,
      );
      setState(content, "open");
      startTransition(content);
      trigger.setAttribute("aria-expanded", "true");
      trigger.setAttribute("data-popup-open", "");
      trigger.setAttribute("data-pressed", "");
      // Base UI moves focus to the selected item when the listbox opens.
      const selected =
        content.querySelector('[data-tui-select-item][data-selected]') ||
        content.querySelector("[data-tui-select-item]");
      enqueueFocus(selected, () => isOpen(content));
    };
    startAutoPositioning(content, trigger).then(finish, finish);
  }

  function close(content) {
    if (content.hidden) return;
    stopAutoPositioning(content);
    stopArrowScroll();
    clearTimeout(content._tuiSelectedDelay);
    content._tuiSelection = {
      allowSelectedMouseUp: false,
      allowUnselectedMouseUp: false,
      dragY: 0,
    };
    content.style.visibility = "";
    setTransitionAttribute(content, "data-starting-style", false);
    setState(content, "closed");
    setTransitionAttribute(content, "data-ending-style", true);
    content._tuiReleaseScroll?.();
    content._tuiReleaseScroll = null;
    const trigger = triggerFor(content);
    if (trigger) {
      trigger.setAttribute("aria-expanded", "false");
      trigger.removeAttribute("data-popup-open");
      trigger.removeAttribute("data-pressed");
    }
    clearTimeout(content._tuiHide);
    // Aligned mode has no exit animation (animate-none, like shadcn) — hide
    // immediately instead of waiting for one.
    const popup = popupFor(content);
    if (popup && popup.getAttribute("data-align-trigger") === "true") {
      content.hidden = true;
      setTransitionAttribute(content, "data-ending-style", false);
      return;
    }
    content._tuiHide = setTimeout(() => {
      if (content.hasAttribute("data-closed") && !content.hidden) {
        content.hidden = true;
        setTransitionAttribute(content, "data-ending-style", false);
      }
    }, EXIT_MS);
  }

  function closeAll() {
    allContents().forEach(close);
  }

  function requestOpenChange(content, nextOpen, openMethod) {
    if (!content || isOpen(content) === nextOpen) return false;
    const accepted = content.dispatchEvent(
      new CustomEvent("select-open-change", {
        bubbles: true,
        cancelable: true,
        detail: {
          open: nextOpen,
          openMethod: nextOpen ? openMethod || "programmatic" : null,
        },
      }),
    );
    if (!accepted || content.hasAttribute("data-tui-select-open-controlled")) return false;
    const trigger = triggerFor(content);
    if (nextOpen && trigger) open(content, trigger, openMethod);
    else if (!nextOpen) close(content);
    return true;
  }

  function requestCloseAll() {
    allContents().forEach((content) => requestOpenChange(content, false));
  }

  function selectItem(content, item) {
    const trigger = triggerFor(content);
    if (!trigger) return;
  if (trigger.getAttribute("aria-readonly") === "true") return;
    const value = item.getAttribute("data-tui-select-value") || "";
    const label =
      item.getAttribute("data-tui-select-label") ||
      (item.querySelector("[data-tui-select-item-text]") || item).textContent.trim();

    const accepted = trigger.dispatchEvent(
      new CustomEvent("select-change", {
        bubbles: true,
        cancelable: true,
        detail: { value: value, label: label },
      }),
    );
    if (!accepted) return;

    if (!trigger.hasAttribute("data-tui-select-value-controlled")) {
      content.querySelectorAll("[data-tui-select-item]").forEach((i) => {
      i.removeAttribute("data-selected");
      i.setAttribute("aria-selected", "false");
      });
      item.setAttribute("data-selected", "");
      item.setAttribute("aria-selected", "true");

      const span = valueSpanFor(trigger);
      if (span) span.textContent = label;
      trigger.removeAttribute("data-placeholder");

      const input = inputFor(trigger);
      if (input && input.value !== value) {
        input.value = value;
        input.dispatchEvent(new Event("change", { bubbles: true }));
      }
    }
    requestOpenChange(content, false);
    trigger.focus();
  }

  // Shows the selected item's label in the trigger (server only knows the
  // value, the label lives in the item). Runs on load and whenever new selects
  // appear in the DOM (e.g. content swapped in by a library like htmx) — the
  // MutationObserver keeps this framework-agnostic.
  function init() {
    document.querySelectorAll("[data-tui-select-trigger]").forEach(listenForEscape);
    removeOrphanedContents();
    document.querySelectorAll("[data-tui-select-trigger]").forEach((trigger) => {
      const content = contentFor(trigger);
      if (!content) return;
      const checked = content.querySelector('[data-tui-select-item][data-selected]');
      if (checked) {
        const label =
          checked.getAttribute("data-tui-select-label") ||
          (checked.querySelector("[data-tui-select-item-text]") || checked).textContent.trim();
        const span = valueSpanFor(trigger);
        if (span && span.textContent.trim() !== label) span.textContent = label;
        if (trigger.hasAttribute("data-placeholder")) trigger.removeAttribute("data-placeholder");
      }
      if (content.getAttribute("data-tui-select-initial-open") === "true") {
        content.removeAttribute("data-tui-select-initial-open");
        const openMethod =
          content.getAttribute("data-tui-select-initial-open-method") || "programmatic";
        open(content, trigger, openMethod);
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  // Re-init on any childList mutation, directly (never rAF-deferred: rAF
  // does not fire in hidden tabs or throttled iframes): swapped-in markup
  // wires itself, removals release portaled content through the
  // ownership sweep.
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });

  // ----- events -------------------------------------------------------------

  // Pointer interactions toggle and dismiss on PRESS, exactly like Base UI.
  // Click is never used for open/close, so the stray click the browser fires
  // on <body> after the menu opened over the trigger is naturally harmless.
  function toggle(trigger, openMethod) {
    const content = contentFor(trigger);
    if (!content) return;
    requestOpenChange(content, !isOpen(content), openMethod);
  }

  // Pendant of floating-ui useClick's pointerTypeRef: pointerdown marks the
  // trigger, the click that follows the same press is skipped. A click
  // without the mark (a <label for> forward, keyboard activation, or a
  // programmatic .click()) toggles instead.
  const pressedTriggers = new WeakSet();

  function isMouseWithinBounds(e, el) {
    const rect = el.getBoundingClientRect();
    return (
      e.clientX >= rect.left &&
      e.clientX <= rect.right &&
      e.clientY >= rect.top &&
      e.clientY <= rect.bottom
    );
  }

  // Pendant of SelectTrigger's mousedown handler: the press that opened the
  // popup cancels the open again when released outside the trigger and the
  // popup positioner.
  function armCancelOpen(trigger, content) {
    // Firefox can fire the mouseup upon mousedown, hence the deferred attach.
    setTimeout(() => {
      document.addEventListener(
        "mouseup",
        (e) => {
          const target = e.target instanceof Element ? e.target : null;
          // Don't treat the release as an outside press when it lands on the
          // trigger or inside the popup (or their children).
          if (target && (trigger.contains(target) || content.contains(target))) return;
          if (isMouseWithinBounds(e, trigger)) return;
          close(content);
        },
        { once: true },
      );
    }, 0);
  }

  document.addEventListener("pointerdown", (e) => {
    if (e.button !== 0 || !(e.target instanceof Element)) return;
    const trigger = e.target.closest("[data-tui-select-trigger]");
    if (trigger) {
      // Touch opens on the click that fires at release (Base UI opens on
      // the compat mousedown, which for touch also fires post-touchend).
      // Opening at press would put the aligned popup under the still-down
      // finger, and the tap's click, hit-tested at the release point,
      // would land on the item above the trigger and instantly commit it.
      trigger._tuiOpenMethod = e.pointerType;
      if (e.pointerType === "touch") return;
      pressedTriggers.add(trigger);
      // Keep the browser from focusing the trigger button, focus lives on
      // the selected item while the listbox is open (Base UI focus scope).
      e.preventDefault();
      if (!trigger.disabled) {
        const content = contentFor(trigger);
        if (content) {
          if (isOpen(content)) {
            requestOpenChange(content, false);
          } else {
            requestOpenChange(content, true, e.pointerType);
            armCancelOpen(trigger, content);
          }
        }
      }
      return;
    }
    // Pendant of SelectItem's allowMouseSelectionRef: a real pointer click
    // only commits when its press started on the item. The stray click the
    // browser hit-tests onto the popup that just opened over the trigger
    // (touch fires its compatibility click at the tap position) never did.
    const item = e.target.closest("[data-tui-select-item]");
    if (item) {
      item._tuiPointerType = e.pointerType;
      item._tuiAllowMouseSelection = true;
      const content = item.closest("[data-tui-select-content]");
      if (content && content._tuiSelection) content._tuiSelection.dragY = 0;
    }
    if (!e.target.closest("[data-tui-select-content]")) requestCloseAll();
  });

  document.addEventListener("pointerover", (e) => {
    if (!(e.target instanceof Element)) return;
    const item = e.target.closest("[data-tui-select-item]");
    if (item) item._tuiPointerType = e.pointerType;
  });

  document.addEventListener("pointercancel", (e) => {
    if (!(e.target instanceof Element)) return;
    const trigger = e.target.closest("[data-tui-select-trigger]");
    if (!trigger) return;
    trigger._tuiOpenMethod = null;
    pressedTriggers.delete(trigger);
  });

  document.addEventListener("click", (e) => {
    if (!(e.target instanceof Element)) return;
    const trigger = e.target.closest("[data-tui-select-trigger]");
    if (trigger) {
      if (pressedTriggers.has(trigger)) {
        pressedTriggers.delete(trigger);
        return;
      }
      const openMethod = trigger._tuiOpenMethod || (e.detail === 0 ? "keyboard" : "mouse");
      trigger._tuiOpenMethod = null;
      if (!trigger.disabled) {
        toggle(trigger, openMethod);
      }
      return;
    }

    const item = e.target.closest("[data-tui-select-item]");
    if (item) {
      const content = item.closest("[data-tui-select-content]");
      if (!content) return;
      // Virtual clicks (detail 0: keyboard, assistive technology, .click())
      // represent explicit activation and always commit; so do touch clicks,
      // whose press necessarily started on the item.
      const isMouseClick = (item._tuiPointerType || "mouse") !== "touch";
      const isVirtualClick = e.detail === 0;
      const isInvalidMouseClick =
        isMouseClick && !isVirtualClick && !item._tuiAllowMouseSelection;
      item._tuiAllowMouseSelection = false;
      if (item.hasAttribute("data-disabled") || isInvalidMouseClick) return;
      selectItem(content, item);
    }
  });

  // Pendant of SelectItem's mouseup: releasing a press that started on the
  // trigger commits the item under the pointer (press trigger, drag, release
  // to select), once the SELECTED_DELAY / drag guards allow it. Touch never
  // selects on mouseup, only on click.
  document.addEventListener("mouseup", (e) => {
    if (!(e.target instanceof Element)) return;
    const item = e.target.closest("[data-tui-select-item]");
    if (!item) return;
    const content = item.closest("[data-tui-select-content]");
    const selection = content && content._tuiSelection;
    if (!selection) return;
    selection.dragY = 0;
    if (item.hasAttribute("data-disabled") || item._tuiPointerType === "touch") return;
    // Regular clicks are committed by the click event.
    if (item._tuiAllowMouseSelection) return;
    const selected = item.hasAttribute("data-selected");
    if (
      (!selection.allowSelectedMouseUp && selected) ||
      (!selection.allowUnselectedMouseUp && !selected)
    ) {
      return;
    }
    item._tuiAllowMouseSelection = true;
    item.click();
    item._tuiAllowMouseSelection = false;
  });

  let typeBuffer = "";
  let typeTimer;

  document.addEventListener("keydown", (e) => {
    if (closeOnEscapeKeyDown(e)) return;
    if (!(e.target instanceof Element)) return;

    // Closed trigger: arrow keys open the listbox (Enter/Space go through
    // the native button click path).
    const trigger = e.target.closest("[data-tui-select-trigger]");
    if (trigger && !trigger.disabled) {
      pressedTriggers.delete(trigger); // like useClick's onKeyDown reset
      trigger._tuiOpenMethod = null;
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const content = contentFor(trigger);
        if (content && !isOpen(content)) requestOpenChange(content, true, "keyboard");
      }
      return;
    }

    // Open listbox: roving focus on the items.
    const item = e.target.closest("[data-tui-select-item]");
    if (!item) return;
    const content = item.closest("[data-tui-select-content]");
    if (!content) return;
    const items = [...content.querySelectorAll("[data-tui-select-item]")].filter(
      (i) => !i.hasAttribute("data-disabled"),
    );
    const index = items.indexOf(item);

    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const next = items[index + (e.key === "ArrowDown" ? 1 : -1)];
      if (next) next.focus();
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      const edge = e.key === "Home" ? items[0] : items[items.length - 1];
      if (edge) edge.focus();
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      selectItem(content, item);
    } else if (e.key === "Tab") {
      requestOpenChange(content, false);
    } else if (e.key.length === 1) {
      clearTimeout(typeTimer);
      typeBuffer += e.key.toLowerCase();
      typeTimer = setTimeout(() => {
        typeBuffer = "";
      }, 500);
      const match = items.find((i) => i.textContent.trim().toLowerCase().startsWith(typeBuffer));
      if (match) match.focus();
    }
  });

  // The highlight follows the pointer, one highlighted item at a time.
  document.addEventListener("pointermove", (e) => {
    if (!(e.target instanceof Element)) return;
    const item = e.target.closest("[data-tui-select-item]");
    if (!item) return;
    // Dragging with the button held re-arms unselected mouseup selection
    // before SELECTED_DELAY has elapsed, once the drag covers >= 8px.
    if (e.pointerType === "mouse" && e.buttons === 1) {
      const content = item.closest("[data-tui-select-content]");
      if (content && content._tuiSelection) {
        content._tuiSelection.dragY += e.movementY;
        if (content._tuiSelection.dragY ** 2 >= 64) {
          content._tuiSelection.allowUnselectedMouseUp = true;
        }
      }
    }
    if (!item.hasAttribute("data-disabled") && document.activeElement !== item) {
      item.focus({ preventScroll: true });
    }
  });

  window.addEventListener(
    "scroll",
    (e) => {
      const inMenu = e.target instanceof Element && e.target.closest("[data-tui-select-content]");
      if (inMenu) {
        if (inMenu._tuiAligned) {
          handleAlignedScroll(inMenu);
        } else {
          updateScrollArrows(inMenu);
        }
        return;
      }
    },
    true,
  );

})();

// components/sidebar/sidebar.js
(function () {
  "use strict";

  const SIDEBAR_COOKIE_NAME = "sidebar_state";
  const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7; // 7 days
  const MOBILE_QUERY = "(max-width: 767px)";

  function wrapperFor(sidebarId) {
    return document.querySelector(
      '[data-tui-sidebar-wrapper][data-tui-sidebar-id="' + sidebarId + '"]',
    );
  }

  // SidebarProvider.openMobile survives the Sheet's viewport-driven unmount.
  function openMobileOf(sidebarId) {
    return !!anyWrapper(sidebarId)?.hasAttribute("data-tui-sidebar-open-mobile");
  }

  // SidebarProvider.setOpenMobile: state is independent of the mounted Sheet.
  function setOpenMobile(open, sidebarId) {
    const wrapper = anyWrapper(sidebarId);
    if (!wrapper) return;
    wrapper.toggleAttribute("data-tui-sidebar-open-mobile", !!open);
    if (!window.matchMedia(MOBILE_QUERY).matches) return;
    const popup = document.getElementById(wrapper.getAttribute("data-tui-sidebar-id") + "-mobile");
    const dialog = window.tui?.dialog;
    if (!popup || !dialog) return;
    if (open && !dialog.isOpen(popup)) dialog.open(popup);
    else if (!open && dialog.isOpen(popup)) dialog.close(popup);
  }

  // The sidebar content renders once and moves between the desktop container
  // and the mobile sheet, depending on the viewport.
  function init() {
    document.querySelectorAll("[data-tui-sidebar-content]").forEach((content) => {
      const sidebarId = content.getAttribute("data-tui-sidebar-content");
      const portal = document.querySelector(
        '[data-tui-sidebar-mobile-portal="' + sidebarId + '"]',
      );
      if (!portal) return;

      const isMobile = window.matchMedia(MOBILE_QUERY).matches;

      if (isMobile && content.parentElement !== portal) {
        portal.appendChild(content);
      } else if (!isMobile && content.parentElement === portal) {
        const inner = wrapperFor(sidebarId)?.querySelector('[data-slot="sidebar-inner"]');
        if (inner) inner.appendChild(content);
      }

      // Mount/unmount the Sheet with open={openMobile}, as in shadcn's Sidebar.
      const popup = document.getElementById(sidebarId + "-mobile");
      const dialog = window.tui?.dialog;
      if (!popup || !dialog) return;
      if (isMobile && openMobileOf(sidebarId) && !dialog.isOpen(popup)) {
        dialog.open(popup);
      } else if (!isMobile && dialog.isOpen(popup)) {
        dialog.close(popup);
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  window.addEventListener("resize", init);
  // Re-init on any childList mutation, directly (never rAF-deferred: rAF
  // does not fire in hidden tabs or throttled iframes): swapped-in markup
  // wires itself.
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });

  function toggleSidebar(sidebarId) {
    // shadcn's toggleSidebar: setOpenMobile((open) => !open) below md.
    if (window.matchMedia(MOBILE_QUERY).matches) {
      setOpenMobile(!openMobileOf(sidebarId), sidebarId);
      return;
    }

    const wrapper = wrapperFor(sidebarId);
    if (!wrapper) return;
    const mode = wrapper.getAttribute("data-tui-sidebar-collapsible-mode");
    if (mode === "none") return;

    const collapsed = wrapper.getAttribute("data-state") !== "collapsed";
    wrapper.setAttribute("data-state", collapsed ? "collapsed" : "expanded");
    // Like shadcn, data-collapsible carries the mode only while collapsed,
    // so icon/offcanvas selectors need no extra state check.
    wrapper.setAttribute("data-collapsible", collapsed ? mode : "");

    // Menu button tooltips only show while collapsed to icons.
    const tooltipsDisabled = !(collapsed && mode === "icon");
    wrapper.querySelectorAll("[data-tui-tooltip-trigger]").forEach((trigger) => {
      // An explicit tooltip.hidden pendant pins the state.
      if (trigger.hasAttribute("data-tui-sidebar-tooltip-fixed")) return;
      trigger.toggleAttribute("data-tui-tooltip-disabled", tooltipsDisabled);
    });

    document.cookie =
      SIDEBAR_COOKIE_NAME +
      "=" +
      (collapsed ? "false" : "true") +
      "; path=/; max-age=" +
      SIDEBAR_COOKIE_MAX_AGE;
  }

  document.addEventListener("click", (e) => {
    if (!(e.target instanceof Element)) return;
    const trigger = e.target.closest("[data-tui-sidebar-trigger]");
    if (!trigger) return;
    const targetId = trigger.getAttribute("data-tui-sidebar-target");
    if (targetId) toggleSidebar(targetId);
  });

  // Sheet onOpenChange={setOpenMobile}; unmount closes do not change state.
  document.addEventListener("dialog-open-change", (event) => {
    if (!(event.target instanceof Element)) return;
    const id = event.target.id;
    if (!id.endsWith("-mobile")) return;
    const sidebarId = id.slice(0, -"-mobile".length);
    if (wrapperFor(sidebarId)) setOpenMobile(event.detail.open, sidebarId);
  });

  // The useSidebar pendant: the same seven members as the React hook,
  // addressing the first sidebar unless a sidebarId is given.
  function anyWrapper(sidebarId) {
    return sidebarId
      ? wrapperFor(sidebarId)
      : document.querySelector("[data-tui-sidebar-wrapper]");
  }

  window.tui = window.tui || {};
  window.tui.sidebar = {
    state(sidebarId) {
      return anyWrapper(sidebarId)?.getAttribute("data-state") || null;
    },
    open(sidebarId) {
      return this.state(sidebarId) === "expanded";
    },
    setOpen(open, sidebarId) {
      const wrapper = anyWrapper(sidebarId);
      if (!wrapper) return;
      if (this.open(sidebarId) !== open) {
        toggleSidebar(wrapper.getAttribute("data-tui-sidebar-id"));
      }
    },
    openMobile(sidebarId) {
      return openMobileOf(sidebarId);
    },
    setOpenMobile(open, sidebarId) {
      setOpenMobile(open, sidebarId);
    },
    isMobile() {
      return window.matchMedia(MOBILE_QUERY).matches;
    },
    toggleSidebar(sidebarId) {
      const wrapper = anyWrapper(sidebarId);
      if (wrapper) toggleSidebar(wrapper.getAttribute("data-tui-sidebar-id"));
    },
  };

  // Cmd/Ctrl + shortcut key toggles the sidebar.
  document.addEventListener("keydown", (e) => {
    if (!(e.ctrlKey || e.metaKey) || e.key.length !== 1) return;
    const wrapper = document.querySelector("[data-tui-sidebar-wrapper]");
    if (!wrapper) return;
    const shortcut = wrapper.getAttribute("data-tui-sidebar-keyboard-shortcut");
    if (!shortcut || shortcut.toLowerCase() !== e.key.toLowerCase()) return;
    e.preventDefault();
    toggleSidebar(wrapper.getAttribute("data-tui-sidebar-id"));
  });
})();

// components/switch/switch.js
(function () {
  "use strict";

  // Vanilla port of Base UI's switch: the root span behavior comes from
  // switch/root/SwitchRoot.tsx, the non-native button keyboard semantics from
  // internals/use-button/useButton.ts. Clicks, Enter and Space forward to the
  // visually hidden native checkbox beside the root; the input's change event
  // syncs the state attributes back onto the root and thumb.

  function inputOf(root) {
    const next = root.nextElementSibling;
    return next && next.matches("[data-tui-switch-input]") ? next : null;
  }

  function rootOf(input) {
    const prev = input.previousElementSibling;
    return prev && prev.matches("[data-tui-switch]") ? prev : null;
  }

  function isDisabled(root, input) {
    return (input && input.disabled) || root.getAttribute("aria-disabled") === "true";
  }

  function isReadOnly(root) {
    return root.getAttribute("aria-readonly") === "true";
  }

  // Port of utils/dispatchClickWithModifiers.ts: the constructed click keeps
  // the source event's modifier state and still runs native activation
  // behavior (toggling the input).
  function forwardClick(target, sourceEvent) {
    target.dispatchEvent(
      new PointerEvent("click", {
        bubbles: true,
        cancelable: true,
        composed: true,
        detail: 0,
        shiftKey: sourceEvent.shiftKey,
        ctrlKey: sourceEvent.ctrlKey,
        altKey: sourceEvent.altKey,
        metaKey: sourceEvent.metaKey,
      }),
    );
  }

  function sync(root, input) {
    const checked = input.checked;
    root.setAttribute("aria-checked", String(checked));
    root.toggleAttribute("data-checked", checked);
    root.toggleAttribute("data-unchecked", !checked);
    // Base UI mirrors the state onto the thumb via the stateAttributesMapping.
    const thumb = root.querySelector('[data-slot="switch-thumb"]');
    if (thumb) {
      thumb.toggleAttribute("data-checked", checked);
      thumb.toggleAttribute("data-unchecked", !checked);
    }
  }

  function requestCheckedChange(root, input, sourceEvent) {
    const nextChecked = !input.checked;
    const change = new CustomEvent("switch-change", {
      bubbles: true,
      cancelable: true,
      detail: { checked: nextChecked },
    });
    root.dispatchEvent(change);
    if (change.defaultPrevented || root.hasAttribute("data-tui-switch-controlled")) return;
    forwardClick(input, sourceEvent);
  }

  // SwitchRoot onClick: cancel the click's default (a wrapping label would
  // otherwise forward it to the input a second time) and toggle through the
  // hidden input so the native change event fires.
  document.addEventListener("click", (e) => {
    const root = e.target.closest && e.target.closest("[data-tui-switch]");
    if (!root) return;
    const input = inputOf(root);
    if (!input) return;
    if (isDisabled(root, input)) {
      // useButton prevents clicks on disabled non-native buttons.
      e.preventDefault();
      return;
    }
    if (isReadOnly(root)) return;
    e.preventDefault();
    requestCheckedChange(root, input, e);
  });

  document.addEventListener("change", (e) => {
    const input = e.target;
    if (!input.matches || !input.matches("[data-tui-switch-input]")) return;
    const root = rootOf(input);
    if (root) sync(root, input);
  });

  document.addEventListener("keydown", (e) => {
    const root = e.target;
    if (!root.matches || !root.matches("[data-tui-switch]")) return;
    if (isDisabled(root, inputOf(root))) return;
    if (e.key === "Enter") {
      // useButton: Enter activates non-native buttons on keydown.
      if (e.defaultPrevented) return;
      e.preventDefault();
      forwardClick(root, e);
    } else if (e.key === " ") {
      // useButton: Space activates on keyup; prevent the page scroll.
      e.preventDefault();
    }
  });

  // useButton keyup: Space dispatches the click on the root itself, which the
  // click handler above forwards to the input.
  document.addEventListener("keyup", (e) => {
    const root = e.target;
    if (!root.matches || !root.matches("[data-tui-switch]")) return;
    if (e.key !== " " || e.defaultPrevented) return;
    if (isDisabled(root, inputOf(root))) return;
    forwardClick(root, e);
  });

  // Focus on the hidden input (label clicks, programmatic focus) belongs on
  // the root (SwitchRoot's input onFocus).
  document.addEventListener("focusin", (e) => {
    const input = e.target;
    if (!input.matches || !input.matches("[data-tui-switch-input]")) return;
    const root = rootOf(input);
    if (root) root.focus();
  });

  let labelId = 0;

  function setup(root) {
    if (root.hasAttribute("data-tui-switch-initialized")) return;
    root.setAttribute("data-tui-switch-initialized", "");
    const input = inputOf(root);
    if (!input) return;
    // The clicks dispatched on the hidden input are an implementation detail
    // and must not reach ancestors, which already receive the original click
    // (SwitchRoot's input onClick).
    input.addEventListener("click", (e) => e.stopPropagation());
    // useAriaLabelledBy fallback: the span control is labelled by the native
    // label associated with the hidden input.
    if (!root.hasAttribute("aria-labelledby") && !root.hasAttribute("aria-label")) {
      const label =
        input.parentElement && input.parentElement.tagName === "LABEL"
          ? input.parentElement
          : input.labels && input.labels[0];
      if (label) {
        if (!label.id) {
          labelId += 1;
          label.id = (input.id || "tui-switch-" + labelId) + "-label";
        }
        root.setAttribute("aria-labelledby", label.id);
      }
    }
    sync(root, input);
  }

  function init() {
    document.querySelectorAll("[data-tui-switch]").forEach(setup);
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
})();

// components/tabs/tabs.js
(function () {
  "use strict";

  // Update tab state
  function setActiveTab(tabsId, value) {
  const root = document.querySelector(
    `[data-tui-tabs][data-tui-tabs-id="${tabsId}"]`,
  );
  if (root) root.setAttribute("data-tui-tabs-value", value || "");
    // Update all triggers with this tabs-id
    document
      .querySelectorAll(`[data-tui-tabs-trigger][data-tui-tabs-id="${tabsId}"]`)
      .forEach((trigger) => {
        const isActive = trigger.getAttribute("data-tui-tabs-value") === value;
        trigger.setAttribute(
          "data-tui-tabs-state",
          isActive ? "active" : "inactive",
        );
        // Base UI marks the selected tab with a bare data-active attribute;
        // the styles select on it.
        trigger.toggleAttribute("data-active", isActive);
        // The ARIA state moves with the visual one, and the roving tabindex
        // keeps the list a single tab stop.
        trigger.setAttribute("aria-selected", isActive ? "true" : "false");
        trigger.setAttribute("tabindex", isActive ? "0" : "-1");
      });

    // Update all contents with this tabs-id
    document
      .querySelectorAll(`[data-tui-tabs-content][data-tui-tabs-id="${tabsId}"]`)
      .forEach((content) => {
        const isActive = content.getAttribute("data-tui-tabs-value") === value;
        content.setAttribute(
          "data-tui-tabs-state",
          isActive ? "active" : "inactive",
        );
        content.classList.toggle("hidden", !isActive);
        content.setAttribute("tabindex", isActive ? "0" : "-1");
      });
  }

  function requestValueChange(root, value) {
    if (!root || root.getAttribute("data-tui-tabs-value") === value) return;
    const accepted = root.dispatchEvent(
      new CustomEvent("tabs-value-change", {
        bubbles: true,
        cancelable: true,
        detail: { value },
      }),
    );
    if (!accepted || root.hasAttribute("data-tui-tabs-controlled")) return;
    setActiveTab(root.getAttribute("data-tui-tabs-id"), value);
  }

  // Click handler
  document.addEventListener("click", (e) => {
    const trigger = e.target.closest("[data-tui-tabs-trigger]");
    if (!trigger || trigger.getAttribute("aria-disabled") === "true") return;

    const tabsId = trigger.getAttribute("data-tui-tabs-id");
    const value = trigger.getAttribute("data-tui-tabs-value");
    if (tabsId && value) {
    const root = trigger.closest("[data-tui-tabs]");
    requestValueChange(root, value);
    }
  });

  // Keyboard navigation from useTabsList: the arrows walk the list, Home and
  // End jump to its ends, disabled tabs stay focusable and movement wraps.
  //
  // Moving focus does not activate. That is Base UI's activateOnFocus=false
  // default, and the right one here: a panel is free to load its content when
  // it becomes active, and selecting on every keystroke would fire a request
  // per arrow press. Enter and Space activate, through the native button
  // click the click handler above already answers. Set ActivateOnFocus on the
  // list for the other behaviour.
  document.addEventListener("keydown", (e) => {
    const trigger = e.target.closest && e.target.closest("[data-tui-tabs-trigger]");
    if (!trigger) return;
    const root = trigger.closest("[data-tui-tabs]");
    if (!root) return;

    // In a horizontal list the arrows follow the writing direction.
    const vertical = root.getAttribute("data-orientation") === "vertical";
    const rtl = getComputedStyle(root).direction === "rtl";
    const prev = vertical ? "ArrowUp" : rtl ? "ArrowRight" : "ArrowLeft";
    const next = vertical ? "ArrowDown" : rtl ? "ArrowLeft" : "ArrowRight";

    const triggers = [...root.querySelectorAll("[data-tui-tabs-trigger]")];
    const current = triggers.indexOf(trigger);
    if (current === -1) return;

    let target = null;
    if (e.key === next) target = triggers[(current + 1) % triggers.length];
    else if (e.key === prev)
      target = triggers[(current - 1 + triggers.length) % triggers.length];
    else if (e.key === "Home") target = triggers[0];
    else if (e.key === "End") target = triggers[triggers.length - 1];
    if (!target) return;

    e.preventDefault(); // the arrows would otherwise scroll the page

    const list = trigger.closest("[data-tui-tabs-list]");
    if (
      list && list.hasAttribute("data-activate-on-focus") &&
      target.getAttribute("aria-disabled") !== "true"
    ) {
      // setActiveTab moves the roving tabindex with the selection.
      setActiveTab(
        target.getAttribute("data-tui-tabs-id"),
        target.getAttribute("data-tui-tabs-value"),
      );
    } else {
      // Focus moves without selecting, so the roving tabindex has to follow
      // the focus instead: tabbing away and back returns to where the user
      // was, not to the selected tab.
      triggers.forEach((t) => t.setAttribute("tabindex", t === target ? "0" : "-1"));
    }
    target.focus();
  });

  // Initialize active states
  function init() {
    document.querySelectorAll("[data-tui-tabs]").forEach((container) => {
      const tabsId = container.getAttribute("data-tui-tabs-id");
      if (!tabsId) return;

      // Find active trigger or use first
    const authored = container.querySelector(
    `[data-tui-tabs-trigger][data-tui-tabs-state="active"]`,
    );
    const activeTrigger =
    authored ||
    (container.hasAttribute("data-tui-tabs-controlled")
      ? null
      : container.querySelector(`[data-tui-tabs-trigger]:not([aria-disabled="true"])`));

      if (activeTrigger) {
        setActiveTab(tabsId, activeTrigger.getAttribute("data-tui-tabs-value"));
      }
    });
  }

  // Setup on load and mutations
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  // Re-init on any childList mutation, directly (never rAF-deferred: rAF
  // does not fire in hidden tabs or throttled iframes): swapped-in markup
  // wires itself.
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });

  // Expose public API
  window.tui = window.tui || {};
  window.tui.tabs = {
    setActive: setActiveTab,
  };
})();

// components/tooltip/tooltip.js
// Uses window.FloatingUIDOM from components/floatingui (loaded in the same bundle).
(function () {
  // Exit animations run at the tw-animate default (150ms); hide after.
  const EXIT_MS = 170;

  const escapeTargets = new WeakSet();
  function listenForEscape(element) {
    if (!element || escapeTargets.has(element)) return;
    element.addEventListener("keydown", closeOnEscapeKeyDown);
    escapeTargets.add(element);
  }

  // useDismiss: popup/reference listeners stop Escape before outer document handlers.
  function closeOnEscapeKeyDown(event) {
    if (event.key !== "Escape") return;
    const contents = event.currentTarget === document
      ? allContents()
      : [event.currentTarget.hasAttribute("data-tui-tooltip-content")
        ? event.currentTarget
        : contentFor(event.currentTarget)];
    let handled = false;
    for (const content of contents) {
      if (!content?.hasAttribute("data-open")) continue;
      if (requestOpenChange(triggerFor(content), false)) event.preventDefault();
      event.stopPropagation();
      handled = true;
    }
    return handled;
  }

  function allContents() {
    return document.querySelectorAll("[data-tui-tooltip-content]");
  }

  function contentFor(trigger) {
    return document.getElementById(trigger.getAttribute("aria-describedby"));
  }

  function triggerFor(content) {
    return document.querySelector(
      '[data-tui-tooltip-trigger][aria-describedby="' + content.id + '"]',
    );
  }

  // Base UI zooms the popup out of the anchor's center point (e.g.
  // "96px -4px"), not out of a placement corner.
  function anchorOrigin(result, anchorRect, positionerRect, sideOffset) {
    const side = result.placement.split("-")[0];
    const centerX = anchorRect.left + anchorRect.width / 2 - positionerRect.left + "px";
    const centerY = anchorRect.top + anchorRect.height / 2 - positionerRect.top + "px";
    if (side === "bottom") return centerX + " " + -sideOffset + "px";
    if (side === "top") return centerX + " calc(100% + " + sideOffset + "px)";
    if (side === "right") return -sideOffset + "px " + centerY;
    return "calc(100% + " + sideOffset + "px) " + centerY;
  }

  // The arrow styles itself per side (data-side classes, like Base UI's
  // Arrow); the script only feeds it the side and the centered coordinate.
  function placeArrow(content, side, arrowData) {
    const arrowEl = content.querySelector("[data-tui-tooltip-arrow]");
    if (!arrowEl) return;
    arrowEl.setAttribute("data-side", side);
    arrowEl.style.left = arrowData && arrowData.x != null ? arrowData.x + "px" : "";
    arrowEl.style.top = arrowData && arrowData.y != null ? arrowData.y + "px" : "";
  }

  // Moves the content to <body> (shadcn portals it the same way).
  // The unmount half of the React portal pendant: a portaled content lives
  // as long as its SSR declaration site (_tuiPortalOwner) stays in the
  // document. Trigger-presence heuristics judged mid-swap moments wrongly -
  // multi-phase swap layers briefly disconnect the new triggers.
  function removeOrphanedContents(content) {
    document.querySelectorAll("body > [data-tui-tooltip-content]").forEach((c) => {
      if (c !== content && c._tuiPortalOwner && !c._tuiPortalOwner.isConnected) {
        stopAutoPositioning(c);
        c.remove();
      }
    });
  }

  function portal(content) {
    listenForEscape(content);
    removeOrphanedContents(content);
    if (content.parentElement !== document.body) {
      if (!content._tuiPortalOwner) content._tuiPortalOwner = content.parentElement;
      document.body.appendChild(content);
    }
  }

  function positionContent(content, trigger) {
    const { computePosition, offset, flip, shift, arrow } = window.FloatingUIDOM;
    const side = content.getAttribute("data-tui-tooltip-side") || "top";
    const sideOffset =
      parseInt(content.getAttribute("data-tui-tooltip-side-offset"), 10) || 4;
    const arrowEl = content.querySelector("[data-tui-tooltip-arrow]");

    return computePosition(trigger, content, {
      placement: side,
      strategy: "absolute",
      middleware: [
        offset(sideOffset),
        flip(),
        shift({ padding: 5 }),
        arrowEl ? arrow({ element: arrowEl, padding: 5 }) : undefined,
      ].filter(Boolean),
    }).then((result) => {
      content.style.transition = "none";
      content.style.left = result.x + "px";
      content.style.top = result.y + "px";
      content.style.setProperty(
        "--transform-origin",
        anchorOrigin(
          result,
          trigger.getBoundingClientRect(),
          content.getBoundingClientRect(),
          sideOffset,
        ),
      );
      const finalSide = result.placement.split("-")[0];
      content.setAttribute("data-side", finalSide);
      placeArrow(content, finalSide, result.middlewareData.arrow);
      content.offsetHeight; // flush styles before re-enabling transitions
      content.style.transition = "";
    });
  }

  function startAutoPositioning(content, trigger) {
    if (content._tuiPositionCleanup) content._tuiPositionCleanup();
    let resolveFirst;
    const firstPosition = new Promise((resolve) => {
      resolveFirst = resolve;
    });
    const update = () => positionContent(content, trigger).then(resolveFirst, resolveFirst);
    content._tuiPositionCleanup = window.FloatingUIDOM.autoUpdate(trigger, content, update, {
      elementResize: typeof ResizeObserver !== "undefined",
      layoutShift: typeof IntersectionObserver !== "undefined",
    });
    return firstPosition;
  }

  function stopAutoPositioning(content) {
    if (!content._tuiPositionCleanup) return;
    content._tuiPositionCleanup();
    content._tuiPositionCleanup = null;
  }

  function open(trigger) {
    // Consumers can suppress a tooltip situationally (e.g. the sidebar only
    // shows menu tooltips while collapsed to icons).
    if (trigger.hasAttribute("data-tui-tooltip-disabled")) return;
    const content = contentFor(trigger);
    if (!content) return;
    clearTimeout(content._tuiHide);
    portal(content);
    // z-index portal like shadcn (no native top layer); re-append
    // keeps paint order = open order.
    document.body.appendChild(content);
    content.hidden = false;

    // Position it invisibly first, then play the enter animation in place.
    content.style.visibility = "hidden";
    startAutoPositioning(content, trigger).then(() => {
      if (content.hidden) return; // closed meanwhile
      // duration-100 transitions `all`; a visibility transition would
      // freeze at hidden in background tabs - flip suppressed.
      content.style.transitionProperty = "none";
      content.style.visibility = "";
      void content.offsetWidth;
      content.style.transitionProperty = "";
      content.removeAttribute("data-closed");
      content.removeAttribute("data-ending-style");
      content.setAttribute("data-open", "");
      content.setAttribute("data-starting-style", "");
      const arrowEl = content.querySelector("[data-tui-tooltip-arrow]");
      if (arrowEl) {
        arrowEl.removeAttribute("data-closed");
        arrowEl.removeAttribute("data-ending-style");
        arrowEl.setAttribute("data-open", "");
        arrowEl.setAttribute("data-starting-style", "");
      }
      trigger.setAttribute("data-popup-open", "");
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          content.removeAttribute("data-starting-style");
          if (arrowEl) arrowEl.removeAttribute("data-starting-style");
        });
      });
    });
  }

  function close(content) {
    if (content.hidden) return;
    stopAutoPositioning(content);
    content.removeAttribute("data-open");
    content.removeAttribute("data-starting-style");
    content.setAttribute("data-closed", "");
    content.setAttribute("data-ending-style", "");
    const arrowEl = content.querySelector("[data-tui-tooltip-arrow]");
    if (arrowEl) {
      arrowEl.removeAttribute("data-open");
      arrowEl.removeAttribute("data-starting-style");
      arrowEl.setAttribute("data-closed", "");
      arrowEl.setAttribute("data-ending-style", "");
    }
    const trigger = triggerFor(content);
    if (trigger) trigger.removeAttribute("data-popup-open");
    clearTimeout(content._tuiHide);
    content._tuiHide = setTimeout(() => {
      if (content.hasAttribute("data-closed") && !content.hidden) {
        content.hidden = true;
        content.removeAttribute("data-ending-style");
        if (arrowEl) arrowEl.removeAttribute("data-ending-style");
      }
    }, EXIT_MS);
  }

  function closeAll() {
    allContents().forEach(close);
  }

  function requestOpenChange(trigger, nextOpen) {
    if (!trigger) return false;
    const content = contentFor(trigger);
    if (!content || content.hasAttribute("data-open") === nextOpen) return false;
    const accepted = content.dispatchEvent(
      new CustomEvent("tooltip-open-change", {
        bubbles: true,
        cancelable: true,
        detail: { open: nextOpen },
      }),
    );
    if (!accepted || content.hasAttribute("data-tui-tooltip-controlled")) return false;
    if (nextOpen) open(trigger);
    else close(content);
    return true;
  }

  function requestCloseAll() {
    allContents().forEach((content) => requestOpenChange(triggerFor(content), false));
  }

  // ----- events -------------------------------------------------------------

  document.addEventListener("mouseover", (e) => {
    const trigger = e.target.closest("[data-tui-tooltip-trigger]");
    if (trigger) requestOpenChange(trigger, true);
  });

  document.addEventListener("mouseout", (e) => {
    const trigger = e.target.closest("[data-tui-tooltip-trigger]");
    if (!trigger) return;
    if (e.relatedTarget && trigger.contains(e.relatedTarget)) return; // still inside
    const content = contentFor(trigger);
    if (content) requestOpenChange(trigger, false);
  });

  // Keyboard: show on focus, hide on blur. Like Base UI, only visible
  // focus opens the tooltip, so programmatic focus (e.g. a dialog's
  // autofocus) does not pop it.
  document.addEventListener("focusin", (e) => {
    const trigger = e.target.closest("[data-tui-tooltip-trigger]");
    if (trigger && trigger.matches(":focus-visible")) requestOpenChange(trigger, true);
  });

  document.addEventListener("focusout", (e) => {
    const trigger = e.target.closest("[data-tui-tooltip-trigger]");
    if (!trigger) return;
    const content = contentFor(trigger);
    if (content) requestOpenChange(trigger, false);
  });

  document.addEventListener("keydown", closeOnEscapeKeyDown);

  // Content stays in its hidden portal node until it opens.
  function init() {
    removeOrphanedContents();
    document.querySelectorAll("[data-tui-tooltip-trigger]").forEach(listenForEscape);
    allContents().forEach((content) => {
      if (content.getAttribute("data-tui-tooltip-initial-open") === "true") {
        content.removeAttribute("data-tui-tooltip-initial-open");
        const trigger = triggerFor(content);
        if (trigger) open(trigger);
      }
    });
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  // Re-init on any childList mutation, directly (never rAF-deferred: rAF
  // does not fire in hidden tabs or throttled iframes): swapped-in markup
  // wires itself, removals release portaled content through the
  // ownership sweep.
  new MutationObserver(() => init()).observe(document.body, { childList: true, subtree: true });

})();

