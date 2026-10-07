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
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// AudioGraftMediaFileRefIndex indexes AudioGrafts by status.mediaFileRef,
// the file transcode judged (and grafted) them against.
const AudioGraftMediaFileRefIndex = "status.mediaFileRef"

func indexAudioGraftByMediaFileRef(o client.Object) []string {
	g, ok := o.(*transcodev1alpha1.AudioGraft)
	if !ok || g.Status.MediaFileRef == "" {
		return nil
	}
	return []string{g.Status.MediaFileRef}
}

// FileOfAudioGraft is the file a graft names (status.mediaFileRef).
func FileOfAudioGraft(_ context.Context, o client.Object) []types.NamespacedName {
	g, ok := o.(*transcodev1alpha1.AudioGraft)
	if !ok || g.Status.MediaFileRef == "" {
		return nil
	}
	return []types.NamespacedName{{Namespace: g.Namespace, Name: g.Status.MediaFileRef}}
}

// AudioGraftDoneChanged is the predicate a graft's file is woken through:
// its phase or completion time moved.
func AudioGraftDoneChanged() predicate.Predicate {
	return k8s.StatusFieldChanged(extractAudioGraftDone)
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
