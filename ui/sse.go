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
	"net/http"
	"sort"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/pipeline"
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

	for {
		select {
		case <-ctx.Done():
			return
		case entries := <-ch:
			if !writePipelineEvent(w, ctx, entries) {
				return
			}
			flusher.Flush()
		}
	}
}

// writePipelineEvent writes one "pipeline" SSE event for entries and
// reports whether the write succeeded; a false return means the client is
// gone and the caller should stop.
func writePipelineEvent(w http.ResponseWriter, ctx context.Context, entries []pipeline.Entry) bool {
	if entries == nil {
		entries = []pipeline.Entry{}
	}

	var fragment bytes.Buffer
	if err := views.PipelineRows(entries).Render(ctx, &fragment); err != nil {
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

	for {
		select {
		case <-ctx.Done():
			return
		case downloads := <-ch:
			if !writeDownloadsEvent(w, ctx, downloads) {
				return
			}
			flusher.Flush()
		}
	}
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
func writeDownloadsEvent(w http.ResponseWriter, ctx context.Context, downloads []downloadv1.Download) bool {
	if downloads == nil {
		downloads = []downloadv1.Download{}
	}

	var fragment bytes.Buffer
	if err := views.DownloadRows(downloads).Render(ctx, &fragment); err != nil {
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

// defaultSubscribeDownloads is [defaultSubscribe]'s Task D3-3 counterpart:
// it adapts a direct read of Options.Reader to the SubscribeDownloads shape
// /events/downloads consumes, by polling on downloadsPushInterval from a
// goroutine private to each subscription. It exists so that Options.Reader
// alone (no Options.SubscribeDownloads, i.e. no shared
// ui/projection.Projection wired) still streams something -- every test in
// this package that sets only Reader, and a `clustarr ui` process too
// short-lived to have wired the projection loop yet.
//
// Unlike defaultSubscribe, there is no plain poll func on Options to adapt:
// D3-2's GET /downloads reads Options.Reader directly through the Server's
// own listDownloads method, which this package-level func cannot call (it
// runs from [NewServer], before a *Server exists), so it lists Download
// itself instead.
func defaultSubscribeDownloads(reader client.Reader) func() (<-chan []downloadv1.Download, func()) {
	return pollingSubscribe(downloadsPushInterval, func(ctx context.Context) []downloadv1.Download {
		return pollDownloadsOnly(ctx, reader)
	})
}

// pollDownloadsOnly lists every Download through reader, sorted by name to
// match ui/routes.go's listDownloads ordering, tolerating a nil reader (no
// cluster configured) or a List error the same way listDownloads does: as
// "nothing to show" rather than a failure, since this is a best-effort
// fallback poller, not a request handler with a caller to report an error
// to.
func pollDownloadsOnly(ctx context.Context, reader client.Reader) []downloadv1.Download {
	if reader == nil {
		return nil
	}

	var list downloadv1.DownloadList
	if err := reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list downloads for sse fallback poll", "error", err)
		return nil
	}
	downloads := list.Items
	sort.Slice(downloads, func(i, j int) bool { return downloads[i].Name < downloads[j].Name })
	return downloads
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
// []pipeline.Entry and defaultSubscribeDownloads's []downloadv1.Download
// (Task D3-3) share the one implementation.
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
