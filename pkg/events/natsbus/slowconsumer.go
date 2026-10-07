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

package natsbus

import (
	"context"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// Slow-consumer shedding (ADR-0020 §3). A core subscription whose
// client-side buffer is full has messages dropped by nats.go, which only
// counts them (Subscription.Dropped) and calls the async error handler once
// per stretch. The bus samples every core subscription's count into
// clustarr_nats_dropped_messages, and a responder that keeps dropping for
// SlowConsumerLeave leaves its queue group to its peers for
// SlowConsumerRejoin -- only when a peer exists.
const (
	// DefaultSlowConsumerLeave is how long a responder drops before it
	// leaves its queue group (WithSlowConsumerTimes).
	DefaultSlowConsumerLeave = 5 * time.Minute
	// DefaultSlowConsumerRejoin is how long it stays out.
	DefaultSlowConsumerRejoin = time.Minute

	// droppedSampleEvery is how often the bus reads every core
	// subscription's dropped count.
	droppedSampleEvery = 15 * time.Second
	// slowConsumerQuiet ends a dropping stretch: a responder that has
	// dropped nothing for this long is no longer dropping.
	slowConsumerQuiet = time.Minute
	// peerCountTimeout bounds one PeerCounter call.
	peerCountTimeout = 5 * time.Second
)

// PeerCounter reports how many other processes serve subject in queue group
// queue right now. A slow responder leaves its queue group only when it is
// above zero: where it is the only member, leaving would turn a slow verb
// into no verb at all (ADR-0020 §3).
//
// Nothing in clustarr knows this reliably today, so the default is nil, which
// counts none and keeps every responder subscribed: core NATS exposes a
// queue group's membership only through the system account's SUBSZ, which
// clustarr's clients do not hold, and an agent's presence key says the pod
// lives, not that its responder is still in the group -- two slow replicas
// would each read the other as a peer and leave together (ruling N3-1). An
// error counts as none.
type PeerCounter func(ctx context.Context, subject, queue string) (int, error)

// WithResponderPeers installs the PeerCounter a slow responder asks before
// it leaves its queue group. Nil (the default) counts no peer, so no
// responder ever leaves.
func WithResponderPeers(p PeerCounter) Option {
	return func(o *options) { o.peers = p }
}

// WithSlowConsumerTimes sets how long a responder drops before it leaves its
// queue group and how long it stays out. A zero value takes its default
// (DefaultSlowConsumerLeave, DefaultSlowConsumerRejoin).
func WithSlowConsumerTimes(leave, rejoin time.Duration) Option {
	return func(o *options) { o.slowLeave, o.slowRejoin = leave, rejoin }
}

func (o options) slowConsumerLeave() time.Duration {
	if o.slowLeave > 0 {
		return o.slowLeave
	}
	return DefaultSlowConsumerLeave
}

func (o options) slowConsumerRejoin() time.Duration {
	if o.slowRejoin > 0 {
		return o.slowRejoin
	}
	return DefaultSlowConsumerRejoin
}

// sampleDropped samples every core subscription's dropped count until the
// bus stops.
func (b *Bus) sampleDropped() {
	t := time.NewTicker(droppedSampleEvery)
	defer t.Stop()
	for {
		select {
		case <-b.serveCtx.Done():
			return
		case now := <-t.C:
			b.sampleOnce(now)
		}
	}
}

// sampleOnce sets clustarr_nats_dropped_messages for every responder and
// core subscription, summed by name, and lets each responder that has
// dropped for long enough leave its queue group.
func (b *Bus) sampleOnce(now time.Time) {
	b.mu.Lock()
	responders := append([]*responder(nil), b.responders...)
	cores := append([]*coreSub(nil), b.cores...)
	b.mu.Unlock()
	totals := map[string]float64{}
	for _, r := range responders {
		totals[r.subject] += float64(r.observeDropped(now))
	}
	for _, c := range cores {
		if n, err := c.sub.Dropped(); err == nil {
			totals[c.subject] += float64(n)
		}
	}
	for name, n := range totals {
		metrics.NATSDroppedMessages.WithLabelValues(name).Set(n)
	}
	for _, r := range responders {
		b.maybeLeave(r, now)
	}
}

// observeDropped reads r's dropped count -- every subscription it has held,
// since a rejoin starts a new one -- and keeps its dropping stretch: started
// by the first sample that sees a new drop, ended by slowConsumerQuiet
// without one.
func (r *responder) observeDropped(now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.droppedBase
	if r.sub != nil {
		if n, err := r.sub.Dropped(); err == nil {
			cur += n
		}
	}
	switch {
	case cur > r.lastDropped:
		r.lastDropAt = now
		if r.droppingSince.IsZero() {
			r.droppingSince = now
		}
	case !r.lastDropAt.IsZero() && now.Sub(r.lastDropAt) >= slowConsumerQuiet:
		r.droppingSince = time.Time{}
		r.stayLogged = false
	}
	r.lastDropped = cur
	return cur
}

// maybeLeave takes r out of its queue group when it has dropped for the
// leave time and a peer serves its verb: the old subscription drains (what
// it buffered is still answered) and r subscribes again after the rejoin
// time. A responder with no queue group, or no peer, stays.
func (b *Bus) maybeLeave(r *responder, now time.Time) {
	leave := b.opts.slowConsumerLeave()
	r.mu.Lock()
	due := !r.stopping && r.sub != nil && r.queue != "" &&
		!r.droppingSince.IsZero() && now.Sub(r.droppingSince) >= leave
	r.mu.Unlock()
	if !due {
		return
	}
	log := logging.FromContext(r.ctx)
	peers := 0
	if b.opts.peers != nil {
		ctx, cancel := context.WithTimeout(r.ctx, peerCountTimeout)
		n, err := b.opts.peers(ctx, r.subject, r.queue)
		cancel()
		if err != nil {
			log.Debug("bus: could not count a slow responder's peers; it stays", "subject", r.subject, "error", err)
		} else {
			peers = n
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if peers < 1 {
		if !r.stayLogged {
			r.stayLogged = true
			log.Warn("bus: responder keeps dropping requests but has no peer to leave them to; it stays in its queue group",
				"subject", r.subject, "queue", r.queue, "dropping_for", now.Sub(r.droppingSince).Round(time.Second))
		}
		return
	}
	if r.stopping || r.sub == nil {
		return
	}
	sub := r.sub
	if n, err := sub.Dropped(); err == nil {
		r.droppedBase += n
	}
	r.lastDropped = r.droppedBase
	r.sub = nil
	r.droppingSince, r.lastDropAt, r.stayLogged = time.Time{}, time.Time{}, false
	rejoin := b.opts.slowConsumerRejoin()
	log.Warn("bus: responder keeps dropping requests; leaving its queue group to its peers",
		"subject", r.subject, "queue", r.queue, "peers", peers, "rejoin_after", rejoin)
	done := make(chan struct{})
	r.leaving = done
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), b.stopBudget())
		defer cancel()
		_ = drainSubscription(ctx, sub)
	}()
	r.rejoin = time.AfterFunc(rejoin, func() { b.rejoin(r) })
}

// rejoin subscribes r to its queue group again, unless it stopped meanwhile;
// a failed subscribe is retried after the rejoin time.
func (b *Bus) rejoin(r *responder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping || r.sub != nil {
		return
	}
	log := logging.FromContext(r.ctx)
	sub, err := r.subscribe()
	if err != nil {
		log.Warn("bus: responder could not rejoin its queue group; retrying",
			"subject", r.subject, "queue", r.queue, "error", err)
		r.rejoin = time.AfterFunc(b.opts.slowConsumerRejoin(), func() { b.rejoin(r) })
		return
	}
	r.sub = sub
	r.lastDropped = r.droppedBase
	log.Info("bus: responder rejoined its queue group", "subject", r.subject, "queue", r.queue)
}
