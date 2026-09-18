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

	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui/views"
)

// pipelinePushInterval is how often /events/pipeline re-sends the projection
// to a connected client. §A3.4 calls for "SSE per row"; this skeleton polls
// Options.Entries on an interval rather than pushing on a real change event,
// which is enough to prove the endpoint end to end before a later task wires
// it to the informer caches that would make it push-driven.
const pipelinePushInterval = 5 * time.Second

// handlePipelineEvents streams the pipeline projection as Server-Sent
// Events. Each event's data is the same HTML fragment views.PipelineRows
// renders for the initial GET /pipeline -- the Pipeline page wires
// hx-ext="sse" sse-connect="/events/pipeline" sse-swap="pipeline" onto
// #pipeline-rows, and htmx's SSE extension swaps that element's innerHTML
// with the named event's data verbatim, so the payload must already be
// rendered markup, not JSON a browser would need extra script to turn into
// DOM.
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
	if !s.writePipelineEvent(w, ctx) {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(pipelinePushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.writePipelineEvent(w, ctx) {
				return
			}
			flusher.Flush()
		}
	}
}

// writePipelineEvent writes one "pipeline" SSE event and reports whether the
// write succeeded; a false return means the client is gone and the caller
// should stop.
func (s *Server) writePipelineEvent(w http.ResponseWriter, ctx context.Context) bool {
	entries := s.opts.Entries(ctx)
	if entries == nil {
		entries = []pipeline.Entry{}
	}

	var fragment bytes.Buffer
	if err := views.PipelineRows(entries).Render(ctx, &fragment); err != nil {
		s.logger.Error("render pipeline rows for sse", "error", err)
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
