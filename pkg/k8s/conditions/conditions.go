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

// Package conditions reads metav1.Conditions. It is the read half of pkg/k8s's
// condition helpers, split out so that a reader which never writes status
// (pkg/pipeline, and through it cmd/ui) links neither pkg/k8s nor
// controller-runtime (spec §4.3 step 1.2). pkg/k8s keeps a one-line wrapper
// for each name, so its callers are unchanged.
package conditions

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConditionReady is the one condition type every Clustarr kind reports. Each
// api group also declares kind-specific types next to its status struct
// (MovieConditionAvailable, IndexerConditionHealthy, ...); Ready is the
// roll-up, and it is what the "Ready" print column of almost every CRD reads.
const ConditionReady = "Ready"

// FindCondition returns the condition of type condType, or nil.
func FindCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	return meta.FindStatusCondition(conds, condType)
}

// IsConditionTrue reports whether condType is present and True.
func IsConditionTrue(conds []metav1.Condition, condType string) bool {
	return meta.IsStatusConditionTrue(conds, condType)
}

// IsConditionFalse reports whether condType is present and False.
func IsConditionFalse(conds []metav1.Condition, condType string) bool {
	return meta.IsStatusConditionFalse(conds, condType)
}

// IsReady reports whether Ready is present and True.
func IsReady(conds []metav1.Condition) bool { return IsConditionTrue(conds, ConditionReady) }

// ObservedGeneration returns the ObservedGeneration of condType, or 0.
func ObservedGeneration(conds []metav1.Condition, condType string) int64 {
	if c := FindCondition(conds, condType); c != nil {
		return c.ObservedGeneration
	}
	return 0
}

// StatusUpToDate reports whether condType was last set for obj's current
// generation. A controller that returns early on an unchanged spec, and a test
// that waits for a reconcile to land, both ask exactly this question; a stale
// Ready=True is the classic way to miss a regression.
func StatusUpToDate(obj metav1.Object, conds []metav1.Condition, condType string) bool {
	if obj == nil {
		return false
	}
	c := FindCondition(conds, condType)
	return c != nil && c.ObservedGeneration == obj.GetGeneration()
}
