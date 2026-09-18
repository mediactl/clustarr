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
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

func movie(generation int64) *catalogv1alpha1.Movie {
	return &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "inception",
			Namespace:  "media",
			Generation: generation,
		},
	}
}

func TestSetConditionStampsObservedGeneration(t *testing.T) {
	m := movie(7)
	var conds []metav1.Condition

	if !MarkReady(m, &conds, true, ReasonReconciled, "imported") {
		t.Fatal("first MarkReady reported no change")
	}
	c := FindCondition(conds, ConditionReady)
	if c == nil {
		t.Fatal("Ready condition was not set")
	}
	if c.ObservedGeneration != 7 {
		t.Fatalf("ObservedGeneration = %d, want 7", c.ObservedGeneration)
	}
	if c.Status != metav1.ConditionTrue || c.Reason != ReasonReconciled {
		t.Fatalf("condition = %+v", *c)
	}
}

func TestSetConditionKeepsAnExplicitObservedGeneration(t *testing.T) {
	m := movie(7)
	var conds []metav1.Condition
	SetCondition(m, &conds, metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             ReasonReconciled,
		ObservedGeneration: 3,
	})
	if got := ObservedGeneration(conds, ConditionReady); got != 3 {
		t.Fatalf("ObservedGeneration = %d, want the caller's 3", got)
	}
}

func TestSetConditionIsIdempotent(t *testing.T) {
	m := movie(1)
	var conds []metav1.Condition

	MarkReady(m, &conds, true, ReasonReconciled, "ok")
	before := FindCondition(conds, ConditionReady).LastTransitionTime

	if MarkReady(m, &conds, true, ReasonReconciled, "ok") {
		t.Fatal("re-setting an identical condition reported a change")
	}
	after := FindCondition(conds, ConditionReady).LastTransitionTime
	if !before.Equal(&after) {
		t.Fatalf("LastTransitionTime moved without a status change: %v -> %v", before, after)
	}
}

func TestMarkReadyFlipsStatus(t *testing.T) {
	m := movie(1)
	var conds []metav1.Condition

	MarkReady(m, &conds, true, ReasonReconciled, "ok")
	if !IsReady(conds) {
		t.Fatal("IsReady = false after MarkReady(true)")
	}

	if !MarkReady(m, &conds, false, ReasonDependencyNotReady, "no root folder") {
		t.Fatal("flipping Ready reported no change")
	}
	if IsReady(conds) {
		t.Fatal("IsReady = true after MarkReady(false)")
	}
	if !IsConditionFalse(conds, ConditionReady) {
		t.Fatal("IsConditionFalse = false after MarkReady(false)")
	}
}

func TestMarkFormatsItsMessage(t *testing.T) {
	m := movie(1)
	var conds []metav1.Condition
	MarkFalse(m, &conds, catalogv1alpha1.MovieConditionHasFile, ReasonPending, "waiting for %d of %d files", 1, 3)
	if got := FindCondition(conds, catalogv1alpha1.MovieConditionHasFile).Message; got != "waiting for 1 of 3 files" {
		t.Fatalf("message = %q", got)
	}
}

func TestMarkNotReadyError(t *testing.T) {
	m := movie(1)
	var conds []metav1.Condition
	MarkNotReadyError(m, &conds, errors.New("boom: 50% failed"))
	c := FindCondition(conds, ConditionReady)
	if c.Status != metav1.ConditionFalse || c.Reason != ReasonReconcileError {
		t.Fatalf("condition = %+v", *c)
	}
	// The error text must survive verbatim; passing it as a format string
	// would turn "50%" into a %!f(MISSING).
	if c.Message != "boom: 50% failed" {
		t.Fatalf("message = %q", c.Message)
	}
}

func TestMarkUnknownAndRemove(t *testing.T) {
	m := movie(1)
	var conds []metav1.Condition
	MarkUnknown(m, &conds, catalogv1alpha1.MovieConditionMetadataReady, ReasonPending, "probing")
	if IsConditionTrue(conds, catalogv1alpha1.MovieConditionMetadataReady) {
		t.Fatal("Unknown condition reported as True")
	}
	if !RemoveCondition(&conds, catalogv1alpha1.MovieConditionMetadataReady) {
		t.Fatal("RemoveCondition reported nothing removed")
	}
	if FindCondition(conds, catalogv1alpha1.MovieConditionMetadataReady) != nil {
		t.Fatal("condition survived removal")
	}
}

func TestStatusUpToDate(t *testing.T) {
	m := movie(4)
	var conds []metav1.Condition
	MarkReady(m, &conds, true, ReasonReconciled, "ok")

	if !StatusUpToDate(m, conds, ConditionReady) {
		t.Fatal("StatusUpToDate = false right after setting the condition")
	}

	// A spec edit bumps the generation; the stale Ready=True must not count.
	m.Generation = 5
	if StatusUpToDate(m, conds, ConditionReady) {
		t.Fatal("StatusUpToDate = true for a stale condition")
	}
	if StatusUpToDate(m, conds, "Nonexistent") {
		t.Fatal("StatusUpToDate = true for a missing condition")
	}
	if StatusUpToDate(nil, conds, ConditionReady) {
		t.Fatal("StatusUpToDate = true for a nil object")
	}
}

func TestConditionACFillsLastTransitionTime(t *testing.T) {
	ac := ConditionAC(metav1.Condition{
		Type:   ConditionReady,
		Status: metav1.ConditionTrue,
		Reason: ReasonReconciled,
	})
	if ac.LastTransitionTime == nil || ac.LastTransitionTime.IsZero() {
		t.Fatal("ConditionAC left LastTransitionTime unset; the CRD schema requires it")
	}
	if ac.Type == nil || *ac.Type != ConditionReady {
		t.Fatalf("type = %v", ac.Type)
	}
	if ac.ObservedGeneration == nil {
		t.Fatal("ObservedGeneration was dropped")
	}
}

func TestConditionACKeepsAnExplicitTransitionTime(t *testing.T) {
	when := metav1.NewTime(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	ac := ConditionAC(metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             ReasonReconciled,
		LastTransitionTime: when,
	})
	if !ac.LastTransitionTime.Equal(&when) {
		t.Fatalf("LastTransitionTime = %v, want %v", ac.LastTransitionTime, when)
	}
}

func TestConditionACs(t *testing.T) {
	in := []metav1.Condition{
		{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonReconciled},
		{Type: catalogv1alpha1.MovieConditionHasFile, Status: metav1.ConditionFalse, Reason: ReasonPending},
	}
	out := ConditionACs(in)
	if len(out) != len(in) {
		t.Fatalf("got %d apply configurations, want %d", len(out), len(in))
	}
	for i := range out {
		if out[i].Type == nil || *out[i].Type != in[i].Type {
			t.Fatalf("configuration %d = %v", i, out[i].Type)
		}
	}
}
