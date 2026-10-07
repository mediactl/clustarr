// The SSE `art` event (artwork design §B.8 as amended 2026-10-07): swap every
// image of the changed key to the new digest's URL. htmx's SSE extension
// dispatches htmx:sseMessage for every message it listens for on an
// sse-connect element; the library grid and the item page listen for `art`
// through hx-trigger="sse:art".
document.addEventListener("htmx:sseMessage", (e) => {
  if (!e.detail || e.detail.type !== "art") return;
  let msg;
  try { msg = JSON.parse(e.detail.data); } catch { return; }
  if (!msg || typeof msg.key !== "string" || typeof msg.v !== "string") return;
  document.querySelectorAll(`img[data-art="${CSS.escape(msg.key)}"]`).forEach((img) => {
    img.src = `/art/${msg.key}?v=${encodeURIComponent(msg.v)}`;
  });
});
