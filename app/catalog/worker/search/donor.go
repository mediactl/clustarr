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

package search

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=audiografts,verbs=get;list;watch

// donorWant is what an audio donor for ref must carry (anime dual-audio
// spec §6.1): the languages the item's status.audio says its file lacks,
// the original as the anchor, and the donors a graft of it already failed
// with; and the item's donors in flight. ok is false when nothing is
// missing or the original is unknown, so no donor could be aligned.
func (w *Worker) donorWant(ctx context.Context, ns string, ref commonv1.MediaRef, t decision.Target) (*decision.Donor, []decision.Queued, bool, error) {
	var audio *catalogv1alpha1.AudioState
	key := client.ObjectKey{Namespace: ns, Name: ref.Name}
	switch ref.Kind {
	case commonv1.MediaKindEpisode:
		var e catalogv1alpha1.Episode
		if err := w.Client.Get(ctx, key, &e); err != nil {
			return nil, nil, false, client.IgnoreNotFound(err)
		}
		audio = e.Status.Audio
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := w.Client.Get(ctx, key, &m); err != nil {
			return nil, nil, false, client.IgnoreNotFound(err)
		}
		audio = m.Status.Audio
	default:
		return nil, nil, false, nil
	}
	if audio == nil || len(audio.Missing) == 0 || t.OriginalLanguageTag == "" {
		return nil, nil, false, nil
	}
	d := &decision.Donor{Languages: audio.Missing, Anchor: t.OriginalLanguageTag}
	if t.Current != nil {
		d.Source = t.Current.Quality.Source
	}
	var g transcodev1alpha1.AudioGraft
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: k8s.AudioGraftName(ref.Name)}, &g); err == nil {
		d.Rejected = g.Status.RejectedReleases
	} else if client.IgnoreNotFound(err) != nil {
		return nil, nil, false, fmt.Errorf("get the AudioGraft of %s: %w", ref.Name, err)
	}

	entries, err := OwnerEntries(ctx, w.Client, ns, ref)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read the donor grabs of %s: %w", ref.Name, err)
	}
	var queue []decision.Queued
	for i := range entries {
		if rollup.IsDonor(&entries[i]) {
			queue = append(queue, decision.Queued{Quality: entries[i].Release.Quality})
		}
	}
	return d, queue, true, nil
}
