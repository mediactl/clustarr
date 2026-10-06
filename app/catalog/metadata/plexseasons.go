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

package metadata

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// maxPlexSeasons is SeriesMetadata.PlexSeasons' MaxItems.
const maxPlexSeasons = 400

// plexSeasons is a Series' status.metadata.plexSeasons: Plex's season ids
// from the first PlexProvider that answers, deduplicated by number (first
// wins) and capped, else prior -- a failed or absent lookup never releases
// ids an earlier refresh found (spec §4).
func plexSeasons(ctx context.Context, reg *pkgmetadata.Registry, ids pkgmetadata.ExternalIDs, prior []catalogv1alpha1.PlexSeasonRef) []catalogv1alpha1.PlexSeasonRef {
	if reg == nil {
		return prior
	}
	for _, p := range reg.Plex {
		pCtx, span := tracing.Start(ctx, "metadata.PlexProvider.ShowChildren")
		ch, err := p.ShowChildren(pCtx, ids)
		if err != nil {
			tracing.RecordError(span, err)
			span.End()
			continue
		}
		span.End()
		seen := make(map[int32]bool, len(ch.Seasons))
		out := make([]catalogv1alpha1.PlexSeasonRef, 0, len(ch.Seasons))
		for _, s := range ch.Seasons {
			if seen[s.Number] || len(out) == maxPlexSeasons {
				continue
			}
			seen[s.Number] = true
			out = append(out, catalogv1alpha1.PlexSeasonRef{Number: s.Number, ID: s.ID})
		}
		return out
	}
	return prior
}

// knownPlexSeasons is obj's current status.metadata.plexSeasons, read
// before this refresh started.
func knownPlexSeasons(obj client.Object) []catalogv1alpha1.PlexSeasonRef {
	if s, ok := obj.(*catalogv1alpha1.Series); ok && s.Status.Metadata != nil {
		return s.Status.Metadata.PlexSeasons
	}
	return nil
}

// withPlexSeasons adds seasons to md's apply configuration.
func withPlexSeasons(md *catalogac.SeriesMetadataApplyConfiguration, seasons []catalogv1alpha1.PlexSeasonRef) {
	for _, s := range seasons {
		md.WithPlexSeasons(catalogac.PlexSeasonRef().WithNumber(s.Number).WithID(s.ID))
	}
}
