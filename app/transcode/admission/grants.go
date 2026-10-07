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
	"time"

	"k8s.io/apimachinery/pkg/types"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// TakeGrant marks uid's grant in use and returns the file's snapshot: an
// in-use grant never expires, and the planner must Confirm or Release it
// (the loop releases it on any pass that did neither, ReleaseUnconfirmed).
func (l *Ledger) TakeGrant(uid types.UID) (Snapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	g := l.grants[uid]
	if g == nil || (!g.inUse && !l.clock.Now().Before(g.expires)) {
		return l.snapshotLocked(uid), false
	}
	g.inUse = true
	return l.snapshotLocked(uid), true
}

// Confirm drops uid's grant once the apply that used it landed: a dispatch
// grant's slot is held from then on by the Queued block the loop observes,
// a window grant's place by the Planned or Pending one. seq is the dispatch
// the grant became (0 for a window grant).
func (l *Ledger) Confirm(uid types.UID, _ int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.grants, uid)
	delete(l.backoffs, uid)
	l.signalLocked()
}

// Release gives uid's grant back unused and wakes admission.
func (l *Ledger) Release(uid types.UID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.grants, uid)
	l.signalLocked()
}

// ReleaseUnconfirmed is the loop's deferred release: an in-use grant that
// Confirm did not reach this pass (a conflict, an error, a panic) goes back.
func (l *Ledger) ReleaseUnconfirmed(uid types.UID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if g := l.grants[uid]; g != nil && g.inUse {
		delete(l.grants, uid)
		l.signalLocked()
	}
}

// expireLocked drops every unconsumed grant past its expiry and puts its
// file on a backoff of grantBackoffMin, doubling to grantBackoffMax.
func (l *Ledger) expireLocked(now time.Time) {
	for uid, g := range l.grants {
		if g.inUse || now.Before(g.expires) {
			continue
		}
		delete(l.grants, uid)
		delay := grantBackoffMin
		if prev, ok := l.backoffs[uid]; ok {
			delay = min(prev.delay*2, grantBackoffMax)
		}
		l.backoffs[uid] = backoff{until: now.Add(delay), delay: delay}
	}
}

// onBackoff reports whether uid waits out an expired grant's backoff.
func (l *Ledger) onBackoffLocked(uid types.UID, now time.Time) bool {
	b, ok := l.backoffs[uid]
	return ok && now.Before(b.until)
}

// grantLocked issues a grant of kind to uid and wakes its file.
func (l *Ledger) grantLocked(uid types.UID, key types.NamespacedName, kind transcodeplan.GrantKind,
	class transcodev1alpha1.Hardware, profile string, now time.Time,
) {
	l.grants[uid] = &grant{kind: kind, class: class, profile: profile, expires: now.Add(grantTTL)}
	l.enqueueLocked(key, GrantPriority)
}

// NoteLeaseMissing records whether uid's lease was found missing at now,
// and returns since when it has been (zero when it is not): the lost-worker
// grace counts from the first pass that missed it (§5.5).
func (l *Ledger) NoteLeaseMissing(uid types.UID, missing bool, now time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !missing {
		delete(l.leaseMissingSince, uid)
		return time.Time{}
	}
	since, ok := l.leaseMissingSince[uid]
	if !ok {
		since = now
		l.leaseMissingSince[uid] = since
	}
	return since
}

// PublishFailed holds a task publish failure of uid's dispatch seq for the
// next pass's message, or clears it (err nil).
func (l *Ledger) PublishFailed(uid types.UID, _ int64, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		delete(l.publishFailed, uid)
		return
	}
	l.publishFailed[uid] = err.Error()
}
