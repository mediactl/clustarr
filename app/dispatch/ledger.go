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

package dispatch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/presence"
)

// Timing of the ledger (ADR-0019 §5.4, §5.5).
const (
	// ReservationTTL is how long an admission waits for its Published: a
	// reservation not confirmed by then lapses (the apply that would have
	// issued the seq failed, and the next pass decides again).
	ReservationTTL = 60 * time.Second
	// UnattendedAfter is how long a durable must look unattended before it
	// is (§5.5: no ack for two minutes).
	UnattendedAfter = 2 * time.Minute
	// TickInterval is the leader's period for lapses, unattended detection
	// and the waiting metrics.
	TickInterval = 10 * time.Second
	// WaitingTTL forgets a key the planner stopped reporting as waiting:
	// planners re-report on every pass of a waiting key (it requeues at
	// TickInterval), so a key not re-reported for this long is no longer
	// due.
	WaitingTTL = 5 * time.Minute
	// rebuildRetry spaces the rebuild's attempts at leader start.
	rebuildRetry = 10 * time.Second
)

// Reason is a catalogv1alpha1.DeliveryReason* value.
type Reason = string

// Key is a dispatching CR and, for a CR with several dispatches, the sub
// (an entry uid, "<entry uid>/import", "search": app/catalog/history's
// DispatchSub* conventions).
type Key struct{ Kind, Namespace, Name, Sub string }

// String is "<kind>/<namespace>/<name>[#<sub>]", for logs.
func (k Key) String() string {
	s := k.Kind + "/" + k.Namespace + "/" + k.Name
	if k.Sub != "" {
		s += "#" + k.Sub
	}
	return s
}

// Outstanding is one dispatch a CR's status says is in flight (seq >
// answeredSeq).
type Outstanding struct {
	Key Key
	Seq int64
}

// Source rebuilds one durable's outstanding dispatches from the cache.
type Source interface {
	Durable() string
	Rebuild(ctx context.Context, r client.Reader) ([]Outstanding, error)
}

// ConsumerStater reads one durable's state: the autoscale StateCache (5 s,
// leader-answered) in the manager.
type ConsumerStater interface {
	ConsumerState(ctx context.Context, stream, durable string) (events.ConsumerState, error)
}

// Options is what a Ledger reads.
type Options struct {
	Topology events.Topology
	States   ConsumerStater
	Presence *presence.Reader
	// Reader is the manager's cache, which Sources rebuild from.
	Reader client.Reader
	// Recorder is "clustarr-dispatch", recording on Pod.
	Recorder k8sevents.EventRecorder
	// Pod is the manager's own Pod ($POD_NAMESPACE/$POD_NAME); no Event is
	// recorded without its name.
	Pod types.NamespacedName
	Now func() time.Time
}

// slotState is a dispatch's place in the ledger.
type slotState int

const (
	reserved slotState = iota
	published
)

type slot struct {
	seq   int64
	state slotState
	// expires is when the slot lapses; zero never (a rebuildable durable's
	// published dispatch is removed by Answered or Forget only).
	expires time.Time
	// prev is the dispatch a reservation supersedes: when the reservation
	// lapses unpublished, the older dispatch is still in flight and comes
	// back.
	prev *slot
}

type waitingEntry struct {
	reason Reason
	at     time.Time
}

// Ledger is the dispatch ledger (ADR-0019 §5.4): per durable, the
// dispatches published and not yet answered, keyed by (Key, seq).
// Leader-local; see the package doc.
type Ledger struct {
	o Options

	mu         sync.Mutex
	sources    map[string]Source
	rebuilt    bool
	books      map[string]map[Key]*slot
	waiting    map[string]map[Key]waitingEntry
	attendance map[string]*attendance
}

// New is a Ledger over o. Register every Source, then add it to the manager.
func New(o Options) *Ledger {
	return &Ledger{
		o:          o,
		sources:    map[string]Source{},
		books:      map[string]map[Key]*slot{},
		waiting:    map[string]map[Key]waitingEntry{},
		attendance: map[string]*attendance{},
	}
}

// Register adds s, before Start: one per rebuildable durable. A second
// Source for one durable replaces the first.
func (l *Ledger) Register(s Source) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sources[s.Durable()] = s
}

// NeedLeaderElection makes the ledger leader-only: only the leader plans.
func (l *Ledger) NeedLeaderElection() bool { return true }

// Start rebuilds every registered durable from the cache (retrying until it
// can), then every TickInterval lapses reservations, detects unattended
// durables and sets the waiting and unattended metrics, until ctx ends.
func (l *Ledger) Start(ctx context.Context) error {
	log := logging.FromContext(ctx).With("component", "dispatch-ledger")
	for {
		err := l.rebuild(ctx)
		if err == nil {
			break
		}
		log.Warn("dispatch: could not rebuild the ledger from status; admission waits", "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(rebuildRetry):
		}
	}
	log.Info("dispatch: ledger rebuilt from status")
	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()
	for {
		l.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// rebuild reads every Source and replaces its durable's book with the
// published dispatches status names, then marks the ledger rebuilt.
func (l *Ledger) rebuild(ctx context.Context) error {
	l.mu.Lock()
	sources := make([]Source, 0, len(l.sources))
	for _, s := range l.sources {
		sources = append(sources, s)
	}
	l.mu.Unlock()
	books := map[string]map[Key]*slot{}
	for _, s := range sources {
		outs, err := s.Rebuild(ctx, l.o.Reader)
		if err != nil {
			return fmt.Errorf("rebuild %s: %w", s.Durable(), err)
		}
		book := map[Key]*slot{}
		for _, o := range outs {
			if cur, ok := book[o.Key]; ok && cur.seq >= o.Seq {
				continue
			}
			book[o.Key] = &slot{seq: o.Seq, state: published}
		}
		books[s.Durable()] = book
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for durable, book := range books {
		l.books[durable] = book
	}
	l.rebuilt = true
	return nil
}

// Rebuilt reports whether Start has rebuilt the ledger.
func (l *Ledger) Rebuilt() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rebuilt
}

// spec is durable's ConsumerSpec, the dynamic engine family's included.
func (l *Ledger) spec(durable string) (events.ConsumerSpec, bool) {
	if c, ok := l.o.Topology.Consumer(durable); ok {
		return c, true
	}
	if strings.HasPrefix(durable, events.EngineConsumerPrefix) {
		c := events.EngineConsumer("", 0)
		c.Name = durable
		return c, true
	}
	return events.ConsumerSpec{}, false
}

// Budget is 2 × MaxAckPending (§5.4): one full fleet in flight and one
// queued behind it. A durable outside the topology has none.
func (l *Ledger) Budget(durable string) int {
	c, ok := l.spec(durable)
	if !ok {
		return 0
	}
	return budgetOf(c)
}

func budgetOf(c events.ConsumerSpec) int { return 2 * max(c.MaxAckPending, 1) }

// lapseAfter is how long a published dispatch of a durable with no Source
// is remembered (ruling R23): AckWait × MaxDeliver of its spec.
func lapseAfter(c events.ConsumerSpec) time.Duration {
	ackWait := c.AckWait
	if ackWait <= 0 {
		ackWait = events.DefaultAckWait
	}
	return ackWait * time.Duration(max(c.MaxDeliver, 1))
}

// Published records that the publish of k at seq landed. A slot already
// at a later seq is kept.
func (l *Ledger) Published(durable string, k Key, seq int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	book := l.bookLocked(durable)
	if cur, ok := book[k]; ok && cur.seq > seq {
		return
	}
	s := &slot{seq: seq, state: published} // the superseded dispatch is answered by this one
	if _, rebuildable := l.sources[durable]; !rebuildable {
		if c, ok := l.spec(durable); ok {
			s.expires = l.now().Add(lapseAfter(c))
		}
	}
	book[k] = s
	l.clearWaitingLocked(durable, k)
}

// Answered records that the planner incorporated, or closed, k's dispatch
// at seq: every slot of k at or below seq goes.
func (l *Ledger) Answered(durable string, k Key, seq int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	book := l.bookLocked(durable)
	if cur, ok := book[k]; ok && cur.seq <= seq {
		delete(book, k)
	}
}

// Waiting records that the planner left k's block waiting with reason (a
// refused Admit, or its own NoCapableAgent or EngineNotReady), for the
// waiting metric.
func (l *Ledger) Waiting(durable string, k Key, reason Reason) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.waiting[durable]
	if w == nil {
		w = map[Key]waitingEntry{}
		l.waiting[durable] = w
	}
	w[k] = waitingEntry{reason: reason, at: l.now()}
}

// Forget drops every slot and waiting mark of k: the CR is gone.
func (l *Ledger) Forget(k Key) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, book := range l.books {
		delete(book, k)
	}
	for _, w := range l.waiting {
		delete(w, k)
	}
}

// WaitingCount is how many keys wait on durable: what QueueGauge exports
// as clustarr_dispatch_waiting.
func (l *Ledger) WaitingCount(durable string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lapseWaitingLocked(durable, l.now())
	return len(l.waiting[durable])
}

// Outstanding is how many dispatches of durable the ledger holds.
func (l *Ledger) Outstanding(durable string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	book := l.bookLocked(durable)
	l.lapseLocked(book, l.now())
	return len(book)
}

// Unattended reports durable's last detected attendance (§5.5).
func (l *Ledger) Unattended(durable string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unattendedLocked(durable)
}

func (l *Ledger) unattendedLocked(durable string) bool {
	a := l.attendance[durable]
	return a != nil && a.unattended
}

func (l *Ledger) bookLocked(durable string) map[Key]*slot {
	b := l.books[durable]
	if b == nil {
		b = map[Key]*slot{}
		l.books[durable] = b
	}
	return b
}

// lapseLocked drops every slot of book past its expiry, restoring the
// dispatch a lapsed reservation superseded.
func (l *Ledger) lapseLocked(book map[Key]*slot, now time.Time) {
	for k, s := range book {
		for s != nil && !s.expires.IsZero() && !now.Before(s.expires) {
			s = s.prev
		}
		if s == nil {
			delete(book, k)
		} else {
			book[k] = s
		}
	}
}

func (l *Ledger) lapseWaitingLocked(durable string, now time.Time) {
	for k, w := range l.waiting[durable] {
		if now.Sub(w.at) >= WaitingTTL {
			delete(l.waiting[durable], k)
		}
	}
}

func (l *Ledger) clearWaitingLocked(durable string, k Key) {
	if w := l.waiting[durable]; w != nil {
		delete(w, k)
	}
}

func (l *Ledger) now() time.Time {
	if l.o.Now != nil {
		return l.o.Now()
	}
	return time.Now()
}
