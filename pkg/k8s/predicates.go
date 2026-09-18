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

package k8s

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// The predicates in this file implement §10's cross-service contract. The rule
// there is narrow on purpose: a watcher in another service reacts to
// metadata.generation or to one named status field, and to nothing else. A
// metadata refresh that rewrites a Movie's overview must not wake squasharr,
// and §14 has an envtest for exactly that. Anything broader turns five
// independent managers into one distributed hot loop.

// GenerationChanged passes creates, deletes and generic events, and passes an
// update only when metadata.generation moved -- that is, when the spec changed
// rather than the status.
//
// controller-runtime's own GenerationChangedPredicate does the same thing; this
// wrapper exists so the cross-service watches in §10 all name their predicate
// from one place, and so a create is explicitly included (§10's first row reads
// "changed or created").
func GenerationChanged() predicate.Predicate {
	return predicate.GenerationChangedPredicate{}
}

// StatusFieldChanged passes an update only when extract returns a different
// value for the old and the new object. Creates and deletes always pass:
// a watcher has to see a new object once, and has to be able to clean up.
//
// extract is given the object as the informer holds it; it should return a
// comparable projection of the single status field the watch cares about, and
// the zero value for an object of an unexpected type.
//
//	// squasharr's transcodeprofile watch, from §10.
//	k8s.StatusFieldChanged(func(o client.Object) string {
//	    mf, ok := o.(*catalogv1alpha1.MediaFile)
//	    if !ok {
//	        return ""
//	    }
//	    return mf.Status.ProbeHash
//	})
func StatusFieldChanged[T comparable](extract func(client.Object) T) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			return extract(e.ObjectOld) != extract(e.ObjectNew)
		},
	}
}

// StatusFieldIn passes an object whose extracted status field is one of want.
// It is the §10 row for catalogarr's importer, which reacts to a Download in
// phase Completed, Seeding or Failed and ignores every other transition.
//
// Unlike [StatusFieldChanged] this filters creates and deletes too: an object
// that never enters one of the wanted states is not this watcher's business at
// all.
func StatusFieldIn[T comparable](extract func(client.Object) T, want ...T) predicate.Predicate {
	match := func(o client.Object) bool {
		if o == nil {
			return false
		}
		got := extract(o)
		for _, w := range want {
			if got == w {
				return true
			}
		}
		return false
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return match(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return match(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return match(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Fire on entering a wanted state, and on moving between two
			// wanted states, but not on churn while already inside one.
			if !match(e.ObjectNew) {
				return false
			}
			if e.ObjectOld == nil {
				return true
			}
			return extract(e.ObjectOld) != extract(e.ObjectNew)
		},
	}
}

// LabelSelector passes only objects matching sel. §10 gives grabarr's engine
// pods a label-selected informer (`download.clustarr.io/engine`) so a replica
// never sees the Downloads that belong to its siblings.
//
// It returns an error for a malformed selector rather than silently matching
// everything, which is the failure mode that would let one engine attach every
// torrent in the cluster.
func LabelSelector(sel metav1.LabelSelector) (predicate.Predicate, error) {
	p, err := predicate.LabelSelectorPredicate(sel)
	if err != nil {
		return nil, fmt.Errorf("k8s: label selector predicate: %w", err)
	}
	return p, nil
}

// HasLabel passes objects carrying key with value.
func HasLabel(key, value string) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o != nil && o.GetLabels()[key] == value
	})
}

// Deleting passes objects that have a deletion timestamp, for a watch that
// exists only to drive cleanup.
func Deleting() predicate.Predicate {
	return predicate.NewPredicateFuncs(IsDeleting)
}

// And passes when every p passes.
func And(ps ...predicate.Predicate) predicate.Predicate { return predicate.And(ps...) }

// Or passes when any p passes.
func Or(ps ...predicate.Predicate) predicate.Predicate { return predicate.Or(ps...) }

// Not inverts p.
func Not(p predicate.Predicate) predicate.Predicate { return predicate.Not(p) }
