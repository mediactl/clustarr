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

// Package presence is agent presence (ADR-0019 §5.3): every agent process
// and cmd/markers write one key in clustarr-progress at start and every
// Interval, and the manager's admission (app/dispatch) reads them through a
// short cache. A presence key is liveness telemetry, not a record: one
// writer per key (the pod), rebuilt within Interval of any loss, retired by
// the bucket's TTL when the pod is gone.
//
// It imports pkg/events and its schema only (and the context logger): cmd/markers
// links it and links no Kubernetes client.
package presence

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Interval is how often a Writer rewrites its key (design §5.3). The
// bucket's 10 min TTL retires the key of a pod that stopped writing.
const Interval = 30 * time.Second

// DefaultReaderTTL is how long a Reader serves one read of the bucket.
const DefaultReaderTTL = 5 * time.Second

// deleteTimeout bounds the best-effort delete of a Writer's key at stop.
const deleteTimeout = 5 * time.Second

// Writer reports one agent process (ADR-0019 §5.3).
type Writer struct {
	// KV is clustarr-progress (events.BucketProgress).
	KV events.KV
	// Domain is the agent domain ("catalog", "import", ..., "markers").
	Domain string
	// Pod is this process's pod name.
	Pod string
	// Node is $NODE_NAME, "" when the installer does not set it.
	Node    string
	Version string
	// Slots are the handler slots per bound durable (events.SlotsFor).
	Slots map[string]int
	// Durables are the durables the process binds.
	Durables []string
	// Capabilities, when set, is asked at every write: what the process
	// proved it can do ("ffmpeg": "9", "data": "mounted", ...).
	Capabilities func() map[string]string
	// Now is a seam for tests; nil is time.Now.
	Now func() time.Time
	// Interval overrides the package Interval when above zero (tests).
	Interval time.Duration
}

// Key is the writer's key, events.PresenceKey(Domain, Pod).
func (w *Writer) Key() string { return events.PresenceKey(w.Domain, w.Pod) }

// Run writes the presence key now and every Interval until ctx ends, then
// deletes it (best effort: the bucket's TTL retires it otherwise). A failed
// write is logged and retried at the next tick; Run returns nil when ctx
// ends, and an error only for a writer it cannot run at all.
func (w *Writer) Run(ctx context.Context) error {
	if w.KV == nil || w.Domain == "" || w.Pod == "" {
		return errors.New("presence: a writer needs the progress bucket, a domain and a pod name")
	}
	interval := w.Interval
	if interval <= 0 {
		interval = Interval
	}
	log := logging.FromContext(ctx).With("domain", w.Domain, "pod", w.Pod)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.write(ctx); err != nil && ctx.Err() == nil {
			log.Warn("presence: could not write the presence key", "error", err)
		}
		select {
		case <-ctx.Done():
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deleteTimeout)
			if err := w.KV.Delete(dctx, w.Key()); err != nil {
				log.Debug("presence: could not delete the presence key at stop", "error", err)
			}
			cancel()
			return nil
		case <-ticker.C:
		}
	}
}

// Report is the value the writer puts now.
func (w *Writer) Report() schema.AgentPresence {
	p := schema.AgentPresence{
		Domain:   w.Domain,
		Pod:      w.Pod,
		Node:     w.Node,
		Version:  w.Version,
		Slots:    maps.Clone(w.Slots),
		Durables: slices.Clone(w.Durables),
		At:       w.now().UTC(),
	}
	if w.Capabilities != nil {
		p.Capabilities = w.Capabilities()
	}
	return p
}

func (w *Writer) write(ctx context.Context) error {
	data, err := json.Marshal(w.Report())
	if err != nil {
		return err
	}
	_, err = w.KV.Put(ctx, w.Key(), data)
	return err
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Reader is the manager's cached view of every presence key (design §5.3,
// §8.1: reads through a 5 s cache, no watch). One read lists the bucket's
// keys and gets each presence key; every call within TTL is served from it.
type Reader struct {
	// KV is clustarr-progress.
	KV events.KV
	// TTL is how long one read serves; 0 is DefaultReaderTTL.
	TTL time.Duration
	// Now is a seam for tests; nil is time.Now.
	Now func() time.Time

	mu    sync.Mutex
	at    time.Time
	byDom map[string][]schema.AgentPresence
}

// Domain returns every live presence report of domain, sorted by pod.
func (r *Reader) Domain(ctx context.Context, domain string) ([]schema.AgentPresence, error) {
	all, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return slices.Clone(all[events.KVKeyToken(domain)]), nil
}

// Present reports whether any pod of domain is present.
func (r *Reader) Present(ctx context.Context, domain string) (bool, error) {
	ps, err := r.Domain(ctx, domain)
	return len(ps) > 0, err
}

// Capable reports whether a present pod of domain reports capability with
// a non-empty value (design §5.5's NoCapableAgent is a false here).
func (r *Reader) Capable(ctx context.Context, domain, capability string) (bool, error) {
	ps, err := r.Domain(ctx, domain)
	if err != nil {
		return false, err
	}
	for _, p := range ps {
		if p.Capabilities[capability] != "" {
			return true, nil
		}
	}
	return false, nil
}

// snapshot is the cached bucket read, keyed by the domain's key token.
func (r *Reader) snapshot(ctx context.Context) (map[string][]schema.AgentPresence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ttl := r.TTL
	if ttl <= 0 {
		ttl = DefaultReaderTTL
	}
	now := r.now()
	if r.byDom != nil && now.Sub(r.at) < ttl {
		return r.byDom, nil
	}
	keys, err := r.KV.Keys(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]schema.AgentPresence{}
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, events.PresencePrefix)
		if !ok {
			continue
		}
		dom, _, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		e, err := r.KV.Get(ctx, k)
		if err != nil {
			if errors.Is(err, events.ErrKeyNotFound) {
				continue // expired or deleted since the listing
			}
			return nil, err
		}
		var p schema.AgentPresence
		if err := json.Unmarshal(e.Value, &p); err != nil {
			continue // not a presence report: ignore rather than fail admission
		}
		out[dom] = append(out[dom], p)
	}
	for _, ps := range out {
		slices.SortFunc(ps, func(a, b schema.AgentPresence) int { return strings.Compare(a.Pod, b.Pod) })
	}
	r.byDom, r.at = out, now
	return out, nil
}

func (r *Reader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
