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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConditionReady is the one condition type every Clustarr kind reports. Each
// api group also declares kind-specific types next to its status struct
// (MovieConditionAvailable, IndexerConditionHealthy, ...); Ready is the
// roll-up, and it is what the "Ready" print column of almost every CRD reads.
const ConditionReady = "Ready"

// Condition reasons shared across services. A reason is a CamelCase token, is
// part of the API, and is what an operator greps for; kind-specific reasons
// live next to the controller that sets them, but a controller should reach
// for one of these first so that `kubectl get -o json | jq '..|.reason?'`
// stays readable across services.
const (
	// ReasonReconciled is the happy path: the observed generation has been
	// fully acted on.
	ReasonReconciled = "Reconciled"

	// ReasonReconciling means work for the current generation is in flight.
	ReasonReconciling = "Reconciling"

	// ReasonReconcileError means the last reconcile returned an error and
	// will be retried.
	ReasonReconcileError = "ReconcileError"

	// ReasonInvalidSpec means the spec cannot be acted on at all and
	// retrying will not help until the user edits it.
	ReasonInvalidSpec = "InvalidSpec"

	// ReasonDependencyNotReady means a referenced object (RootFolder,
	// QualityProfile, DownloadClient, Secret, ...) is missing or not ready.
	ReasonDependencyNotReady = "DependencyNotReady"

	// ReasonThrottled means an external rate limit or quota is in force.
	ReasonThrottled = "Throttled"

	// ReasonDisabled means the object is switched off by its own spec.
	ReasonDisabled = "Disabled"

	// ReasonTerminating means the object is being deleted and its finalizer
	// is running.
	ReasonTerminating = "Terminating"

	// ReasonSucceeded is the terminal success reason for one-shot kinds
	// (Search, TranscodeJob, SubtitleRequest).
	ReasonSucceeded = "Succeeded"

	// ReasonFailed is the terminal failure reason for one-shot kinds.
	ReasonFailed = "Failed"

	// ReasonPending means the object is waiting for an external event with no
	// error of its own.
	ReasonPending = "Pending"
)

// NewCondition builds a condition with LastTransitionTime left to
// meta.SetStatusCondition, which only stamps it when the status actually flips.
// message is a format string so callers do not need a fmt.Sprintf at each site.
func NewCondition(condType string, status metav1.ConditionStatus, reason, message string, args ...any) metav1.Condition {
	if len(args) > 0 {
		message = fmt.Sprintf(message, args...)
	}
	return metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

// SetCondition merges c into conditions and reports whether anything changed.
//
// It stamps ObservedGeneration from obj when the caller left it at zero, which
// is what makes the "is this condition about the spec I am looking at?"
// question answerable; passing a nil obj skips that.
func SetCondition(obj client.Object, conditions *[]metav1.Condition, c metav1.Condition) bool {
	if c.ObservedGeneration == 0 && obj != nil {
		c.ObservedGeneration = obj.GetGeneration()
	}
	return meta.SetStatusCondition(conditions, c)
}

// MarkTrue sets condType to True.
func MarkTrue(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool {
	return SetCondition(obj, conditions, NewCondition(condType, metav1.ConditionTrue, reason, message, args...))
}

// MarkFalse sets condType to False.
func MarkFalse(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool {
	return SetCondition(obj, conditions, NewCondition(condType, metav1.ConditionFalse, reason, message, args...))
}

// MarkUnknown sets condType to Unknown, which is the right state while a probe
// or an external lookup has not answered yet.
func MarkUnknown(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool {
	return SetCondition(obj, conditions, NewCondition(condType, metav1.ConditionUnknown, reason, message, args...))
}

// MarkReady is the Ready-condition convention: Ready=True means "this object's
// current generation has been fully reconciled and the thing it describes is
// usable". A controller calls it once, at the end of a reconcile, with the
// outcome it reached.
func MarkReady(obj client.Object, conditions *[]metav1.Condition, ready bool, reason, message string, args ...any) bool {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	return SetCondition(obj, conditions, NewCondition(ConditionReady, status, reason, message, args...))
}

// MarkNotReadyError is the shorthand for the error tail of a reconcile.
func MarkNotReadyError(obj client.Object, conditions *[]metav1.Condition, err error) bool {
	return MarkFalse(obj, conditions, ConditionReady, ReasonReconcileError, "%s", err.Error())
}

// RemoveCondition drops condType and reports whether it was there.
func RemoveCondition(conditions *[]metav1.Condition, condType string) bool {
	return meta.RemoveStatusCondition(conditions, condType)
}

// FindCondition returns the condition of type condType, or nil.
func FindCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	return meta.FindStatusCondition(conditions, condType)
}

// IsConditionTrue reports whether condType is present and True.
func IsConditionTrue(conditions []metav1.Condition, condType string) bool {
	return meta.IsStatusConditionTrue(conditions, condType)
}

// IsConditionFalse reports whether condType is present and False.
func IsConditionFalse(conditions []metav1.Condition, condType string) bool {
	return meta.IsStatusConditionFalse(conditions, condType)
}

// IsReady reports whether Ready is present and True.
func IsReady(conditions []metav1.Condition) bool {
	return IsConditionTrue(conditions, ConditionReady)
}

// ObservedGeneration returns the ObservedGeneration of condType, or 0.
func ObservedGeneration(conditions []metav1.Condition, condType string) int64 {
	if c := FindCondition(conditions, condType); c != nil {
		return c.ObservedGeneration
	}
	return 0
}

// StatusUpToDate reports whether condType was last set for obj's current
// generation. A controller that returns early on an unchanged spec, and a test
// that waits for a reconcile to land, both ask exactly this question; a stale
// Ready=True is the classic way to miss a regression.
func StatusUpToDate(obj client.Object, conditions []metav1.Condition, condType string) bool {
	if obj == nil {
		return false
	}
	c := FindCondition(conditions, condType)
	return c != nil && c.ObservedGeneration == obj.GetGeneration()
}

// ConditionAC converts a condition into the apply configuration [PatchStatus]
// needs. LastTransitionTime is required by the CRD schema, so it is filled with
// now when the caller left it unset -- under server-side apply the field is
// owned by this manager and a zero value would be rejected.
//
// Because conditions are a listType=map keyed on type, applying a subset
// replaces only those entries and leaves conditions owned by other managers
// alone, which is exactly what §5's split between a controller and its worker
// needs.
func ConditionAC(c metav1.Condition) *metav1ac.ConditionApplyConfiguration {
	if c.LastTransitionTime.IsZero() {
		c.LastTransitionTime = metav1.Now()
	}
	return metav1ac.Condition().
		WithType(c.Type).
		WithStatus(c.Status).
		WithReason(c.Reason).
		WithMessage(c.Message).
		WithObservedGeneration(c.ObservedGeneration).
		WithLastTransitionTime(c.LastTransitionTime)
}

// ConditionACs converts a slice of conditions for [PatchStatus].
func ConditionACs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration {
	out := make([]*metav1ac.ConditionApplyConfiguration, 0, len(conditions))
	for _, c := range conditions {
		out = append(out, ConditionAC(c))
	}
	return out
}
