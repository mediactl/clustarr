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

package admission

import (
	"sync"
	"time"

	"github.com/jonboulle/clockwork"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// The ledger's timings (loop spec §5.3).
const (
	// GrantPriority is the queue priority a grant, a directive or a changed
	// wait reason wakes its file at: above the initial list's LowPriority,
	// so a grant jumps the startup flood rather than expiring behind it.
	GrantPriority = 100
	// grantTTL is how long a grant nobody took lives.
	grantTTL = 2 * time.Minute
	// grantBackoffMax caps the backoff an expired grant puts its file on.
	grantBackoffMax = 30 * time.Minute
	// grantBackoffMin is the first backoff.
	grantBackoffMin = time.Minute
	// admissionBackstop is the longest an admission pass waits for the next.
	admissionBackstop = time.Minute
	// ledgerResync is how often the quorum read and the task sweep repeat.
	ledgerResync = 5 * time.Minute
)

// Options configures a Ledger.
type Options struct {
	Slots  map[string]int32 // --slots
	Window int              // --job-window; 0 is unlimited (§5.11)
	// GraftConcurrency is --graft-concurrency: standalone graft slots per
	// namespace (§5.13); 0 is graftplan.DefaultConcurrency.
	GraftConcurrency int
	Clock            clockwork.Clock // nil: the real clock
}

// Snapshot is one file's admission state as the transcode planner reads it.
type Snapshot = transcodeplan.Ledger

type entry struct {
	view       FileView
	observedAt time.Time
	// key is view.Key.String(), built once: the backlog sorts by it.
	key string
}

type grant struct {
	kind    transcodeplan.GrantKind
	class   transcodev1alpha1.Hardware
	profile string
	expires time.Time
	inUse   bool
}

type backoff struct {
	until time.Time
	delay time.Duration
}

type wait struct{ reason, cause string }

// Ledger is admission's memory (loop spec §5.5): every file it has seen,
// the grants and directives it issued, and each pool's state. It writes
// nothing: the loop's transcode planner acts on what it grants, and the slim
// TranscodeProfile reconciler sizes the pools from what it counts. One
// mutex guards everything; every method is O(1) but Pass and the rebuild.
type Ledger struct {
	mu                sync.Mutex
	o                 Options
	clock             clockwork.Clock
	files             map[types.UID]*entry
	byKey             map[types.NamespacedName]types.UID
	grants            map[types.UID]*grant
	backoffs          map[types.UID]backoff
	directives        map[types.UID]transcodeplan.Directive
	pools             map[pool.Key]*PoolState
	devices           map[transcodev1alpha1.Hardware]transcodeplan.Device
	waits             map[types.UID]wait
	leaseMissingSince map[types.UID]time.Time
	publishFailed     map[types.UID]string
	withdrawFailed    map[types.UID]string
	profileUIDs       map[string]types.UID
	dispatched        map[pool.Key]int32
	rebuilt           bool
	waker             func(types.NamespacedName, int) // LoopSource's; nil until the loop starts
	wake              chan event.GenericEvent         // the admission pass, buffer 1
	profileWake       chan event.GenericEvent         // the slim TranscodeProfile reconciler, buffer 64
}

// NewLedger is an empty ledger: it grants nothing until Rebuild has run.
func NewLedger(o Options) *Ledger {
	clk := o.Clock
	if clk == nil {
		clk = clockwork.NewRealClock()
	}
	return &Ledger{
		o: o, clock: clk, files: map[types.UID]*entry{}, byKey: map[types.NamespacedName]types.UID{},
		grants: map[types.UID]*grant{}, backoffs: map[types.UID]backoff{}, directives: map[types.UID]transcodeplan.Directive{},
		pools: map[pool.Key]*PoolState{}, devices: map[transcodev1alpha1.Hardware]transcodeplan.Device{},
		waits: map[types.UID]wait{}, leaseMissingSince: map[types.UID]time.Time{}, publishFailed: map[types.UID]string{},
		withdrawFailed: map[types.UID]string{},
		profileUIDs:    map[string]types.UID{}, dispatched: map[pool.Key]int32{},
		wake: make(chan event.GenericEvent, 1), profileWake: make(chan event.GenericEvent, 64),
	}
}

// Observe merges one view: it replaces the entry only when (Seq, rank) is
// lexicographically >= the entry's (§5.5), so a stale view never frees a
// slot. A view whose UID differs from the one recorded under its key
// retires the old UID's entry (a file deleted and recreated under its name;
// the task sweep withdraws whatever the old UID left).
func (l *Ledger) Observe(v FileView) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observeLocked(v, l.clock.Now())
	l.signalLocked()
}

func (l *Ledger) observeLocked(v FileView, now time.Time) bool {
	if old, ok := l.byKey[v.Key]; ok && old != v.UID {
		l.retireLocked(old)
	}
	cur, ok := l.files[v.UID]
	if ok && (v.Seq < cur.view.Seq || (v.Seq == cur.view.Seq && rank(v.Phase) < rank(cur.view.Phase))) {
		return false
	}
	l.storeLocked(v, cur, now)
	return true
}

// storeLocked records v over cur (which may be nil).
func (l *Ledger) storeLocked(v FileView, cur *entry, now time.Time) {
	if cur != nil && !v.EligibleKnown {
		v.Eligible, v.EligibleKnown = cur.view.Eligible, cur.view.EligibleKnown
	}
	if v.ProfileUID == "" {
		v.ProfileUID = l.profileUIDs[v.Profile]
	}
	l.files[v.UID] = &entry{view: v, observedAt: now, key: v.Key.String()}
	l.byKey[v.Key] = v.UID
	if g := l.grants[v.UID]; g != nil && !g.inUse {
		switch {
		case g.kind == transcodeplan.GrantGraftSlot:
			if graftHoldsSlot(v) {
				delete(l.grants, v.UID) // the graft slot is held by the block now
			}
		case windowPhase(v.Phase):
			delete(l.grants, v.UID) // the place is held by the block now
		case g.kind == transcodeplan.GrantDispatch:
			// A dispatch grant is a Planned file's: one observed with no
			// block (a clear) or a verdict holds no slot (§5.5).
			delete(l.grants, v.UID)
		}
	}
	if d, ok := l.directives[v.UID]; ok && (!slotPhase(v.Phase) || v.DispatchSeq != d.Seq) {
		delete(l.directives, v.UID) // acted on
	}
	if v.Phase != catalogv1alpha1.TranscodePhasePlanned {
		delete(l.waits, v.UID)
	}
	if !slotPhase(v.Phase) {
		delete(l.leaseMissingSince, v.UID)
		delete(l.publishFailed, v.UID)
		delete(l.withdrawFailed, v.UID)
	}
}

// graftHoldsSlot reports a view whose graft block holds a graft slot.
func graftHoldsSlot(v FileView) bool {
	return holdsGraftSlot(GraftFile{Phase: v.GraftPhase, Reason: v.GraftReason})
}

// Forget handles a NotFound pass: it drops the key's entry and its grant,
// and reports the UID it held and whether that file had a dispatch in
// flight or a grant, so the loop withdraws its task.
func (l *Ledger) Forget(key types.NamespacedName) (types.UID, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	uid, ok := l.byKey[key]
	if !ok {
		return "", false
	}
	e := l.files[uid]
	open := (e != nil && (slotPhase(e.view.Phase) || e.view.Phase == catalogv1alpha1.TranscodePhaseSwapping)) || l.grants[uid] != nil
	l.retireLocked(uid)
	l.signalLocked()
	return uid, open
}

// retireLocked drops every trace of uid.
func (l *Ledger) retireLocked(uid types.UID) {
	if e := l.files[uid]; e != nil {
		delete(l.byKey, e.view.Key)
	}
	delete(l.files, uid)
	delete(l.grants, uid)
	delete(l.backoffs, uid)
	delete(l.directives, uid)
	delete(l.waits, uid)
	delete(l.leaseMissingSince, uid)
	delete(l.publishFailed, uid)
	delete(l.withdrawFailed, uid)
}

// Snapshot is the file's admission state for the planner: its grant (kind
// and class), its directive, the wait reason admission wants written, and
// the device of the granted class.
func (l *Ledger) Snapshot(uid types.UID) Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked(uid)
}

func (l *Ledger) snapshotLocked(uid types.UID) Snapshot {
	var s Snapshot
	if g := l.grants[uid]; g != nil && (g.inUse || l.clock.Now().Before(g.expires)) {
		s.Grant, s.Class, s.Device = g.kind, g.class, l.devices[g.class]
	}
	if d, ok := l.directives[uid]; ok {
		s.Directive = &d
	}
	w := l.waits[uid]
	s.WaitReason, s.WaitCause = w.reason, w.cause
	s.PublishFailed, s.WithdrawFailed = l.publishFailed[uid], l.withdrawFailed[uid]
	return s
}

// Device is the device class's pods last published, as the pass read it.
func (l *Ledger) Device(class transcodev1alpha1.Hardware) transcodeplan.Device {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.devices[class]
}

// Rebuilt reports whether the first quorum read has landed: before it the
// ledger grants nothing and the pool reconciler suspends nothing.
func (l *Ledger) Rebuilt() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rebuilt
}

func (l *Ledger) markRebuilt() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rebuilt = true
	l.signalLocked()
}

// setWaker installs the loop queue's add function (LoopSource).
func (l *Ledger) setWaker(f func(types.NamespacedName, int)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.waker = f
}

// enqueueLocked wakes the file key at prio, when the loop has started.
func (l *Ledger) enqueueLocked(key types.NamespacedName, prio int) {
	if l.waker != nil {
		l.waker(key, prio)
	}
}

// signalLocked wakes the admission pass (non-blocking, buffer 1).
func (l *Ledger) signalLocked() {
	select {
	case l.wake <- event.GenericEvent{}:
	default:
	}
}

// Wake asks for one admission pass.
func (l *Ledger) Wake() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.signalLocked()
}

// ProfileWake is the slim TranscodeProfile reconciler's wake: a profile's
// per-pool dispatched count changed (§5.7 step 10).
func (l *Ledger) ProfileWake() <-chan event.GenericEvent { return l.profileWake }

// SetSlots replaces the per-class budget (a flag reload, and tests).
func (l *Ledger) SetSlots(slots map[string]int32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.o.Slots = slots
	l.signalLocked()
}

// SetWindow replaces the per-profile window (a flag reload, and tests).
func (l *Ledger) SetWindow(window int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.o.Window = window
	l.signalLocked()
}
