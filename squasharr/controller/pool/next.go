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

package pool

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// Desired is one admission pass's answer for a pool: the parallelism (and
// gang minCount) to send, and whether the Job should be suspended.
type Desired struct {
	Parallelism int32
	Suspend     bool
}

// Action is what the reconciler should do with a Desired: nothing, apply it,
// or delete the stored Job (a drained pool that must be recreated).
type Action int

const (
	ActionNone Action = iota
	ActionApply
	ActionDelete
)

// Next decides one admission pass for one pool. dispatched counts the pool's
// Queued and Running TranscodeJobs; admission never dispatches past the
// class's slots or the profile's maxConcurrent, so it is already the size.
func Next(stored *batchv1.Job, dispatched int32, drift Drift) (Desired, Action) {
	if stored == nil {
		if dispatched == 0 {
			return Desired{}, ActionNone
		}
		return Desired{Parallelism: dispatched}, ActionApply
	}
	if failed(stored) {
		return Desired{}, ActionDelete
	}
	par := ptr.Deref(stored.Spec.Parallelism, 1)
	suspended := ptr.Deref(stored.Spec.Suspend, false)
	if drift != DriftNone {
		switch {
		case !suspended && dispatched > 0:
			return Desired{Parallelism: par}, ActionNone // draining: admission holds new work
		case !suspended:
			return Desired{Parallelism: par, Suspend: true}, ActionApply
		case !Mutable(stored):
			return Desired{Parallelism: par, Suspend: true}, ActionNone
		case drift == DriftRecreate:
			return Desired{}, ActionDelete
		default:
			return Desired{Parallelism: max(dispatched, 1), Suspend: dispatched == 0}, ActionApply
		}
	}
	switch {
	case dispatched == 0 && suspended:
		return Desired{Parallelism: par, Suspend: true}, ActionNone
	case dispatched == 0:
		return Desired{Parallelism: par, Suspend: true}, ActionApply
	case suspended:
		return Desired{Parallelism: dispatched}, ActionApply
	case dispatched > par:
		return Desired{Parallelism: dispatched}, ActionApply
	default:
		return Desired{Parallelism: par}, ActionNone
	}
}

func failed(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
