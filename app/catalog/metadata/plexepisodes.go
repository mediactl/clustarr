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
	"time"

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// plexLookupBudget bounds the Plex lookup inside an episode RPC: a cold
// show costs one match, one seasons page and an episode page per 100
// episodes, all behind the provider's limiter.
const plexLookupBudget = 10 * time.Second

// withPlexIDs sets each episode's PlexID from the first PlexProvider that
// answers for the series ids name, and marks every episode PlexConsulted,
// so the Series reconciler releases an id Plex no longer gives. Any
// failure leaves the episodes as they are: the reconciler keeps the ids
// it stored before.
func withPlexIDs(ctx context.Context, reg *pkgmetadata.Registry, ids pkgmetadata.ExternalIDs, episodes []pkgmetadata.Episode) {
	if len(reg.Plex) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, plexBudget(ctx))
	defer cancel()
	for _, p := range reg.Plex {
		pCtx, span := tracing.Start(ctx, "metadata.PlexProvider.ShowChildren")
		ch, err := p.ShowChildren(pCtx, ids)
		if err != nil {
			tracing.RecordError(span, err)
			span.End()
			continue
		}
		span.End()
		joinPlexEpisodes(ch.Episodes, episodes)
		for i := range episodes {
			episodes[i].PlexConsulted = true
		}
		return
	}
}

// plexBudget is how long the Plex lookup may take: half the caller's
// remaining time, capped at plexLookupBudget, so the episode list it only
// enriches is answered inside the caller's deadline whatever Plex does.
func plexBudget(ctx context.Context) time.Duration {
	budget := plexLookupBudget
	if dl, ok := ctx.Deadline(); ok {
		if half := time.Until(dl) / 2; half < budget {
			budget = half
		}
	}
	return budget
}

// joinPlexEpisodes gives each episode Plex's id for it: by TVDB episode id,
// else by (season, episode) when exactly one of Plex's episodes carries
// that pair, no other episode claimed it by TVDB id, and it does not name
// a different TVDB episode. Plex numbers specials its own way, so the pair
// is only a fallback, and anything ambiguous gets no id (spec §4).
func joinPlexEpisodes(plex []pkgmetadata.PlexEpisode, episodes []pkgmetadata.Episode) {
	type pair struct{ season, episode int32 }
	byTVDB := make(map[string]string, len(plex))
	byPair := make(map[pair]pkgmetadata.PlexEpisode, len(plex))
	count := make(map[pair]int, len(plex))
	for _, p := range plex {
		if p.TVDB != "" {
			byTVDB[p.TVDB] = p.ID
		}
		k := pair{p.Season, p.Episode}
		count[k]++
		byPair[k] = p
	}
	claimed := make(map[string]bool, len(episodes))
	for i := range episodes {
		tvdb := episodes[i].IDs[pkgmetadata.KeyTVDB]
		if id := byTVDB[tvdb]; tvdb != "" && id != "" {
			episodes[i].PlexID = id
			claimed[id] = true
		}
	}
	for i := range episodes {
		e := &episodes[i]
		if e.PlexID != "" {
			continue
		}
		k := pair{e.SeasonNumber, e.EpisodeNumber}
		p, ok := byPair[k]
		if !ok || count[k] != 1 || claimed[p.ID] {
			continue
		}
		if tvdb := e.IDs[pkgmetadata.KeyTVDB]; p.TVDB != "" && tvdb != p.TVDB {
			continue
		}
		e.PlexID = p.ID
		claimed[p.ID] = true
	}
}
