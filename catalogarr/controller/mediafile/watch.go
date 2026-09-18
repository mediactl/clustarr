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

package mediafile

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// Field index keys. Registered against TranscodeJob and SubtitleRequest so
// Reconcile can List "every {TranscodeJob,SubtitleRequest} for this
// MediaFile" -- the direction the mapping functions below don't need,
// because both spec types name their MediaFile directly.
const (
	transcodeJobMediaFileRefIndex    = ".spec.mediaFileRef"
	subtitleRequestMediaFileRefIndex = ".spec.mediaFileRef"
)

func indexTranscodeJobByMediaFileRef(o client.Object) []string {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok || tj.Spec.MediaFileRef == "" {
		return nil
	}
	return []string{tj.Spec.MediaFileRef}
}

func indexSubtitleRequestByMediaFileRef(o client.Object) []string {
	sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
	if !ok || sr.Spec.MediaFileRef == "" {
		return nil
	}
	return []string{sr.Spec.MediaFileRef}
}

// extractTranscodeJobPhase is §10's predicate: catalogarr's mediafile
// controller only reacts to a TranscodeJob reaching Succeeded, via
// k8s.StatusFieldIn. A wrong-typed object (never happens in practice --
// Watches only calls this for TranscodeJobs -- but StatusFieldChanged's own
// doc comment models exactly this defensive shape) returns "".
func extractTranscodeJobPhase(o client.Object) string {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok {
		return ""
	}
	return string(tj.Status.Phase)
}

// extractSubtitleItemsSignature is §10's other predicate:
// "status.items changed", via k8s.StatusFieldChanged. Items is a slice, not
// comparable, so this renders a comparable projection of the fields the
// sidecar feedback path cares about (langKey/state/path), not the whole
// struct -- a score or attempts-count change alone should not re-trigger
// this controller.
func extractSubtitleItemsSignature(o client.Object) string {
	sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
	if !ok {
		return ""
	}
	sig := ""
	for _, it := range sr.Status.Items {
		sig += string(it.LangKey) + "=" + string(it.State) + ":" + it.Path + ";"
	}
	return sig
}

// mediaFileForTranscodeJob and mediaFileForSubtitleRequest map the watched
// object straight to its named MediaFile -- both spec types carry the ref
// directly, so no List/field-index round trip is needed here (the index
// above is for the reverse direction, used inside Reconcile).
func (r *Reconciler) mediaFileForTranscodeJob(_ context.Context, o client.Object) []reconcile.Request {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok || tj.Spec.MediaFileRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}}}
}

func (r *Reconciler) mediaFileForSubtitleRequest(_ context.Context, o client.Object) []reconcile.Request {
	sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
	if !ok || sr.Spec.MediaFileRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: sr.Namespace, Name: sr.Spec.MediaFileRef}}}
}
