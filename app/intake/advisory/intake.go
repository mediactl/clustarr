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

package advisory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	cataloghistory "github.com/mediactl/clustarr/app/catalog/history"
	historyworker "github.com/mediactl/clustarr/app/catalog/worker/history"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// CopyWait is how long a resolved MSG_TERMINATED waits for the DLQ
// projector to report the task's dead-letter copy before the intake
// annotates the CR itself (§8.5's second net).
const CopyWait = 60 * time.Second

// sweepInterval paces the pending-copy and index sweeps.
const sweepInterval = 5 * time.Second

// seenTTL keeps a reported copy's id: a copy is published before its term,
// so its report may arrive first.
const seenTTL = 10 * time.Minute

// wakeBuffer bounds Wakes; a full channel drops the wake (the CR's next
// pass reads the book anyway).
const wakeBuffer = 256

// advisory is the part of JetStream's JSConsumerDeliveryNakAdvisory and
// JSConsumerDeliveryTerminatedAdvisory (nats-server jetstream_events.go)
// the intake reads.
type advisory struct {
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	StreamSeq  uint64 `json:"stream_seq"`
	Deliveries uint64 `json:"deliveries"`
	Reason     string `json:"reason,omitempty"`
}

// pendingTerm is a terminated task awaiting its dead-letter copy.
type pendingTerm struct {
	resolved
	deadline time.Time
}

// Intake is the leader-only task-events intake (ADR-0019 §8.2).
type Intake struct {
	Bus   events.Bus
	Admin events.StreamAdmin
	Book  *dispatch.DeliveryBook
	// Topology is the installed one: the dispatched durables and their
	// MaxDeliver.
	Topology events.Topology
	// Projector, when set, is the manager's DLQ projector: its Seen reports
	// cancel the second net, and its Annotate is the second net. Nil runs
	// no second net.
	Projector *historyworker.DLQProjector
	// ByUID resolves a terminated task's Msg-Id UID that no nak indexed;
	// nil resolves none (ruling A2-2).
	ByUID UIDResolver
	// Now is a seam for tests; nil is time.Now.
	Now func() time.Time

	once    sync.Once
	wakes   chan cataloghistory.Target
	mu      sync.Mutex
	index   map[string]indexed
	seen    map[string]time.Time
	pending map[string]pendingTerm
}

func (i *Intake) init() {
	i.once.Do(func() {
		i.wakes = make(chan cataloghistory.Target, wakeBuffer)
		i.index = map[string]indexed{}
		i.seen = map[string]time.Time{}
		i.pending = map[string]pendingTerm{}
	})
}

// NeedLeaderElection makes the intake leader-only (§8.1).
func (i *Intake) NeedLeaderElection() bool { return true }

// Wakes is every CR whose delivery state the intake changed, for its loop
// key (A3.3 maps a target to a remediation key). The send never blocks.
func (i *Intake) Wakes() <-chan cataloghistory.Target {
	i.init()
	return i.wakes
}

// Start binds clustarr-task-events and the ack-metric subjects, and runs
// the second net, until ctx ends.
func (i *Intake) Start(ctx context.Context) error {
	i.init()
	if i.Bus == nil || i.Admin == nil || i.Book == nil {
		return errors.New("advisory: the intake needs the bus, its stream admin and the delivery book")
	}
	spec, ok := i.Topology.Consumer(events.ConsumerTaskEvents)
	if !ok {
		return fmt.Errorf("advisory: %s is not in the topology", events.ConsumerTaskEvents)
	}
	stop, err := i.Bus.Subscribe(ctx, spec.Subscription(), i.Handle)
	if err != nil {
		return fmt.Errorf("advisory: subscribe %s: %w", events.ConsumerTaskEvents, err)
	}
	defer stop()
	stopAcks := i.subscribeAcks(ctx)
	defer stopAcks()
	i.sweep(ctx)
	return nil
}

// Handle settles one advisory. Every advisory is acked: it is history, not
// state (§8.4), and a retried one would only be dead-lettered into
// CLUSTARR_DLQ, where it concerns no CR.
func (i *Intake) Handle(ctx context.Context, m events.Message) error {
	i.init()
	var adv advisory
	if err := json.Unmarshal(m.Envelope().Data, &adv); err != nil {
		count("", "", outcomeUnresolved)
		return nil
	}
	subject := m.Subject()
	switch {
	case strings.HasPrefix(subject, events.SubjectNakAdvisoryPrefix+"."):
		i.naked(ctx, adv)
		return nil
	case strings.HasPrefix(subject, events.SubjectTermAdvisoryPrefix+"."):
		i.terminated(ctx, adv)
		return nil
	}
	count(adv.Consumer, "", outcomeUnresolved)
	return nil
}

// naked records a nak: the message is still in its stream, read by
// sequence and resolved; only a dispatch's first nak and the nak at
// MaxDeliver - 1 are recorded.
func (i *Intake) naked(ctx context.Context, adv advisory) {
	subject, env, err := i.Admin.Message(ctx, adv.Stream, adv.StreamSeq)
	if err != nil {
		// Gone (acked, termed or purged since the nak) or unreadable: the
		// nak's visibility is lost, nothing else.
		if !errors.Is(err, events.ErrMessageNotFound) {
			logging.FromContext(ctx).Debug("advisory: could not read a naked message", "stream", adv.Stream,
				"seq", adv.StreamSeq, "error", err)
		}
		count(adv.Consumer, kindNak, outcomeUnresolved)
		return
	}
	t, seq, sub, ok := cataloghistory.ResolveDispatch(subject, env)
	if !ok {
		count(adv.Consumer, kindNak, outcomeUnresolved)
		return
	}
	now := i.now()
	r := resolved{target: t, seq: seq, sub: sub}
	i.remember(env.ID, r, now)
	k := r.key()
	if i.Book.Newer(k, seq) {
		count(adv.Consumer, kindNak, outcomeStale)
		return
	}
	last := adv.Deliveries > 0 && int(adv.Deliveries) == i.maxDeliver(adv.Stream, adv.Consumer)-1
	if i.Book.Seen(k, seq) && !last {
		count(adv.Consumer, kindNak, outcomeMetricsOnly)
		return
	}
	i.Book.Retrying(k, seq, int32(min(adv.Deliveries, 1<<31-1)), now, last)
	count(adv.Consumer, kindNak, outcomeRecorded)
	i.wake(t)
}

// terminated records a term: the message is gone, so the reason's first
// field, the task's Clustarr-Id (R10), resolves it.
func (i *Intake) terminated(ctx context.Context, adv advisory) {
	id, _, _ := strings.Cut(adv.Reason, " ")
	if id == "" {
		count(adv.Consumer, kindTerm, outcomeUnresolved)
		return
	}
	r, ok := i.resolveTerm(ctx, id)
	if !ok {
		count(adv.Consumer, kindTerm, outcomeUnresolved)
		return
	}
	k := r.key()
	if i.Book.Newer(k, r.seq) {
		count(adv.Consumer, kindTerm, outcomeStale)
		return
	}
	now := i.now()
	i.Book.DeadLettered(k, r.seq, now)
	count(adv.Consumer, kindTerm, outcomeRecorded)
	i.wake(r.target)
	if i.Projector == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, copied := i.seen[id]; copied {
		return
	}
	i.pending[id] = pendingTerm{resolved: r, deadline: now.Add(CopyWait)}
}

// sweep runs the second net until ctx ends: a DLQ copy the projector
// reports cancels its pending term; a pending term past CopyWait is
// annotated through the projector's code (§8.5). It also expires the
// Msg-Id index and the reported copies.
func (i *Intake) sweep(ctx context.Context) {
	var seenCh <-chan string
	if i.Projector != nil {
		seenCh = i.Projector.Seen()
	}
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-seenCh:
			i.mu.Lock()
			i.seen[id] = i.now()
			delete(i.pending, id)
			i.mu.Unlock()
		case <-ticker.C:
			for _, p := range i.due() {
				if err := i.Projector.Annotate(ctx, p.target, p.seq); err != nil {
					logging.FromContext(ctx).Warn("advisory: could not annotate a terminated task's CR",
						"kind", p.target.Kind, "namespace", p.target.Namespace, "name", p.target.Name, "error", err)
				}
			}
		}
	}
}

// due removes and returns every pending term past its deadline, and
// expires the index and the reported copies.
func (i *Intake) due() []pendingTerm {
	now := i.now()
	i.mu.Lock()
	defer i.mu.Unlock()
	var out []pendingTerm
	for id, p := range i.pending {
		if !now.Before(p.deadline) {
			out = append(out, p)
			delete(i.pending, id)
		}
	}
	for id, at := range i.seen {
		if now.Sub(at) >= seenTTL {
			delete(i.seen, id)
		}
	}
	for id, e := range i.index {
		if now.Sub(e.at) >= indexTTL {
			delete(i.index, id)
		}
	}
	return out
}

// maxDeliver is the MaxDeliver of a durable: the topology's, or the dynamic
// transcode and engine families'.
func (i *Intake) maxDeliver(stream, durable string) int {
	if c, ok := i.Topology.Consumer(durable); ok {
		return c.MaxDeliver
	}
	switch stream {
	case events.StreamWorkTranscode:
		return events.TranscodeTaskConsumer("", "").MaxDeliver
	case events.StreamWorkEngine:
		return events.EngineConsumer("", 0).MaxDeliver
	}
	return 0
}

// wake sends t on Wakes without blocking.
func (i *Intake) wake(t cataloghistory.Target) {
	select {
	case i.wakes <- t:
	default:
	}
}

func (i *Intake) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}
