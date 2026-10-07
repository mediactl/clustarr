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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
)

// HeldPrefix starts the message of a Planned file admission holds for a
// pool, so a later pass can tell the message is its own.
const HeldPrefix = "waiting for pool "

// PoolState is what the ledger keeps of one pool between passes: the time
// its pods became unschedulable is marked until, the recreation backoff of
// a Failed pool, and the recreate mark of a pool the apiserver will not
// change (the recreate, poolBackoff and unschedulable maps a controller kept
// before ADR-0016). Only admission and the slim TranscodeProfile
// reconciler touch it.
type PoolState struct {
	UnschedulableUntil     time.Time
	FailedAt, BackoffUntil time.Time
	BackoffDelay           time.Duration
	Recreate               bool
}

// DispatchedPerPool is the confirmed dispatches (Queued and Running files)
// of pool k; ok is false until the ledger is rebuilt (§5.12), and then the
// pool reconciler suspends and deletes nothing. A file whose pool is not
// k's name -- its profile was recreated under a new UID -- counts toward no
// pool of the new profile.
func (l *Ledger) DispatchedPerPool(k pool.Key) (int32, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.rebuilt {
		return 0, false
	}
	return l.dispatchedLocked()[k], true
}

// dispatchedLocked counts the Queued and Running files of every pool.
func (l *Ledger) dispatchedLocked() map[pool.Key]int32 {
	out := map[pool.Key]int32{}
	for _, e := range l.files {
		v := e.view
		if !slotPhase(v.Phase) || v.Class == "" {
			continue
		}
		uid, ok := l.profileUIDs[v.Profile]
		if !ok {
			continue
		}
		k := pool.Key{Profile: v.Profile, ProfileUID: uid, Class: v.Class}
		if v.Pool != "" && v.Pool != pool.Name(k) {
			continue // orphaned: dispatched to a pool of a profile UID that is gone
		}
		out[k]++
	}
	return out
}

// SetUnschedulable marks pool k unschedulable until until: no auto or gpu
// file is sent to it, and its Queued ones are rerouted (§5.7 step 2).
func (l *Ledger) SetUnschedulable(k pool.Key, until time.Time) {
	l.UpdatePool(k, func(p *PoolState) { p.UnschedulableUntil = until })
}

// PoolState is a copy of pool k's state.
func (l *Ledger) PoolState(k pool.Key) PoolState {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.pools[k]; p != nil {
		return *p
	}
	return PoolState{}
}

// UpdatePool changes pool k's state under the lock and wakes admission.
func (l *Ledger) UpdatePool(k pool.Key, f func(*PoolState)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.pools[k]
	if p == nil {
		p = &PoolState{}
		l.pools[k] = p
	}
	f(p)
	l.signalLocked()
}

// unschedulableLocked is, per profile, the GPU classes whose pool is marked
// unschedulable now.
func (l *Ledger) unschedulableLocked(now time.Time) func(profile string) map[transcodev1alpha1.Hardware]bool {
	return func(profile string) map[transcodev1alpha1.Hardware]bool {
		var out map[transcodev1alpha1.Hardware]bool
		for _, class := range GPUClasses {
			k := pool.Key{Profile: profile, ProfileUID: l.profileUIDs[profile], Class: class}
			if p := l.pools[k]; p != nil && now.Before(p.UnschedulableUntil) {
				if out == nil {
					out = map[transcodev1alpha1.Hardware]bool{}
				}
				out[class] = true
			}
		}
		return out
	}
}

// Holding is the set of pools admission dispatches nothing new to in this
// pass, each with the message its held files carry (the holding a
// controller kept before ADR-0016, re-typed): stored is every pool Job by name, as the
// cache holds them; cfg the deployment-level half of every pool. A pool is
// held while its template drifted and its pods may still run (it must drain
// first), while it failed or waits out its recreation backoff, and while
// its name is taken by a Job whose controller owner is not the profile.
func (l *Ledger) Holding(stored map[string]*batchv1.Job, profiles []*transcodev1alpha1.TranscodeProfile, cfg pool.Config) map[pool.Key]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	held := map[pool.Key]string{}
	for _, tp := range profiles {
		for _, class := range pool.Classes {
			k := pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: class}
			name := pool.Name(k)
			j, ok := stored[name]
			st := l.pools[k]
			switch {
			case !ok:
				if st != nil && now.Before(st.BackoffUntil) {
					held[k] = fmt.Sprintf("%s%s to recover: it failed at %s and is recreated at %s",
						HeldPrefix, name, st.FailedAt.UTC().Format(time.RFC3339), st.BackoffUntil.UTC().Format(time.RFC3339))
				}
			case pool.OwnerProfileUID(j) != tp.UID:
				held[k] = fmt.Sprintf("%s%s: that Job's controller owner reference is not TranscodeProfile %s (uid %s), "+
					"so transcode will not touch it; delete the Job and the pool is recreated", HeldPrefix, name, tp.Name, tp.UID)
			case pool.Failed(j):
				held[k] = fmt.Sprintf("%s%s to recover: it failed at %s", HeldPrefix, name, FailedAt(j, now).UTC().Format(time.RFC3339))
			case pool.Mutable(j):
			case st != nil && st.Recreate:
				held[k] = HeldPrefix + name + " to drain before it is recreated"
			case pool.Classify(j, pool.Want(tp, class, cfg)) != pool.DriftNone:
				held[k] = HeldPrefix + name + " to drain before its profile change applies"
			}
		}
	}
	return held
}

// FailedAt is when j's Failed condition was set, or now when it says none.
func FailedAt(j *batchv1.Job, now time.Time) time.Time {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time
		}
	}
	return now
}
