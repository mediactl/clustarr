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

	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui/paging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// pipelinePushInterval is how often the fallback [defaultSubscribe] polls
// Entries when Options.Subscribe is left nil. It only runs when no shared
// ui/projection.Projection is wired -- see that field's doc comment -- so it
// no longer governs every open connection in production the way it did
// before Task D3-1.
const pipelinePushInterval = 5 * time.Second

// downloadsPushInterval is [defaultSubscribeDownloads]'s own analogue of
// pipelinePushInterval: how often it polls Options.Reader directly when
// Options.SubscribeDownloads is left nil, i.e. when nothing has wired a
// shared ui/projection.Projection.
const downloadsPushInterval = 5 * time.Second

// libraryPushInterval is [defaultSubscribeLibrary]'s analogue of
// pipelinePushInterval: how often it polls Options.Library when
// Options.SubscribeLibrary is left nil.
const libraryPushInterval = 5 * time.Second

// unmatchedPushInterval is [defaultSubscribeUnmatched]'s analogue of
// pipelinePushInterval: how often it polls Options.Unmatched when
// Options.SubscribeUnmatched is left nil.
const unmatchedPushInterval = 5 * time.Second

// importListsPushInterval is [defaultSubscribeImportLists]'s analogue of
// pipelinePushInterval: how often it polls Options.ImportLists when
// Options.SubscribeImportLists is left nil.
const importListsPushInterval = 5 * time.Second

// handlePipelineEvents streams the pipeline projection as Server-Sent
// Events. Each event's data is the same HTML fragment views.PipelineRows
// renders for the initial GET /pipeline -- the Pipeline page wires
// hx-ext="sse" sse-connect="/events/pipeline" sse-swap="pipeline" onto
// #pipeline-rows, and htmx's SSE extension swaps that element's innerHTML
// with the named event's data verbatim, so the payload must already be
// rendered markup, not JSON a browser would need extra script to turn into
// DOM.
//
// The handler itself no longer ticks: it subscribes once via
// Options.Subscribe and relays whatever arrives until the client goes away.
// In production Options.Subscribe is a *projection.Projection shared by
// every open connection (design plan ruling R4); Options.Entries alone
// (nil Subscribe) still works through [defaultSubscribe]'s per-connection
// poll.
func (s *Server) handlePipelineEvents(w http.ResponseWriter, r *http.Request) {
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
	ch, unsubscribe := s.opts.Subscribe()
	defer unsubscribe()

	// The page's own ?page and ?per, re-clamped against every push: the
	// list can grow or shrink under an open stream, and the frame must be
	// the page the reader is looking at, not page 1.
	want := paging.Parse(r.URL.Query())
	for {
		select {
		case <-ctx.Done():
			return
		case entries := <-ch:
			p := want.Page(len(entries))
			if !writePipelineEvent(w, ctx, p, paging.Window(entries, p)) {
				return
			}
			flusher.Flush()
		}
	}
}

// writePipelineEvent writes one "pipeline" SSE event for entries and
// reports whether the write succeeded; a false return means the client is
// gone and the caller should stop.
func writePipelineEvent(w http.ResponseWriter, ctx context.Context, p paging.Page, entries []pipeline.Entry) bool {
	if entries == nil {
		entries = []pipeline.Entry{}
	}

	var fragment bytes.Buffer
	if err := views.PipelineList(p, entries).Render(ctx, &fragment); err != nil {
		logging.FromContext(ctx).Error("render pipeline rows for sse", "error", err)
		return false
	}

	// SSE data may not contain a bare newline within one field, and
	// PipelineRows renders multi-line HTML, so the fragment is split into
	// one "data:" line per source line per the spec; the client-side
	// concatenation of those lines with '\n' reassembles the original
	// markup exactly.
	var buf bytes.Buffer
	buf.WriteString("event: pipeline\n")
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

// handleDownloadsEvents streams the downloads projection as Server-Sent
// Events, the Downloads page's counterpart to handlePipelineEvents. Each
// event's data is the same HTML fragment views.DownloadRows renders for the
// initial GET /downloads inside #downloads-rows -- the Downloads page wires
// hx-ext="sse" sse-connect="/events/downloads" sse-swap="downloads" onto
// that element, and htmx's SSE extension swaps its innerHTML with the named
// event's data verbatim, so the payload must already be rendered markup
// (ui/views/downloads.templ's own doc comment on Downloads makes the same
// point about DownloadRows' contract).
//
// Like handlePipelineEvents, this subscribes once via
// Options.SubscribeDownloads and relays whatever arrives until the client
// goes away. In production Options.SubscribeDownloads is backed by the same
// *projection.Projection as Options.Subscribe, ticking once and feeding
// both streams from the one list round (design plan ruling R4, Task D3-3);
// a nil Options.SubscribeDownloads falls back to [defaultSubscribeDownloads],
// a per-connection poll of Options.Reader.
func (s *Server) handleDownloadsEvents(w http.ResponseWriter, r *http.Request) {
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
	ch, unsubscribe := s.opts.SubscribeDownloads()
	defer unsubscribe()

	want := paging.Parse(r.URL.Query())
	// The rows change when an entry's status does; the transfer telemetry
	// beside them every second. A frame goes out on either: a new set of
	// rows, or progressPushInterval with rows in flight.
	ticker := time.NewTicker(progressPushInterval)
	defer ticker.Stop()
	var rows []projection.DownloadRow
	for {
		select {
		case <-ctx.Done():
			return
		case rows = <-ch:
		case <-ticker.C:
			if !anyMoving(rows) {
				continue
			}
		}
		p := want.Page(len(rows))
		if !writeDownloadsEvent(w, ctx, p, s.downloadViews(ctx, paging.Window(rows, p))) {
			return
		}
		flusher.Flush()
	}
}

// progressPushInterval is how often /events/downloads re-renders a page
// with a transfer in flight, for its clustarr-progress telemetry.
const progressPushInterval = 2 * time.Second

// anyMoving reports a row whose transfer telemetry moves.
func anyMoving(rows []projection.DownloadRow) bool {
	for i := range rows {
		switch rows[i].Entry.Phase {
		case commonv1.DownloadPhaseQueued, commonv1.DownloadPhaseDownloading, commonv1.DownloadPhaseSeeding:
			return true
		}
	}
	return false
}

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

// writeDownloadsEvent writes one "downloads" SSE event for downloads and
// reports whether the write succeeded; a false return means the client is
// gone and the caller should stop. Its framing is writePipelineEvent's,
// unchanged: SSE forbids a bare newline within one field, so
// views.DownloadRows' multi-line HTML becomes one "data:" line per source
// line, and the client-side concatenation of those lines with '\n'
// reassembles the fragment exactly -- the same fragment #downloads-rows was
// initially rendered with, which is the D3-2/D3-3 markup contract this
// stream exists to satisfy.
func writeDownloadsEvent(w http.ResponseWriter, ctx context.Context, p paging.Page, downloads []views.DownloadView) bool {
	if downloads == nil {
		downloads = []views.DownloadView{}
	}

	var fragment bytes.Buffer
	if err := views.DownloadList(p, downloads).Render(ctx, &fragment); err != nil {
		logging.FromContext(ctx).Error("render download rows for sse", "error", err)
		return false
	}

	var buf bytes.Buffer
	buf.WriteString("event: downloads\n")
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

// handleLibraryEvents streams the Library page's projection as Server-Sent
// Events, the Library page's counterpart to handlePipelineEvents and
// handleDownloadsEvents. Each event's data is the same HTML fragment
// views.LibraryRows renders for the initial GET /library inside
// #library-rows; the Library page wires hx-ext="sse"
// sse-connect="/events/library" sse-swap="library" onto that element, and
// htmx's SSE extension swaps its innerHTML with the named event's data
// verbatim, so the payload must already be rendered markup.
//
// Like handlePipelineEvents, this subscribes once via Options.SubscribeLibrary
// and relays whatever arrives until the client goes away. In production
// Options.SubscribeLibrary is backed by the same *projection.Projection as
// Subscribe and SubscribeDownloads -- one list round feeding every stream
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

// defaultSubscribe adapts entries -- a plain poll function, i.e.
// Options.Entries -- to the Subscribe shape /events/pipeline now consumes,
// by polling it on pipelinePushInterval from a goroutine private to each
// subscription. It exists so that Options.Entries alone (no
// Options.Subscribe) keeps behaving exactly as /events/pipeline did before
// Task D3-1 rewired the handler onto Subscribe: every test in this package
// that sets only Entries needs no changes, and a `clustarr ui` process that
// has not finished wiring its projection loop yet still streams something.
func defaultSubscribe(entries func(context.Context) []pipeline.Entry) func() (<-chan []pipeline.Entry, func()) {
	return pollingSubscribe(pipelinePushInterval, entries)
}

// defaultSubscribeDownloads is [defaultSubscribe]'s Downloads-page
// counterpart: with no shared ui/projection.Projection wired it polls
// Options.Reader through projection.ListDownloadRows on
// downloadsPushInterval, from a goroutine private to each subscription.
func defaultSubscribeDownloads(reader client.Reader) func() (<-chan []projection.DownloadRow, func()) {
	return pollingSubscribe(downloadsPushInterval, func(ctx context.Context) []projection.DownloadRow {
		return pollDownloadsOnly(ctx, reader)
	})
}

// defaultSubscribeLibrary is [defaultSubscribe]'s Library-page counterpart
// (Task G3-3): it adapts Options.Library, a plain poll function, to the
// SubscribeLibrary shape /events/library consumes, by polling it on
// libraryPushInterval from a goroutine private to each subscription. Unlike
// [defaultSubscribeDownloads], there is no separate direct-Reader path to
// fall back to: Library's per-kind monitored/phase/hasFile derivation lives
// in ui/projection (describeLibraryItem), unexported, so the only thing this
// package can poll is the Library func itself -- exactly [defaultSubscribe]'s
// shape for Entries.
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

// pollDownloadsOnly builds the grab rows through reader, tolerating a nil
// reader or a List error as "nothing to show": this is a best-effort
// fallback poller with no caller to report an error to.
func pollDownloadsOnly(ctx context.Context, reader client.Reader) []projection.DownloadRow {
	rows, err := projection.ListDownloadRows(ctx, reader)
	if err != nil {
		logging.FromContext(ctx).Error("list grab rows for sse fallback poll", "error", err)
		return nil
	}
	return rows
}

// pollingSubscribe adapts poll -- a plain per-call snapshot function -- to
// the Subscribe shape an SSE handler consumes, by polling it on interval
// from a goroutine private to each subscription. It is the shared
// implementation behind both [defaultSubscribe] and
// [defaultSubscribeDownloads]: each call opens its own goroutine and its own
// ticker -- the per-connection re-poll Task D3-1 replaced, for the pipeline
// stream, with ui/projection.Projection's shared broadcaster -- kept here
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
// reading. Generic over the payload so both defaultSubscribe's
// []pipeline.Entry and defaultSubscribeDownloads's []projection.DownloadRow
// share the one implementation.
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
