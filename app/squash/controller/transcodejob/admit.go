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

package transcodejob

import (
	"sort"
	"time"
)

// Slot is one transcode as the admission function sees it: either a Job
// already holding a slot (running) or a suspended one waiting for one
// (queued). It carries nothing Kubernetes-shaped, so [Admit] can be
// table-tested without a cluster.
type Slot struct {
	// Key identifies the TranscodeJob, "<namespace>/<name>". It is the
	// tie-breaker of last resort, which is what makes [Admit] deterministic
	// when priority and creation time are equal.
	Key string

	// Hardware is the slot class the transcode needs: squasharr.HardwareCPU,
	// HardwareNVIDIA or HardwareIntel. A class the budget does not name has
	// a budget of zero.
	Hardware string

	// Profile is the TranscodeProfile name, for the per-profile limit.
	Profile string

	// Priority orders queued transcodes; higher is admitted first. Ignored
	// for running ones.
	Priority int32

	// Created orders queued transcodes of equal priority, oldest first.
	// Ignored for running ones.
	Created time.Time
}

// Budget is what [Admit] admits against.
type Budget struct {
	// Slots is the per-hardware concurrency budget, the parsed --slots flag
	// (squasharr.ParseSlots). A missing or zero entry means "never admit
	// this class", which is how a cluster without that GPU is configured.
	Slots map[string]int32

	// ProfileLimits caps how many transcodes of one profile may run at once,
	// across every hardware class -- §6.4's "per-profile counts". A missing
	// or non-positive entry means no per-profile cap: only the hardware
	// budget applies.
	//
	// The reconciler fills it from each TranscodeProfile's
	// spec.maxConcurrent ([profileLimits]), whose zero means the same "no
	// cap" as a missing entry here.
	ProfileLimits map[string]int32
}

// Admit returns the queued transcodes to unsuspend now, in the order they
// were admitted.
//
// It is pure: the same inputs always give the same answer, whatever order
// queued and running arrive in. Queued transcodes are considered by priority
// (higher first), then creation time (older first), then Key; each is
// admitted when a slot of its hardware class is free AND its profile is
// under its limit, counting both what is already running and what this call
// has admitted so far.
//
// A blocked transcode does not block the ones behind it: a high-priority
// NVIDIA encode waiting for the one GPU slot does not hold back a
// lower-priority CPU encode, because they compete for different slots. That
// is deliberate -- strict head-of-line blocking would leave CPU slots idle
// for as long as any GPU job waits.
//
// Admit never admits more than the budget, even when running already
// exceeds it (a --slots reduction on restart): an over-budget class simply
// admits nothing until enough running transcodes finish.
func Admit(queued, running []Slot, budget Budget) []Slot {
	used := map[string]int32{}
	perProfile := map[string]int32{}
	for _, r := range running {
		used[r.Hardware]++
		perProfile[r.Profile]++
	}

	order := make([]Slot, len(queued))
	copy(order, queued)
	sort.SliceStable(order, func(i, j int) bool { return admitsBefore(order[i], order[j]) })

	var admitted []Slot
	seen := map[string]bool{}
	for _, q := range order {
		if seen[q.Key] {
			continue // a duplicate key is one transcode, not two
		}
		seen[q.Key] = true
		if used[q.Hardware] >= budget.Slots[q.Hardware] {
			continue
		}
		if limit := budget.ProfileLimits[q.Profile]; limit > 0 && perProfile[q.Profile] >= limit {
			continue
		}
		used[q.Hardware]++
		perProfile[q.Profile]++
		admitted = append(admitted, q)
	}
	return admitted
}

// admitsBefore is [Admit]'s order over queued transcodes: priority
// (higher first), then creation time (older first), then Key. The class
// assignment that runs before Admit (assignClasses) takes candidates in the
// same order, through this one function, so the two cannot disagree.
func admitsBefore(a, b Slot) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if !a.Created.Equal(b.Created) {
		return a.Created.Before(b.Created)
	}
	return a.Key < b.Key
}
