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

package engine

import (
	"time"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// gate is the commands one indexer deferred as a whole -- its grab limit, or
// the pause a 429 set, which every one of its releases waits out alike -- in
// the order they were deferred, behind one timer. When the wait ends the
// first is asked for again, and the next only once that one was not deferred
// again, so a pause's end costs the indexer an ask or two however many grabs
// wait on it; a command for the indexer that arrives meanwhile joins the
// queue without asking. On talos (2026-10-28) 1,165 nzb-life grabs each kept
// a timer of their own, all firing when the pause ended, and the index agent
// answered 15,403 refusals in 43 minutes for 12 grabs.
type gate struct {
	queue []schema.EngineCommand
	// until is when the wait ends and the timer drains the queue.
	until time.Time
	timer *time.Timer
	// draining is set while drainGate runs; asking is the entry it asks for,
	// and deferredAgain is set when the indexer defers that one again.
	draining      bool
	asking        string
	deferredAgain bool
}

// indexerOf is the indexer cmd's payload is fetched through,
// "<namespace>/<indexer>", or "" for a payload fetched any other way.
func indexerOf(cmd schema.EngineCommand) string {
	if cmd.Source == nil || cmd.Source.IndexerDownload == nil || cmd.Source.IndexerDownload.IndexerRef == "" {
		return ""
	}
	return cmd.Owner.Namespace + "/" + cmd.Source.IndexerDownload.IndexerRef
}

// queueLocked puts cmd in the queue: in its entry's place when the entry
// waits already (the newer command kept), else last.
func (g *gate) queueLocked(cmd schema.EngineCommand) {
	for i := range g.queue {
		if g.queue[i].Entry.UID == cmd.Entry.UID {
			if cmd.Seq >= g.queue[i].Seq {
				g.queue[i] = cmd
			}
			return
		}
	}
	g.queue = append(g.queue, cmd)
}

// removeLocked drops uid's command from the queue: the one at seq, or any
// when seq is 0.
func (g *gate) removeLocked(uid string, seq int64) {
	for i := range g.queue {
		if g.queue[i].Entry.UID == uid {
			if seq == 0 || g.queue[i].Seq == seq {
				g.queue = append(g.queue[:i], g.queue[i+1:]...)
			}
			return
		}
	}
}

// gateLocked is key's gate, made when it has none.
func (h *Handler) gateLocked(key string) *gate {
	if h.gates == nil {
		h.gates = map[string]*gate{}
	}
	g := h.gates[key]
	if g == nil {
		g = &gate{}
		h.gates[key] = g
	}
	return g
}

// deferIndexer queues cmd behind key's indexer, whose wait ends after wait,
// and reports how many commands wait on it.
func (h *Handler) deferIndexer(key string, cmd schema.EngineCommand, wait time.Duration) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.deferred[cmd.Entry.UID]; t != nil {
		t.Stop()
		delete(h.deferred, cmd.Entry.UID)
	}
	g := h.gateLocked(key)
	g.queueLocked(cmd)
	if g.asking == cmd.Entry.UID {
		g.deferredAgain = true
	}
	g.until = time.Now().Add(wait)
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(wait, func() { h.drainGate(key) })
	return len(g.queue)
}

// waitsOnIndexer queues cmd behind its indexer's wait when one has not
// ended: the indexer deferred every payload until then, so asking now is a
// refusal.
func (h *Handler) waitsOnIndexer(cmd schema.EngineCommand) bool {
	key := indexerOf(cmd)
	if key == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	g := h.gates[key]
	if g == nil || !time.Now().Before(g.until) {
		return false
	}
	if t := h.deferred[cmd.Entry.UID]; t != nil {
		t.Stop()
		delete(h.deferred, cmd.Entry.UID)
	}
	g.queueLocked(cmd)
	if g.asking == cmd.Entry.UID {
		g.deferredAgain = true
	}
	return true
}

// drainGate asks for the payloads key's indexer deferred, the first
// deferred first, until the indexer defers one again; that deferral's timer
// drains the rest.
func (h *Handler) drainGate(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	g := h.gates[key]
	if g == nil || g.draining {
		return
	}
	g.draining = true
	defer func() { g.draining, g.asking = false, "" }()
	for !time.Now().Before(g.until) {
		// A head superseded by a newer command for its entry is dropped
		// (superseded's rule).
		for len(g.queue) > 0 && g.queue[0].Seq < h.newest[g.queue[0].Entry.UID] {
			g.queue = g.queue[1:]
		}
		if len(g.queue) == 0 {
			if g.timer != nil {
				g.timer.Stop()
			}
			delete(h.gates, key)
			return
		}
		cmd := g.queue[0]
		g.asking, g.deferredAgain = cmd.Entry.UID, false
		h.mu.Unlock()
		h.retry(cmd)
		h.mu.Lock()
		if !g.deferredAgain {
			g.removeLocked(cmd.Entry.UID, cmd.Seq)
		}
	}
}
