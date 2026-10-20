// A dialog htmx swaps in -- the episode, book and season-search modals,
// open by default -- is moved to <body> by shadcn-templ's portal
// (components/baseui/portal.js) when its lifecycle MutationObserver opens it,
// a microtask after the swap and before htmx settles it. htmx then processes
// only the portal's owner, left empty where the dialog was declared, so
// nothing inside the dialog was wired: an interactive search's panel never
// polled and read "Searching indexers…" for good (2026-10-19). htmx:load
// fires on that owner once the swap settles; process every portaled node it
// owns. htmx.process leaves a node it has already wired alone, so a dialog
// that moves only after htmx has processed it is unaffected.
document.addEventListener("htmx:load", (e) => {
  const portal = window.templ?.portal;
  if (!portal) return;
  document.querySelectorAll("[data-base-ui-portal]").forEach((node) => {
    if (e.target.contains(portal.treeParent(node))) htmx.process(node);
  });
});
