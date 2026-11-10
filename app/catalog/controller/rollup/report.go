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

package rollup

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EventAction is the action every item reconcile's Kubernetes Event
// carries: the reconcile observed the edge.
const EventAction = "Reconcile"

// Reporter records an item reconcile's Kubernetes Events on any kind
// (2026-11-10 design D14): Movie's report.go made kind-neutral, so the
// Album, Book, Audiobook and Issue reconcilers emit the same phase and
// condition edges Movie has since Phase C. Every call site emits on an
// edge, never on a steady state (Transitioned, PhaseEdge): a RootFolder
// still missing or a profile still unresolved persists on every reconcile,
// and an Event per reconcile would bury the one that said when it started.
// A nil Recorder records nothing, so a test that wires none loses only the
// Events.
type Reporter struct {
	// Recorder is the k8s.io/client-go/tools/events.EventRecorder the
	// manager hands each reconciler (mgr.GetEventRecorder(name)); it writes
	// events.k8s.io/v1, which is the group each reconciler's RBAC grants.
	Recorder k8sevents.EventRecorder
}

// Normal records a Normal Event on o.
func (r Reporter) Normal(o client.Object, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(o, nil, corev1.EventTypeNormal, reason, EventAction, format, args...)
	}
}

// Warn records a Warning Event on o.
func (r Reporter) Warn(o client.Object, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(o, nil, corev1.EventTypeWarning, reason, EventAction, format, args...)
	}
}

// PhaseEdge records the Normal Event "phase <prev> -> <next>" under reason
// next when the item's phase moved. It is called after the apply, so a
// phase that never landed is never announced, and the first phase an item
// ever gets is no edge: the ItemEvent that announced the item already said
// it was added (Movie's rule). It reports whether an Event was recorded.
func (r Reporter) PhaseEdge(o client.Object, prev, next string) bool {
	if prev == "" || prev == next {
		return false
	}
	r.Normal(o, next, "phase %s -> %s", prev, next)
	return true
}

// WarnEdge records a Warning Event under reason when setting condType to
// (status, reason) is a change from prev, the conditions the item carried
// before this reconcile (Transitioned); it reports whether one was recorded.
// The call sits beside the Mark* that sets the condition, so the Event says
// when the state started and the condition says that it persists.
func (r Reporter) WarnEdge(o client.Object, prev []metav1.Condition, condType string, status metav1.ConditionStatus, reason, format string, args ...any) bool {
	if !Transitioned(prev, condType, status, reason) {
		return false
	}
	r.Warn(o, reason, format, args...)
	return true
}
