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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// AnnotationDeadLettered is the metadata annotation the DLQ projector
// (catalogarr/history.DLQProjector, under ManagerDLQProjector) applies to the
// object a dead-lettered message concerns. Its value is
// "<original-subject>@<RFC3339>". catalogarr/history.AnnotationDeadLettered
// is the same key.
//
// Design spec §5 had the projector set a DeadLettered status condition
// itself; Phase G ruling R1 replaced that with this annotation, so the
// projector never becomes a second status writer on every kind. Each owning
// controller turns the annotation into [ConditionDeadLettered] in its own
// status apply, through [MarkDeadLettered].
//
// # Which kinds can carry it
//
// The projector annotates only an object it can name exactly, from the dead
// letter's payload schema (catalogarr/history/target.go's resolvers). That
// is these sixteen kinds -- the same set its +kubebuilder:rbac patch markers
// grant, and the only kinds whose controllers need to fold the condition:
//
//	catalog.clustarr.io   Movie, Series, Episode, Artist, Album, Author,
//	                      Book, Audiobook, Comic, Issue
//	                        (catalog.ItemEvent, ReleaseEvent, MediaFileEvent,
//	                        SearchTask, GrabTask and MetadataTask, by
//	                        MediaRef.kind)
//	                      ImportList   (catalog.ImportListSynced, importarr.ListTask)
//	                      LibraryScan  (importarr.ScanTask)
//	index.clustarr.io     Indexer      (index.Release, IndexerEvent, RssTask)
//	download.clustarr.io  Download     (catalog.ImportTask, download.DownloadEvent)
//	transcode.clustarr.io TranscodeJob (transcode.JobEvent, transcode.Task)
//	subtitle.clustarr.io  SubtitleRequest (subtitle.SubtitleEvent, FetchTask)
//
// Every other kind never carries it: a dead letter whose schema has no
// resolver, or that names no single object (catalog.WantedScan, a namespace
// sweep), gets a namespace-level Warning Event instead. MediaFile, RootFolder, QualityProfile, DownloadClient,
// TranscodeProfile, SubtitleProfile, SubtitleProvider and the rest are never
// annotated.
//
// # Folding it
//
// Each of the sixteen caps status.conditions at MaxItems=8 and declares at
// most six condition types of its own, so one more always fits. The
// condition is present only while the annotation is: MarkDeadLettered removes
// it when the annotation is gone, so it does not take a slot on every object.
// An operator clears it by deleting the annotation (kubectl annotate <kind>
// <name> clustarr.io/dead-lettered-) once the cause is fixed and the message
// replayed. A controller whose For() predicate filters on generation must OR
// in [DeadLetteredAnnotationChanged], or an annotation-only change never
// reaches its reconcile.
const AnnotationDeadLettered = "clustarr.io/dead-lettered"

// ConditionDeadLettered is True while the object carries
// [AnnotationDeadLettered]: a message about it exhausted its redeliveries and
// sits on CLUSTARR_DLQ. It is absent otherwise.
const ConditionDeadLettered = "DeadLettered"

// ReasonDeadLettered is ConditionDeadLettered's reason.
const ReasonDeadLettered = "DeadLettered"

// DeadLetteredCondition returns the DeadLettered condition obj's
// [AnnotationDeadLettered] calls for, and whether it calls for one at all.
// The message names the original subject and when it was dead-lettered;
// LastTransitionTime is the dead-letter time when the value carries a
// parseable one, so the condition dates from the failure rather than from
// the reconcile that noticed it. A value in any other shape is reported
// verbatim rather than dropped.
func DeadLetteredCondition(obj client.Object) (metav1.Condition, bool) {
	if obj == nil {
		return metav1.Condition{}, false
	}
	value, ok := obj.GetAnnotations()[AnnotationDeadLettered]
	if !ok {
		return metav1.Condition{}, false
	}
	c := NewCondition(ConditionDeadLettered, metav1.ConditionTrue, ReasonDeadLettered,
		"a message about this object was dead-lettered: %s; delete the %s annotation to clear this condition",
		value, AnnotationDeadLettered)
	if i := strings.LastIndex(value, "@"); i >= 0 {
		if at, err := time.Parse(time.RFC3339, value[i+1:]); err == nil {
			c.Message = "a message on " + value[:i] + " about this object was dead-lettered at " +
				at.UTC().Format(time.RFC3339) + "; delete the " + AnnotationDeadLettered +
				" annotation to clear this condition"
			c.LastTransitionTime = metav1.NewTime(at)
		}
	}
	return c, true
}

// MarkDeadLettered folds obj's [AnnotationDeadLettered] into conditions:
// DeadLettered=True while the annotation is present, and no DeadLettered
// condition at all once it is removed. It reports whether conditions
// changed.
//
// Call it on the conditions slice a status apply is about to declare -- the
// same slice MarkTrue/MarkReady write -- on every path that applies status,
// early returns included, so the condition is part of the owning manager's
// complete declaration and is never released by a partial apply. conditions
// is normally seeded from the live status, so the removal half matters: it is
// what clears a DeadLettered the controller itself set earlier.
func MarkDeadLettered(obj client.Object, conditions *[]metav1.Condition) bool {
	c, ok := DeadLetteredCondition(obj)
	if !ok {
		return RemoveCondition(conditions, ConditionDeadLettered)
	}
	return SetCondition(obj, conditions, c)
}

// DeadLetteredAnnotationChanged passes an update only when
// [AnnotationDeadLettered]'s value (or presence) changed; creates, deletes
// and generic events pass. OR it into a controller's generation-filtered
// For() predicate so the DLQ projector's annotation, and an operator removing
// it, reach the reconcile that folds it:
//
//	For(&v1alpha1.Movie{}, builder.WithPredicates(
//	    k8s.Or(k8s.GenerationChanged(), k8s.DeadLetteredAnnotationChanged())))
func DeadLetteredAnnotationChanged() predicate.Predicate {
	value := func(o client.Object) (string, bool) {
		if o == nil {
			return "", false
		}
		v, ok := o.GetAnnotations()[AnnotationDeadLettered]
		return v, ok
	}
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			ov, ook := value(e.ObjectOld)
			nv, nok := value(e.ObjectNew)
			return ov != nv || ook != nok
		},
	}
}
