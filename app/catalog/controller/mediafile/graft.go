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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=audiografts,verbs=get;list;watch

// audioGraftMediaFileRefIndex indexes AudioGrafts by status.mediaFileRef,
// the file squasharr judged (and grafted) them against.
const audioGraftMediaFileRefIndex = "status.mediaFileRef"

func indexAudioGraftByMediaFileRef(o client.Object) []string {
	g, ok := o.(*transcodev1alpha1.AudioGraft)
	if !ok || g.Status.MediaFileRef == "" {
		return nil
	}
	return []string{g.Status.MediaFileRef}
}

// mediaFileForAudioGraft wakes the file a graft names.
func mediaFileForAudioGraft(_ context.Context, o client.Object) []reconcile.Request {
	g, ok := o.(*transcodev1alpha1.AudioGraft)
	if !ok || g.Status.MediaFileRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: g.Namespace, Name: g.Status.MediaFileRef}}}
}

// extractAudioGraftDone is what about a graft wakes its file: its phase and
// when it finished.
func extractAudioGraftDone(o client.Object) string {
	g, ok := o.(*transcodev1alpha1.AudioGraft)
	if !ok {
		return ""
	}
	at := ""
	if g.Status.CompletedAt != nil {
		at = g.Status.CompletedAt.UTC().String()
	}
	return string(g.Status.Phase) + "|" + at
}

// unincorporatedGraft is a graft that swapped mf's file after its last
// probe: Succeeded with a graft tag, finished after probedAt. nil when none.
func (r *Reconciler) unincorporatedGraft(ctx context.Context, mf *catalogv1alpha1.MediaFile) (*transcodev1alpha1.AudioGraft, error) {
	var l transcodev1alpha1.AudioGraftList
	if err := r.List(ctx, &l, client.InNamespace(mf.Namespace), client.MatchingFields{audioGraftMediaFileRefIndex: mf.Name}); err != nil {
		return nil, fmt.Errorf("mediafile: list AudioGrafts: %w", err)
	}
	var best *transcodev1alpha1.AudioGraft
	for i := range l.Items {
		g := &l.Items[i]
		if g.Status.Phase != transcodev1alpha1.AudioGraftSucceeded || g.Status.GraftTag == "" || g.Status.CompletedAt == nil {
			continue
		}
		if !after(g.Status.CompletedAt, mf.Status.ProbedAt) || g.Status.GraftTag == mf.Status.GraftTag && !after(g.Status.CompletedAt, mf.Status.GraftedAt) {
			continue
		}
		if best == nil || after(g.Status.CompletedAt, best.Status.CompletedAt) {
			best = g
		}
	}
	return best, nil
}

func after(a, b *metav1.Time) bool { return b == nil || a.After(b.Time) }
