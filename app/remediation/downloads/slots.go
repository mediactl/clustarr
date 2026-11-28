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
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
)

// Late binding (spec 2026-11-27 §7): a grab on a usenet client with a
// bandwidth budget or several engines is assigned only when an engine has
// a transfer slot for it, in the order the engines themselves would run
// them: the higher priority class first, then the longest waiting. The
// order spans owners, which one owner's pass cannot see, so the stage
// keeps a book of the waiters; and a slot is counted from the manager's
// own assignments, the cache's and the ones this leader made a moment ago,
// never from the engine's report.
const (
	// slotAssignedFor is how long an assignment this leader made counts
	// towards its engine before the cache alone is believed.
	slotAssignedFor = time.Minute
	// slotWaiterFor is how long a waiter stays in the book without its
	// owner's pass naming it again: every waiter is passed at least every
	// lifecycle.SlotRecheckTail.
	slotWaiterFor = 3 * lifecycle.SlotRecheckTail
	// slotWake is how many waiters a freed slot wakes, and slotWakeAfter
	// how long after: the pass that freed it writes its status first.
	slotWake      = 4
	slotWakeAfter = 2 * time.Second
)

// slotWaiter is one grab waiting for a transfer slot.
type slotWaiter struct {
	uid       types.UID
	owner     remediation.Key
	namespace string
	client    string
	rank      int
	since     time.Time
	seen      time.Time
}

// slotBook is the stage's leader-local book of slot waiters and of its own
// recent assignments. Lost with the leader: each owner's first pass files
// its waiters again, in the order their grabs were made.
type slotBook struct {
	mu       sync.Mutex
	waiters  map[types.UID]slotWaiter
	assigned map[string]map[types.UID]time.Time // engine → entry → when
}

func newSlotBook() *slotBook {
	return &slotBook{waiters: map[types.UID]slotWaiter{}, assigned: map[string]map[types.UID]time.Time{}}
}

// before orders two waiters: the higher class, then the longer wait, then
// the uid.
func (a slotWaiter) before(b slotWaiter) bool {
	if a.rank != b.rank {
		return a.rank > b.rank
	}
	if !a.since.Equal(b.since) {
		return a.since.Before(b.since)
	}
	return a.uid < b.uid
}

// sync files owner's waiters as they are now and drops the ones it no
// longer has, and any waiter not seen for slotWaiterFor.
func (b *slotBook) sync(owner remediation.Key, waiting []slotWaiter, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := map[types.UID]bool{}
	for _, w := range waiting {
		keep[w.uid] = true
		if cur, ok := b.waiters[w.uid]; ok {
			// The first wait stands: a pass does not move a grab back.
			w.since = cur.since
		}
		w.seen = now
		b.waiters[w.uid] = w
	}
	for uid, w := range b.waiters {
		if (w.owner == owner && !keep[uid]) || now.Sub(w.seen) > slotWaiterFor {
			delete(b.waiters, uid)
		}
	}
}

// ahead counts the waiters of w's client in front of it.
func (b *slotBook) ahead(w slotWaiter) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.waiters[w.uid]; ok {
		w.since = cur.since
	}
	n := 0
	for uid, o := range b.waiters {
		if uid != w.uid && o.namespace == w.namespace && o.client == w.client && o.before(w) {
			n++
		}
	}
	return n
}

// head lists the owners of the first n waiters of client.
func (b *slotBook) head(namespace, client string, n int) []remediation.Key {
	b.mu.Lock()
	ws := make([]slotWaiter, 0, len(b.waiters))
	for _, w := range b.waiters {
		if w.namespace == namespace && w.client == client {
			ws = append(ws, w)
		}
	}
	b.mu.Unlock()
	slices.SortFunc(ws, func(a, c slotWaiter) int {
		switch {
		case a.before(c):
			return -1
		case c.before(a):
			return 1
		}
		return 0
	})
	var out []remediation.Key
	for _, w := range ws {
		if len(out) == n {
			break
		}
		if !slices.Contains(out, w.owner) {
			out = append(out, w.owner)
		}
	}
	return out
}

// assign records that this leader pinned uid to engine at now.
func (b *slotBook) assign(engine string, uid types.UID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.assigned[engine] == nil {
		b.assigned[engine] = map[types.UID]time.Time{}
	}
	b.assigned[engine][uid] = now
	delete(b.waiters, uid)
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

// waiterOf is e as a slot waiter of client.
func waiterOf(ow owner, v *lifecycle.View, e *catalogv1alpha1.DownloadEntry, client string) slotWaiter {
	since := e.GrabbedAt.Time
	if since.IsZero() {
		since = v.Now
	}
	k, _ := ownerKeyOf(ow.ref)
	return slotWaiter{
		uid: types.UID(e.UID), owner: k, namespace: ow.ref.Namespace, client: client,
		rank: priorityRank(v.Intents.Priority[e.ID]), since: since,
	}
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
		v.Ahead[types.UID(e.UID)] = s.slotBook.ahead(waiterOf(ow, v, e, c.Name))
	}
	return nil
}

// slotsAfter files what one owner's plan did to the slots: its waiters as
// they now stand, the assignments just made, and, when one of its grabs
// stopped holding a slot, a wake for the head of that client's queue.
func (s *Stage) slotsAfter(ow owner, v *lifecycle.View, plan lifecycle.Plan) {
	late := lateClients(v)
	if len(late) == 0 {
		return
	}
	before := map[string]*catalogv1alpha1.DownloadEntry{}
	for i := range v.Stored {
		before[v.Stored[i].UID] = &v.Stored[i]
	}
	after := map[string]*catalogv1alpha1.DownloadEntry{}
	var waiting []slotWaiter
	for i := range plan.Entries {
		e := &plan.Entries[i]
		after[e.UID] = e
		if _, ok := late[e.Client]; !ok {
			continue
		}
		switch {
		case dlindex.WaitsForSlot(e):
			waiting = append(waiting, waiterOf(ow, v, e, e.Client))
		case e.Engine != "":
			if was, ok := before[e.UID]; !ok || was.Engine == "" {
				s.slotBook.assign(e.Engine, types.UID(e.UID), v.Now)
			}
		}
	}
	k, _ := ownerKeyOf(ow.ref)
	s.slotBook.sync(k, waiting, v.Now)

	freed := map[string]bool{}
	for uid, was := range before {
		if _, ok := late[was.Client]; !ok || !dlindex.HoldsSlot(was) {
			continue
		}
		if now, ok := after[uid]; !ok || !dlindex.HoldsSlot(now) {
			freed[was.Client] = true
		}
	}
	for c := range freed {
		s.slotWake.add(s.slotBook.head(ow.ref.Namespace, c, slotWake), slotWakeAfter)
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
