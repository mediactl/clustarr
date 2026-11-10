/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/paging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// libraryPushInterval is how often [defaultSubscribeLibrary] polls
// Options.Library when Options.SubscribeLibrary is left nil.
const libraryPushInterval = 5 * time.Second

// unmatchedPushInterval is how often [defaultSubscribeUnmatched] polls
// Options.Unmatched when Options.SubscribeUnmatched is left nil.
const unmatchedPushInterval = 5 * time.Second

// importListsPushInterval is how often [defaultSubscribeImportLists] polls
// Options.ImportLists when Options.SubscribeImportLists is left nil.
const importListsPushInterval = 5 * time.Second

// downloadViews joins a page's rows with their transfer telemetry
// (Options.TransferProgress); a row whose read fails or finds nothing
// renders without it.
func (s *Server) downloadViews(ctx context.Context, rows []projection.DownloadRow) []views.DownloadView {
	out := make([]views.DownloadView, len(rows))
	for i := range rows {
		out[i].Row = rows[i]
		if s.opts.TransferProgress == nil || rows[i].Entry.UID == "" {
			continue
		}
		switch rows[i].Entry.Phase {
		case commonv1.DownloadPhaseQueued, commonv1.DownloadPhaseDownloading, commonv1.DownloadPhasePaused,
			commonv1.DownloadPhaseSeeding, commonv1.DownloadPhaseCompleted:
		default:
			continue
		}
		pr, ok, err := s.opts.TransferProgress(ctx, rows[i].Entry.UID)
		if err != nil {
			logging.FromContext(ctx).Debug("read transfer progress", "entry", rows[i].Entry.ID, "error", err)
			continue
		}
		if ok {
			out[i].Progress = &pr
		}
	}
	return out
}

// handleLibraryEvents streams the Library page's projection as Server-Sent
// Events. Each event's data is the same HTML fragment
// views.LibraryRows renders for the initial GET /library inside
// #library-rows; the Library page wires hx-ext="sse"
// sse-connect="/events/library" sse-swap="library" onto that element, and
// htmx's SSE extension swaps its innerHTML with the named event's data
// verbatim, so the payload must already be rendered markup.
//
// It subscribes once via Options.SubscribeLibrary and relays whatever
// arrives until the client goes away. In production
// Options.SubscribeLibrary is backed by the shared *projection.Projection --
// one list round feeding every stream
// (design plan ruling R4, Task G3-3); a nil Options.SubscribeLibrary falls
// back to [defaultSubscribeLibrary], a per-connection poll of Options.Library.
func (s *Server) handleLibraryEvents(w http.ResponseWriter, r *http.Request) {
	// One stream per tab: a tab's grid is swapped whole on every frame, so
	// a frame carrying another tab's rows would redraw it with the wrong
	// kind. The rows are filtered here, per frame, with the same rule the
	// page itself uses (projection.ForTab); the projection stays one tick.
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	ch, unsubscribe := s.opts.SubscribeLibrary()
	defer unsubscribe()
	// The SSE art event (artwork design §B.8 as amended 2026-10-07): a
	// changed cover of an item on the page this subscriber last rendered.
	art, unsubscribeArt := s.artEvents.subscribe()
	defer unsubscribeArt()
	onPage := map[string]bool{}

	// The same view and window the page parsed from the same query, so a
	// frame carries exactly the rows the page shows.
	view := parseLibraryView(r.URL.Query())
	want := paging.Parse(r.URL.Query())
	want.Params = view.values()
	for {
		select {
		case <-ctx.Done():
			return
		case items := <-ch:
			rows := view.arrange(projection.ForTab(items, tab))
			p := want.Page(len(rows))
			window := paging.Window(rows, p)
			if !writeLibraryEvent(w, ctx, tab, p, window) {
				return
			}
			clear(onPage)
			for _, li := range window {
				onPage[itemOfArtKey(li.ArtKey)] = true
			}
			flusher.Flush()
		case e := <-art:
			if !onPage[itemOfArtKey(e.Key)] {
				continue
			}
			if !writeArtEvent(w, e) {
				return
			}
			flusher.Flush()
		}
	}
}

// maxArtEventKeys bounds GET /events/art's key parameters.
const maxArtEventKeys = 16

// handleArtEvents streams the SSE art event for the image keys it is asked
// about -- GET /events/art?key=<kind>/<uid>/<type>, up to maxArtEventKeys,
// each validated as /art validates its path -- for a page with no stream of
// its own (the item page). With no artwork store it answers 204, which tells
// an EventSource not to reconnect.
func (s *Server) handleArtEvents(w http.ResponseWriter, r *http.Request) {
	keys := r.URL.Query()["key"]
	if len(keys) == 0 || len(keys) > maxArtEventKeys {
		http.Error(w, fmt.Sprintf("between 1 and %d key parameters are required", maxArtEventKeys), http.StatusBadRequest)
		return
	}
	asked := make(map[string]bool, len(keys))
	for _, k := range keys {
		if !parseArtKey(k) {
			http.Error(w, "a key is <kind>/<uid>/<type>", http.StatusBadRequest)
			return
		}
		asked[k] = true
	}
	if s.artEvents == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	art, unsubscribe := s.artEvents.subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-art:
			if !asked[e.Key] {
				continue
			}
			if !writeArtEvent(w, e) {
				return
			}
			flusher.Flush()
		}
	}
}

// writeArtEvent writes one "art" SSE event, its data one JSON line, and
// reports whether the write succeeded.
func writeArtEvent(w http.ResponseWriter, e artEvent) bool {
	b, err := json.Marshal(e)
	if err != nil {
		return false
	}
	var buf bytes.Buffer
	buf.WriteString("event: art\ndata: ")
	buf.Write(b)
	buf.WriteString("\n\n")
	_, err = w.Write(buf.Bytes())
	return err == nil
}

// writeLibraryEvent writes one "library" SSE event for items and reports
// whether the write succeeded; a false return means the client is gone and
// the caller should stop. Its framing is writePipelineEvent's, unchanged.
func writeLibraryEvent(w http.ResponseWriter, ctx context.Context, tab projection.Tab, p paging.Page, items []projection.LibraryItem) bool {
	if items == nil {
		items = []projection.LibraryItem{}
	}

	var fragment bytes.Buffer
	if err := views.LibraryList(tab, p, items).Render(ctx, &fragment); err != nil {
		logging.FromContext(ctx).Error("render library rows for sse", "error", err)
		return false
	}

	var buf bytes.Buffer
	buf.WriteString("event: library\n")
	for _, line := range bytes.Split(fragment.Bytes(), []byte{'\n'}) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')

	if _, err := w.Write(buf.Bytes()); err != nil {
		return false
	}
	return true
}

// handleUnmatchedEvents streams the Unmatched page's projection as
// Server-Sent Events, mirroring handleLibraryEvents for the
// LibraryScan.status.unmatched data (amendment §A3.4: "SSE on scan").
func (s *Server) handleUnmatchedEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	ch, unsubscribe := s.opts.SubscribeUnmatched()
	defer unsubscribe()

	want := paging.Parse(r.URL.Query())
	for {
		select {
		case <-ctx.Done():
			return
		case entries := <-ch:
			p := want.Page(len(entries))
			if !writeUnmatchedEvent(w, ctx, p, paging.Window(entries, p)) {
				return
			}
			flusher.Flush()
		}
	}
}

// writeUnmatchedEvent writes one "unmatched" SSE event for entries and
// reports whether the write succeeded, mirroring writeLibraryEvent.
func writeUnmatchedEvent(w http.ResponseWriter, ctx context.Context, p paging.Page, entries []projection.UnmatchedEntry) bool {
	if entries == nil {
		entries = []projection.UnmatchedEntry{}
	}

	var fragment bytes.Buffer
	if err := views.UnmatchedList(p, entries).Render(ctx, &fragment); err != nil {
		logging.FromContext(ctx).Error("render unmatched rows for sse", "error", err)
		return false
	}

	var buf bytes.Buffer
	buf.WriteString("event: unmatched\n")
	for _, line := range bytes.Split(fragment.Bytes(), []byte{'\n'}) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')

	if _, err := w.Write(buf.Bytes()); err != nil {
		return false
	}
	return true
}

// handleImportListsEvents streams the Import Lists page's projection as
// Server-Sent Events (Task G3-4, amendment §A3.4: "Poll on sync"), mirroring
// handleLibraryEvents and handleUnmatchedEvents. Each event's data is the
// same HTML fragment views.ImportListRows renders for the initial GET
// /import-lists inside #import-lists-rows; the Import Lists page wires
// hx-ext="sse" sse-connect="/events/import-lists" sse-swap="import-lists"
// onto that element, and htmx's SSE extension swaps its innerHTML with the
// named event's data verbatim.
//
// Like the other pages' event handlers, this subscribes once via
// Options.SubscribeImportLists and relays whatever arrives until the client
// goes away. In production Options.SubscribeImportLists is backed by the
// same *projection.Projection as every other stream -- one list round
// feeding all of them (design plan ruling R4, Task G3-4); a nil
// Options.SubscribeImportLists falls back to [defaultSubscribeImportLists],
// a per-connection poll of Options.ImportLists.
func (s *Server) handleImportListsEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	ch, unsubscribe := s.opts.SubscribeImportLists()
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case entries := <-ch:
			if !writeImportListsEvent(w, ctx, entries) {
				return
			}
			flusher.Flush()
		}
	}
}

// writeImportListsEvent writes one "import-lists" SSE event for entries and
// reports whether the write succeeded, mirroring writeLibraryEvent and
// writeUnmatchedEvent.
func writeImportListsEvent(w http.ResponseWriter, ctx context.Context, entries []projection.ImportListEntry) bool {
	if entries == nil {
		entries = []projection.ImportListEntry{}
	}

	var fragment bytes.Buffer
	if err := views.ImportListRows(entries).Render(ctx, &fragment); err != nil {
		logging.FromContext(ctx).Error("render import list rows for sse", "error", err)
		return false
	}

	var buf bytes.Buffer
	buf.WriteString("event: import-lists\n")
	for _, line := range bytes.Split(fragment.Bytes(), []byte{'\n'}) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')

	if _, err := w.Write(buf.Bytes()); err != nil {
		return false
	}
	return true
}

// defaultSubscribeLibrary is the Library page's fallback subscription
// (Task G3-3): it adapts Options.Library, a plain poll function, to the
// SubscribeLibrary shape /events/library consumes, by polling it on
// libraryPushInterval from a goroutine private to each subscription. There
// is no direct-Reader path to fall back to: Library's per-kind
// monitored/phase/hasFile derivation lives in ui/projection
// (describeLibraryItem), unexported, so the only thing this package can
// poll is the Library func itself.
func defaultSubscribeLibrary(library func(context.Context) []projection.LibraryItem) func() (<-chan []projection.LibraryItem, func()) {
	return pollingSubscribe(libraryPushInterval, library)
}

// defaultSubscribeUnmatched is [defaultSubscribeLibrary]'s Unmatched-page
// analogue, polling Options.Unmatched for the same reason: the
// LibraryScan-flattening logic lives in ui/projection, unexported, so
// Options.Unmatched itself is the only thing to poll.
func defaultSubscribeUnmatched(unmatched func(context.Context) []projection.UnmatchedEntry) func() (<-chan []projection.UnmatchedEntry, func()) {
	return pollingSubscribe(unmatchedPushInterval, unmatched)
}

// defaultSubscribeImportLists is [defaultSubscribeLibrary]'s Import Lists
// page analogue (Task G3-4), polling Options.ImportLists for the same
// reason: the ImportList-to-entry derivation lives in ui/projection,
// unexported, so Options.ImportLists itself is the only thing to poll.
func defaultSubscribeImportLists(
	importLists func(context.Context) []projection.ImportListEntry,
) func() (<-chan []projection.ImportListEntry, func()) {
	return pollingSubscribe(importListsPushInterval, importLists)
}

// pollingSubscribe adapts poll -- a plain per-call snapshot function -- to
// the Subscribe shape an SSE handler consumes, by polling it on interval
// from a goroutine private to each subscription. It is the shared
// implementation behind the default subscriptions: each call opens its own
// goroutine and its own ticker -- the per-connection re-poll Task D3-1
// replaced with ui/projection.Projection's shared broadcaster -- kept here
// only as the fallback for when nothing shares a single projection.
func pollingSubscribe[T any](interval time.Duration, poll func(context.Context) T) func() (<-chan T, func()) {
	return func() (<-chan T, func()) {
		ch := make(chan T, 1)
		ctx, cancel := context.WithCancel(context.Background())

		send := func() { publishSSE(ch, poll(ctx)) }
		send()

		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					send()
				}
			}
		}()

		return ch, cancel
	}
}

// publishSSE delivers v to ch without blocking, replacing whatever stale
// frame is already buffered rather than blocking the sender -- the same
// "newest projection is the only one worth delivering" rule
// ui/projection.Projection's own broadcaster uses (design plan, Task D3-1).
// It is needed here too: [pollingSubscribe]'s stop signal is ctx
// cancellation, not the handler draining ch, so an unbuffered or blocking
// send could hang a goroutine past the point its subscriber stopped
// reading. Generic over the payload so every default subscription shares
// the one implementation.
func publishSSE[T any](ch chan T, v T) {
	select {
	case ch <- v:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- v:
	default:
	}
}
