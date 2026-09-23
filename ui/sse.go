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
	"time"

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

// defaultSubscribe adapts entries -- a plain poll function, i.e.
// Options.Entries -- to the Subscribe shape /events/pipeline now consumes,
// by polling it on pipelinePushInterval from a goroutine private to each
// subscription. It exists so that Options.Entries alone (no
// Options.Subscribe) keeps behaving exactly as /events/pipeline did before
// Task D3-1 rewired the handler onto Subscribe: every test in this package
// that sets only Entries needs no changes, and a `clustarr ui` process that
// has not finished wiring its projection loop yet still streams something.
//
// Each call opens its own goroutine and its own ticker -- this is the
// per-connection re-poll Task D3-1 replaces in production, kept here only
// as the fallback for when nothing shares a single projection.
func defaultSubscribe(entries func(context.Context) []pipeline.Entry) func() (<-chan []pipeline.Entry, func()) {
	return func() (<-chan []pipeline.Entry, func()) {
		ch := make(chan []pipeline.Entry, 1)
		ctx, cancel := context.WithCancel(context.Background())

		send := func() { publishSSE(ch, entries(ctx)) }
		send()

		go func() {
			ticker := time.NewTicker(pipelinePushInterval)
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

// publishSSE delivers entries to ch without blocking, replacing whatever
// stale frame is already buffered rather than blocking the sender -- the
// same "newest projection is the only one worth delivering" rule
// ui/projection.Projection's own broadcaster uses (design plan, Task D3-1).
// It is needed here too: this fallback's stop signal is ctx cancellation,
// not the handler draining ch, so an unbuffered or blocking send could hang
// a goroutine past the point its subscriber stopped reading.
func publishSSE(ch chan []pipeline.Entry, entries []pipeline.Entry) {
	select {
	case ch <- entries:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- entries:
	default:
	}
}
