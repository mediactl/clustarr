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

// Package lookuphold holds an item's next metadata lookup back after one
// that failed, for as long as the lookup has been failing: the track
// listing every Album's pass asks the gateway for, and the album listing
// every Artist's. Every pass of such an item asked again 30 s after any
// failure until 2026-11-15, and on talos 1,082 Albums asked the gateway for
// about 70 MusicBrainz requests a second against the one it may make, so
// almost none were answered.
package lookuphold

import (
	"math/rand/v2"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Min and Max bound the wait a failed lookup holds an item to.
const (
	Min = 30 * time.Second
	Max = time.Hour
)

// size bounds how many items' holds are kept, least recently used out first.
const size = 4096

// Hold is a failed lookup an item waits out before it asks again.
type Hold struct {
	// Until is when the next lookup may run.
	Until time.Time
	// Err is the failure, reported again while the item waits, so its
	// condition holds still.
	Err error
	// since is when the lookup started failing.
	since time.Time
}

// Holds keeps each item's hold under a key its caller chooses: a key that
// moves with the item's metadata (a refresh) is not held by the old one's
// failure. The zero value is ready to use.
type Holds[K comparable] struct {
	once sync.Once
	lru  *lru.Cache[K, Hold]
}

func (h *Holds[K]) cache() *lru.Cache[K, Hold] {
	h.once.Do(func() {
		c, err := lru.New[K, Hold](size)
		if err != nil { // lru.New fails only for a size below 1
			panic(err)
		}
		h.lru = c
	})
	return h.lru
}

// Held is k's hold while it lasts at now: no lookup may run before Until.
func (h *Holds[K]) Held(k K, now time.Time) (Hold, bool) {
	hold, ok := h.cache().Get(k)
	if !ok || !now.Before(hold.Until) {
		return Hold{}, false
	}
	return hold, true
}

// Failed holds k's next lookup back after one that failed at now with err,
// and returns the hold. The wait is how long the lookup has been failing,
// between Min and Max, so each wait about doubles the last, jittered to
// between half and all of it, so items that failed together do not ask
// again together. The lookup has been failing since its first failure this
// process saw under k, or since, whichever is earlier: the caller passes
// its condition's (Since), so a restarted manager asks once for an item
// failing for days and then waits the longest, rather than every 30 s
// again.
func (h *Holds[K]) Failed(k K, now, since time.Time, err error) Hold {
	if prev, ok := h.cache().Peek(k); ok && prev.since.Before(since) {
		since = prev.since
	}
	if since.After(now) {
		since = now
	}
	d := min(max(now.Sub(since), Min), Max)
	hold := Hold{Until: now.Add(d/2 + rand.N(d/2+1)), Err: err, since: since}
	h.cache().Add(k, hold)
	return hold
}

// Clear ends k's hold: its lookup succeeded.
func (h *Holds[K]) Clear(k K) { h.cache().Remove(k) }

// Since is when conds' condition of type condType went false over a failed
// lookup, or now: a false condition whose reason is one of notFailures (a
// verdict, not a failed lookup) is none.
func Since(conds []metav1.Condition, condType string, now time.Time, notFailures ...string) time.Time {
	cond := meta.FindStatusCondition(conds, condType)
	if cond == nil || cond.Status != metav1.ConditionFalse || !cond.LastTransitionTime.Time.Before(now) {
		return now
	}
	for _, r := range notFailures {
		if cond.Reason == r {
			return now
		}
	}
	return cond.LastTransitionTime.Time
}
