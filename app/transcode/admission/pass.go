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
	"fmt"
	"maps"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// unschedulableAfter is how long a GPU pool's pod has waited unschedulable
// before its pool is marked; the reroute reason names it.
const unschedulableAfter = 10 * time.Minute

// ProfileView is one TranscodeProfile as a pass reads it.
type ProfileView struct {
	Name          string
	UID           types.UID
	Priority      int32
	MaxConcurrent int32
	Hardware      transcodev1alpha1.Hardware
}

// PassInputs is everything an admission pass reads besides the ledger, from
// the cache and clustarr-progress: no apiserver request (§5.7 step 1).
type PassInputs struct {
	Profiles  map[string]ProfileView
	GPUNodes  map[transcodev1alpha1.Hardware]bool
	Devices   map[transcodev1alpha1.Hardware]transcodeplan.Device
	Unhealthy map[transcodev1alpha1.Hardware]string
	Held      map[pool.Key]string // Holding's answer this pass
	// GraftJobs are the unfinished graft Jobs' names by namespace (§5.13):
	// a slot in use while its Job runs, whatever its block says.
	GraftJobs map[string][]string
	// VideoStreams is how many video conversions cluster-plex's GPU pool
	// runs (videolease.go): above zero, no new file is admitted to intel.
	VideoStreams int
}

// PassResult is when the next pass is due.
type PassResult struct{ RequeueAfter time.Duration }

// Pass is one admission pass (§5.7 steps 2-10): reroute directives, then the
// candidates and what runs, their classes, the slot scheduler, dispatch
// grants and wait reasons, the window grants, the graft slots (§5.13), and
// the metrics. It makes no
// apiserver request and writes nothing but memory: every grant and
// directive is enqueued into the loop's queue, whose transcode planner acts
// on it against its own view.
func (l *Ledger) Pass(in PassInputs) PassResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.devices = maps.Clone(in.Devices)
	if l.devices == nil {
		l.devices = map[transcodev1alpha1.Hardware]transcodeplan.Device{}
	}
	l.profileUIDs = uidsOf(in.Profiles)
	l.expireLocked(now)
	if !l.rebuilt {
		return PassResult{RequeueAfter: admissionBackstop} // §5.6 step 4: no grant before the rebuild
	}
	held := maps.Clone(in.Held)
	if held == nil {
		held = map[pool.Key]string{}
	}
	gpu := maps.Clone(in.GPUNodes)
	for class, msg := range in.Unhealthy {
		delete(gpu, class)
		for _, p := range in.Profiles {
			if k := (pool.Key{Profile: p.Name, ProfileUID: p.UID, Class: class}); held[k] == "" {
				held[k] = msg
			}
		}
	}
	l.rerouteLocked(in, now)
	cands, running := l.candidatesLocked(in, now)
	limits := profileLimits(in.Profiles)
	budget, yielding := l.o.Slots, false
	if _, ok := l.o.Slots[string(transcodev1alpha1.HardwareIntel)]; ok && in.VideoStreams > 0 {
		// Plex's streams take the Intel GPU first: no new intel admission.
		// Running and Queued files hold their slots in running, untouched.
		budget, yielding = maps.Clone(l.o.Slots), true
		budget[string(transcodev1alpha1.HardwareIntel)] = 0
	}
	queued, holds := assignClasses(cands, running, gpu, l.unschedulableLocked(now), held, l.profileUIDs, limits, budget)
	slots := make([]Slot, 0, len(queued))
	for _, c := range queued {
		slots = append(slots, c.slot)
	}
	admitted := Admit(slots, running, Budget{Slots: budget, ProfileLimits: limits})
	l.grantDispatchLocked(queued, admitted, now)
	l.waitReasonsLocked(queued, admitted, holds, yielding)
	l.fillWindowLocked(in.Profiles, now)
	l.grantGraftSlotsLocked(in, now)
	l.metricsLocked(in.Profiles, now)
	return PassResult{RequeueAfter: l.nextWakeLocked(now)}
}

// uidsOf is each profile's UID by name.
func uidsOf(profiles map[string]ProfileView) map[string]types.UID {
	out := make(map[string]types.UID, len(profiles))
	for name, p := range profiles {
		out[name] = p.UID
	}
	return out
}

// profileLimits is Budget.ProfileLimits from each profile's
// spec.maxConcurrent; zero is no cap.
func profileLimits(profiles map[string]ProfileView) map[string]int32 {
	out := map[string]int32{}
	for name, p := range profiles {
		if p.MaxConcurrent > 0 {
			out[name] = p.MaxConcurrent
		}
	}
	return out
}

// orphanedLocked reports an in-flight file whose pool no pool of its
// current profile is: the profile is gone, or was recreated under a new UID.
func (l *Ledger) orphanedLocked(v FileView) bool {
	uid, ok := l.profileUIDs[v.Profile]
	if !ok {
		return true
	}
	return v.Pool != "" && v.Pool != pool.Name(pool.Key{Profile: v.Profile, ProfileUID: uid, Class: v.Class})
}

// rerouteLocked is §5.7 step 2: every Queued file that chooses its class,
// is not suspended, and whose pool is marked unschedulable or whose class
// every fresh report says is unusable gets a reroute directive for its
// dispatch, once, and is enqueued; the planner withdraws the dispatch and
// returns the file to Planned. A rerouted file holds its slot until that
// Planned write is observed.
func (l *Ledger) rerouteLocked(in PassInputs, now time.Time) {
	for uid, e := range l.files {
		v := e.view
		if v.Phase != catalogv1alpha1.TranscodePhaseQueued || v.Suspended || !choosesClass(v) ||
			v.Class == transcodev1alpha1.HardwareCPU || v.Class == "" {
			continue
		}
		if d, ok := l.directives[uid]; ok && d.Seq == v.DispatchSeq {
			continue
		}
		reason := ""
		k := pool.Key{Profile: v.Profile, ProfileUID: l.profileUIDs[v.Profile], Class: v.Class}
		if p := l.pools[k]; p != nil && now.Before(p.UnschedulableUntil) {
			reason = fmt.Sprintf("GPU pool %s unschedulable for %dm", pool.Name(k), int(unschedulableAfter.Minutes()))
		} else if msg, ok := in.Unhealthy[v.Class]; ok {
			reason = msg
		}
		if reason == "" {
			continue
		}
		l.directives[uid] = transcodeplan.Directive{Kind: transcodeplan.DirectiveReroute, Seq: v.DispatchSeq, Reason: reason, KeepGPU: gpuOnly(v)}
		l.enqueueLocked(v.Key, GrantPriority)
	}
}

// Reroute issues a reroute directive for uid's dispatch at seq (Pass step
// 2's, exported for a test with no GPU node) and enqueues the file.
func (l *Ledger) Reroute(uid types.UID, seq int64, reason string, keepGPU bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.directives[uid] = transcodeplan.Directive{Kind: transcodeplan.DirectiveReroute, Seq: seq, Reason: reason, KeepGPU: keepGPU}
	if e := l.files[uid]; e != nil {
		l.enqueueLocked(e.view.Key, GrantPriority)
	}
}

// candidatesLocked is §5.7 steps 4-5: the Planned files that compete this
// pass, and the slots already taken -- in-flight files that are not
// orphaned, and unexpired dispatch grants.
func (l *Ledger) candidatesLocked(in PassInputs, now time.Time) ([]candidate, []Slot) {
	var (
		cands   []candidate
		running []Slot
	)
	for uid, e := range l.files {
		v := e.view
		key := v.Key.String()
		switch {
		case slotPhase(v.Phase):
			if l.orphanedLocked(v) {
				continue // its pool is gone, so it holds no worker's slot
			}
			running = append(running, Slot{Key: key, Hardware: string(v.Class), Profile: v.Profile})
		case v.Phase == catalogv1alpha1.TranscodePhasePlanned:
			p, ok := in.Profiles[v.Profile]
			if !ok || v.Suspended || (!v.NextAttemptAt.IsZero() && v.NextAttemptAt.After(now)) || l.grants[uid] != nil ||
				l.onBackoffLocked(uid, now) {
				continue
			}
			if _, ok := l.directives[uid]; ok {
				continue
			}
			prio := v.Priority
			if prio == 0 {
				prio = p.Priority
			}
			cands = append(cands, candidate{uid: uid, v: v, slot: Slot{Key: key, Profile: v.Profile, Priority: prio, Created: v.PlannedAt}})
		}
	}
	for uid, g := range l.grants {
		if g.kind != transcodeplan.GrantDispatch || (!g.inUse && !now.Before(g.expires)) {
			continue
		}
		if e := l.files[uid]; e != nil && slotPhase(e.view.Phase) {
			continue // already counted from its block
		}
		running = append(running, Slot{Key: string(uid), Hardware: string(g.class), Profile: g.profile})
	}
	return cands, running
}

// grantDispatchLocked is §5.7 step 7: each admitted file gets a dispatch
// grant of its class and is enqueued at GrantPriority.
func (l *Ledger) grantDispatchLocked(queued []candidate, admitted []Slot, now time.Time) {
	byKey := make(map[string]candidate, len(queued))
	for _, c := range queued {
		byKey[c.slot.Key] = c
	}
	for _, s := range admitted {
		c, ok := byKey[s.Key]
		if !ok {
			continue
		}
		l.grantLocked(c.uid, c.v.Key, transcodeplan.GrantDispatch, transcodev1alpha1.Hardware(s.Hardware), c.v.Profile, now)
		delete(l.waits, c.uid)
	}
}

// waitReasonsLocked gives each held or unadmitted candidate the Planned
// message admission wants written, and enqueues it only when that changed
// (§5.7 step 7, §5.11 "Bounded writes").
func (l *Ledger) waitReasonsLocked(queued []candidate, admitted []Slot, holds []hold, yielding bool) {
	in := make(map[string]bool, len(admitted))
	for _, s := range admitted {
		in[s.Key] = true
	}
	set := func(uid types.UID, key types.NamespacedName, w wait) {
		if l.waits[uid] == w {
			return
		}
		l.waits[uid] = w
		l.enqueueLocked(key, GrantPriority)
	}
	for _, h := range holds {
		if e := l.files[h.uid]; e != nil {
			msg := h.msg
			if yielding && msg == WaitingForGPU {
				msg = YieldingToPlex
			}
			set(h.uid, e.view.Key, wait{reason: msg, cause: h.cause})
		}
	}
	for _, c := range queued {
		if in[c.slot.Key] {
			continue
		}
		msg := fmt.Sprintf("waiting for a free %s slot", c.slot.Hardware)
		if yielding && c.slot.Hardware == string(transcodev1alpha1.HardwareIntel) {
			msg = YieldingToPlex
		}
		set(c.uid, c.v.Key, wait{reason: msg, cause: transcodeplan.ReasonWaitingForSlot})
	}
}

// metricsLocked is §5.7 step 10: clustarr_transcode_jobs_active per class
// (in-flight files plus dispatch grants), the backlog gauge per profile, and
// a profileWake for each profile whose per-pool dispatched count changed.
func (l *Ledger) metricsLocked(profiles map[string]ProfileView, now time.Time) {
	active := map[string]int{}
	for class := range l.o.Slots {
		active[class] = 0
	}
	for _, e := range l.files {
		if slotPhase(e.view.Phase) && !l.orphanedLocked(e.view) {
			active[string(e.view.Class)]++
		}
	}
	for _, g := range l.grants {
		if g.kind == transcodeplan.GrantDispatch && (g.inUse || now.Before(g.expires)) {
			active[string(g.class)]++
		}
	}
	for class, n := range active {
		if class != "" {
			metrics.TranscodesActive.WithLabelValues(class).Set(float64(n))
		}
	}
	for name := range profiles {
		metrics.TranscodeBacklogFiles.WithLabelValues(name).Set(float64(len(l.backlogLocked(name, now))))
	}
	dispatched := l.dispatchedLocked()
	woken := map[string]bool{}
	for k, n := range dispatched {
		if l.dispatched[k] != n {
			woken[k.Profile] = true
		}
	}
	for k := range l.dispatched {
		if _, ok := dispatched[k]; !ok {
			woken[k.Profile] = true
		}
	}
	l.dispatched = dispatched
	for name := range woken {
		l.wakeProfileLocked(name)
	}
}

// nextWakeLocked is when the next pass is due: the backstop, or sooner for
// a Planned file's nextAttemptAt, a grant's expiry, a backoff's end or a
// pool's unschedulable mark or recreation backoff.
func (l *Ledger) nextWakeLocked(now time.Time) time.Duration {
	next := now.Add(admissionBackstop)
	soon := func(t time.Time) {
		if t.After(now) && t.Before(next) {
			next = t
		}
	}
	for _, e := range l.files {
		if e.view.Phase == catalogv1alpha1.TranscodePhasePlanned {
			soon(e.view.NextAttemptAt)
		}
	}
	for _, g := range l.grants {
		if !g.inUse {
			soon(g.expires)
		}
	}
	for _, b := range l.backoffs {
		soon(b.until)
	}
	for _, p := range l.pools {
		soon(p.UnschedulableUntil)
		soon(p.BackoffUntil)
	}
	return max(next.Sub(now), time.Second)
}

// wakeProfileLocked asks the slim TranscodeProfile reconciler to reconcile
// profile name, without blocking: a full buffer means wakes are pending.
func (l *Ledger) wakeProfileLocked(name string) {
	select {
	case l.profileWake <- event.GenericEvent{Object: &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: name}}}:
	default:
	}
}
