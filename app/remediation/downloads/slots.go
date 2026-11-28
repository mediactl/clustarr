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

package downloads

import (
	"context"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Late binding (spec 2026-11-27 §7): a grab on a usenet client with a
// bandwidth budget or several engines is assigned only when an engine has
// a transfer slot for it, in the order the engines themselves would run
// them: the higher priority class first, then the earliest chosen.
//
// The queue is status, not a book (architect's review, 2026-11-28, against
// ADR-0025's direction): a waiter is an entry that is Pending on its client
// with the NoSlot reason, its place its priority class and its grabbedAt,
// and both are read through dlindex, so any leader sees the same queue
// without every owner having passed first. Nothing polls: a grab that
// stops holding a slot wakes the next waiters in line, an engine's record
// wakes them when it shows room that was not there (a budget raised, an
// engine added or ready, fewer transfers), and a change to the queue or the
// holders wakes the DownloadClient controller, whose budgets and engine
// count follow them. A waiter's passes therefore do not grow
// with the queue.
//
// The one thing kept in memory is the assignments this leader made a
// moment ago, which count towards their engine until the cache shows them:
// a slot is counted from the manager's own assignments, never from the
// engine's report.
const (
	// slotAssignedFor is how long an assignment this leader made counts
	// towards its engine before the cache alone is believed.
	slotAssignedFor = time.Minute
	// slotWake is how many waiters a freed slot or an engine's new room
	// wakes, and slotWakeAfter how long after a freed slot: the pass that
	// freed it writes its status first.
	slotWake      = 4
	slotWakeAfter = 2 * time.Second
)

// slotWaiter is one grab waiting for a transfer slot.
type slotWaiter struct {
	uid   types.UID
	owner remediation.Key
	rank  int
	since time.Time
}

// before orders two waiters: the higher class, then the earlier chosen,
// then the uid.
func (a slotWaiter) before(b slotWaiter) bool {
	if a.rank != b.rank {
		return a.rank > b.rank
	}
	if !a.since.Equal(b.since) {
		return a.since.Before(b.since)
	}
	return a.uid < b.uid
}

// slotBook is this leader's recent assignments, by engine, and what each
// engine last reported. Lost with the leader, which costs nothing past
// slotAssignedFor and one wake an engine.
type slotBook struct {
	mu       sync.Mutex
	assigned map[string]map[types.UID]time.Time // engine → entry → when
	reported map[string]engineReport
}

// engineReport is the part of an engine record a waiter cares about.
type engineReport struct {
	ready     bool
	budgetSeq int64
	// load is the transfers the engine runs and queues.
	load int32
}

func newSlotBook() *slotBook {
	return &slotBook{assigned: map[string]map[types.UID]time.Time{}, reported: map[string]engineReport{}}
}

// roomChanged records an engine's report and tells whether it may have
// made room for a waiter: the first report this leader sees, an engine
// that became ready, a new budget, or fewer transfers than it last
// reported. A record that says the same as the last one -- an engine
// writes one a minute -- wakes nobody.
func (b *slotBook) roomChanged(engine string, r engineReport) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	last, seen := b.reported[engine]
	b.reported[engine] = r
	return !seen || (r.ready && !last.ready) || r.budgetSeq != last.budgetSeq || r.load < last.load
}

// assign records that this leader pinned uid to engine at now.
func (b *slotBook) assign(engine string, uid types.UID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.assigned[engine] == nil {
		b.assigned[engine] = map[types.UID]time.Time{}
	}
	b.assigned[engine][uid] = now
}

// recent lists the entries this leader pinned to engine within
// slotAssignedFor.
func (b *slotBook) recent(engine string, now time.Time) []types.UID {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []types.UID
	for uid, at := range b.assigned[engine] {
		if now.Sub(at) > slotAssignedFor {
			delete(b.assigned[engine], uid)
			continue
		}
		out = append(out, uid)
	}
	return out
}

// queue is client's waiters in namespace, in order, read from status
// through the cache's index.
func queue(ctx context.Context, r client.Reader, namespace, clientName string) ([]slotWaiter, error) {
	owned, err := dlindex.SlotWaiters(ctx, r, namespace, clientName)
	if err != nil {
		return nil, err
	}
	out := make([]slotWaiter, 0, len(owned))
	for i := range owned {
		k, ok := ownerKeyOf(owned[i].Owner)
		if !ok {
			continue
		}
		e := &owned[i].Entry
		out = append(out, slotWaiter{
			uid: types.UID(e.UID), owner: k, since: e.GrabbedAt.Time,
			rank: priorityRank(lifecycle.ParseIntents(owned[i].Annotations).Priority[e.ID]),
		})
	}
	slices.SortFunc(out, func(a, b slotWaiter) int {
		switch {
		case a.before(b):
			return -1
		case b.before(a):
			return 1
		}
		return 0
	})
	return out, nil
}

// head is the owners of the first n waiters of q, each once.
func head(q []slotWaiter, n int) []remediation.Key {
	var out []remediation.Key
	for _, w := range q {
		if len(out) == n {
			break
		}
		if !slices.Contains(out, w.owner) {
			out = append(out, w.owner)
		}
	}
	return out
}

// priorityRank orders the priority classes as the engines do.
func priorityRank(p string) int {
	switch commonv1.DownloadPriority(p) {
	case commonv1.DownloadPriorityHigh:
		return 2
	case commonv1.DownloadPriorityLow:
		return 0
	}
	return 1
}

// isPending reports an entry not yet assigned.
func isPending(e *catalogv1alpha1.DownloadEntry) bool {
	return e.Phase == "" || e.Phase == commonv1.DownloadPhasePending
}

// lateClients are the late-binding clients of v, by name.
func lateClients(v *lifecycle.View) map[string]lifecycle.Client {
	out := map[string]lifecycle.Client{}
	for _, c := range v.Clients {
		if c.LateBinding {
			out[c.Name] = c
		}
	}
	return out
}

// waiterOf is e, of owner ow, as a slot waiter: a grab not yet chosen a
// time stands from now.
func waiterOf(ow owner, v *lifecycle.View, e *catalogv1alpha1.DownloadEntry) slotWaiter {
	since := e.GrabbedAt.Time
	if since.IsZero() {
		since = v.Now
	}
	k, _ := ownerKeyOf(ow.ref)
	return slotWaiter{uid: types.UID(e.UID), owner: k, rank: priorityRank(v.Intents.Priority[e.ID]), since: since}
}

// slots fills what lifecycle needs to bind an owner's Pending entries late:
// each late-binding engine's pinned count, and how many waiters stand
// ahead of each entry. It reads nothing for an owner with no Pending
// entry, or a namespace with no late-binding client.
func (s *Stage) slots(ctx context.Context, ow owner, v *lifecycle.View) error {
	late := lateClients(v)
	if len(late) == 0 {
		return nil
	}
	var pending []*catalogv1alpha1.DownloadEntry
	for i := range v.Stored {
		if isPending(&v.Stored[i]) {
			pending = append(pending, &v.Stored[i])
		}
	}
	for i := range v.NewEntries {
		if isPending(&v.NewEntries[i]) {
			pending = append(pending, &v.NewEntries[i])
		}
	}
	if len(pending) == 0 {
		return nil
	}
	for _, c := range late {
		for o := range max(c.Replicas, 0) {
			name := lifecycle.EngineName(c.Name, o)
			eng, ok := v.Engines[name]
			if !ok {
				continue
			}
			holders, err := dlindex.SlotHolders(ctx, s.o.Reader, ow.ref.Namespace, name)
			if err != nil {
				return err
			}
			held := map[types.UID]bool{}
			for _, h := range holders {
				held[types.UID(h.Entry.UID)] = true
			}
			for _, uid := range s.slotBook.recent(name, v.Now) {
				held[uid] = true
			}
			eng.Pinned = int32(min(len(held), 1<<30))
			v.Engines[name] = eng
		}
	}
	v.Ahead = map[types.UID]int{}
	queues := map[string][]slotWaiter{}
	for _, e := range pending {
		// The client lifecycle will choose: the entry's own when it has
		// one, else the protocol's first.
		c, ok := late[e.Client]
		if !ok {
			chosen, found := lifecycle.ChooseClient(v.Clients, e.Release.Protocol, v.IndexerClient[e.Release.IndexerRef])
			if !found || !chosen.LateBinding {
				continue
			}
			c = chosen
		}
		q, ok := queues[c.Name]
		if !ok {
			var err error
			if q, err = queue(ctx, s.o.Reader, ow.ref.Namespace, c.Name); err != nil {
				return err
			}
			queues[c.Name] = q
		}
		me := waiterOf(ow, v, e)
		n := 0
		for _, w := range q {
			if w.uid != me.uid && w.before(me) {
				n++
			}
		}
		v.Ahead[types.UID(e.UID)] = n
	}
	return nil
}

// slotsAfter acts on what one owner's plan did to the slots: the
// assignments just made count towards their engines at once; a grab that
// stopped holding a slot wakes the next waiters in line; and any change to
// a client's waiters or holders wakes its DownloadClient, whose budgets
// and engine count follow them.
func (s *Stage) slotsAfter(ctx context.Context, ow owner, v *lifecycle.View, plan lifecycle.Plan) {
	late := lateClients(v)
	if len(late) == 0 {
		return
	}
	// What each entry was and is: waiting, holding, or neither.
	const (
		neither = iota
		waiting
		holding
	)
	state := func(e *catalogv1alpha1.DownloadEntry) int {
		switch {
		case dlindex.WaitsForSlot(e):
			return waiting
		case dlindex.HoldsSlot(e):
			return holding
		}
		return neither
	}
	before := map[string]*catalogv1alpha1.DownloadEntry{}
	for i := range v.Stored {
		before[v.Stored[i].UID] = &v.Stored[i]
	}
	changed, freed := map[string]bool{}, map[string]bool{}
	seen := map[string]bool{}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		seen[e.UID] = true
		was, had := before[e.UID]
		if _, ok := late[e.Client]; !ok {
			continue
		}
		if e.Engine != "" && (!had || was.Engine == "") {
			s.slotBook.assign(e.Engine, types.UID(e.UID), v.Now)
		}
		old := neither
		if had {
			old = state(was)
		}
		if now := state(e); now != old {
			changed[e.Client] = true
			if old == holding {
				freed[e.Client] = true
			}
		}
	}
	for uid, was := range before {
		if _, ok := late[was.Client]; ok && !seen[uid] && state(was) != neither {
			changed[was.Client] = true
			if state(was) == holding {
				freed[was.Client] = true
			}
		}
	}
	ns := ow.ref.Namespace
	for c := range freed {
		q, err := queue(ctx, s.o.Reader, ns, c)
		if err != nil {
			logging.FromContext(ctx).Warn("downloads: the waiters of a freed transfer slot", "client", c, "err", err)
			continue
		}
		s.slotWake.add(head(q, slotWake), slotWakeAfter)
	}
	if s.o.ClientWake != nil {
		for c := range changed {
			s.o.ClientWake(ns, c)
		}
	}
}

// wakeSource is a source the stage itself adds keys to: the waiters a
// freed slot wakes.
type wakeSource struct {
	mu    sync.Mutex
	queue workqueue.TypedRateLimitingInterface[remediation.Key]
}

var _ crsource.TypedSource[remediation.Key] = (*wakeSource)(nil)

// Start implements source.TypedSource: it keeps the queue.
func (w *wakeSource) Start(_ context.Context, q workqueue.TypedRateLimitingInterface[remediation.Key]) error {
	w.mu.Lock()
	w.queue = q
	w.mu.Unlock()
	return nil
}

// String implements fmt.Stringer for the controller's logs.
func (*wakeSource) String() string { return "downloads slot-waiter wake" }

// add enqueues keys after d; before the loop starts it does nothing, and
// the waiters' own rechecks find the slot.
func (w *wakeSource) add(keys []remediation.Key, d time.Duration) {
	w.mu.Lock()
	q := w.queue
	w.mu.Unlock()
	if q == nil {
		return
	}
	for _, k := range keys {
		q.AddAfter(k, d)
	}
}
