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
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// open is P's files in a window phase plus its outstanding window grants
// (§5.11).
func (l *Ledger) open(profile string) int {
	n := 0
	for uid, e := range l.files {
		if e.view.Profile == profile && windowPhase(e.view.Phase) {
			n++
			continue
		}
		if g := l.grants[uid]; g != nil && g.kind == transcodeplan.GrantWindow && g.profile == profile {
			n++
		}
	}
	return n
}

// Open is open under the lock: P's window count.
func (l *Ledger) Open(profile string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open(profile)
}

// backlogLocked is P's backlog: eligible files with no window phase, no
// current verdict, no grant and no backoff, best first -- effective
// priority descending, then namespace/name (§5.11 "Filling it").
func (l *Ledger) backlogLocked(profile string, now time.Time) []*entry {
	var out []*entry
	for uid, e := range l.files {
		if l.inBacklogLocked(uid, e, profile, now) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.view.Priority != b.view.Priority {
			return a.view.Priority > b.view.Priority
		}
		return a.key < b.key
	})
	return out
}

// backlogLenLocked is len(backlogLocked(profile, now)), unsorted.
func (l *Ledger) backlogLenLocked(profile string, now time.Time) int {
	n := 0
	for uid, e := range l.files {
		if l.inBacklogLocked(uid, e, profile, now) {
			n++
		}
	}
	return n
}

// inBacklogLocked reports whether file uid is in profile's backlog.
func (l *Ledger) inBacklogLocked(uid types.UID, e *entry, profile string, now time.Time) bool {
	v := e.view
	return v.Profile == profile && v.Eligible && !windowPhase(v.Phase) && !v.VerdictHolds && l.grants[uid] == nil &&
		!l.onBackoffLocked(uid, now)
}

// Backlog is P's backlog size: the files admission holds in memory for the
// window (TranscodeProfile.status.waitingFiles).
func (l *Ledger) Backlog(profile string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backlogLenLocked(profile, l.clock.Now())
}

// fillWindowLocked grants each profile's best backlog files a window place
// while the profile's window has room (§5.11); a window of 0 is unlimited.
func (l *Ledger) fillWindowLocked(profiles map[string]ProfileView, now time.Time) {
	for name := range profiles {
		open := l.open(name)
		if l.o.Window > 0 && open >= l.o.Window {
			continue // full, the steady state: no need to sort the backlog
		}
		for _, e := range l.backlogLocked(name, now) {
			if l.o.Window > 0 && open >= l.o.Window {
				break
			}
			l.grantLocked(e.view.UID, e.view.Key, transcodeplan.GrantWindow, "", name, now)
			open++
		}
	}
}
