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
	"github.com/mediactl/clustarr/pkg/release"
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

// joinPlexEpisodes gives each episode Plex's id for it, never guessing: an
// id is given only where it names exactly one of Plex's episodes no other
// episode claimed, and a second attribute agrees. In order:
//
//  1. by TVDB episode id;
//  2. by (season, episode), when Plex's episode carries no TVDB id or the
//     same one -- or another one but the same title or air date (TVDB
//     re-issues ids Plex keeps: Family Guy, Very Important People);
//  3. by title, unique among Plex's episodes of the show and among the
//     episodes still unjoined (Plex numbers One Piece's tail lower);
//  4. by air date, unique among Plex's episodes of the same season and the
//     episodes still unjoined there.
//
// Plex numbers specials its own way and merges two-parts TVDB splits, so
// anything left gets no id (spec §4): the provider answers it with
// clustarr's own GUID.
func joinPlexEpisodes(plex []pkgmetadata.PlexEpisode, episodes []pkgmetadata.Episode) {
	type pair struct{ season, episode int32 }
	byTVDB := make(map[string]string, len(plex))
	byPair := make(map[pair]pkgmetadata.PlexEpisode, len(plex))
	count := make(map[pair]int, len(plex))
	byTitle := map[string][]pkgmetadata.PlexEpisode{}
	type seasonDate struct {
		season int32
		date   string
	}
	byDate := map[seasonDate][]pkgmetadata.PlexEpisode{}
	for _, p := range plex {
		if p.TVDB != "" {
			byTVDB[p.TVDB] = p.ID
		}
		k := pair{p.Season, p.Episode}
		count[k]++
		byPair[k] = p
		if t := release.TitleNorm(p.Title); t != "" {
			byTitle[t] = append(byTitle[t], p)
		}
		if p.AirDate != "" {
			byDate[seasonDate{p.Season, p.AirDate}] = append(byDate[seasonDate{p.Season, p.AirDate}], p)
		}
	}
	claimed := make(map[string]bool, len(episodes))
	join := func(e *pkgmetadata.Episode, id string) {
		e.PlexID = id
		claimed[id] = true
	}

	// 1: TVDB id.
	for i := range episodes {
		tvdb := episodes[i].IDs[pkgmetadata.KeyTVDB]
		if id := byTVDB[tvdb]; tvdb != "" && id != "" {
			join(&episodes[i], id)
		}
	}

	// 2: (season, episode).
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
		if tvdb := e.IDs[pkgmetadata.KeyTVDB]; p.TVDB != "" && tvdb != p.TVDB && !sameTitle(p, e) && !sameAirDate(p, e) {
			continue
		}
		join(e, p.ID)
	}

	// 3: title, unique on both sides.
	wantTitle := map[string]int{}
	for i := range episodes {
		if episodes[i].PlexID == "" {
			wantTitle[release.TitleNorm(episodes[i].Title)]++
		}
	}
	for i := range episodes {
		e := &episodes[i]
		t := release.TitleNorm(e.Title)
		if e.PlexID != "" || t == "" || wantTitle[t] != 1 {
			continue
		}
		if ps := byTitle[t]; len(ps) == 1 && !claimed[ps[0].ID] {
			join(e, ps[0].ID)
		}
	}

	// 4: air date within the season, unique on both sides.
	wantDate := map[seasonDate]int{}
	for i := range episodes {
		if d := airDate(&episodes[i]); episodes[i].PlexID == "" && d != "" {
			wantDate[seasonDate{episodes[i].SeasonNumber, d}]++
		}
	}
	for i := range episodes {
		e := &episodes[i]
		k := seasonDate{e.SeasonNumber, airDate(e)}
		if e.PlexID != "" || k.date == "" || wantDate[k] != 1 {
			continue
		}
		if ps := byDate[k]; len(ps) == 1 && !claimed[ps[0].ID] {
			join(e, ps[0].ID)
		}
	}
}

// airDate is the episode's air date as Plex writes one, "" when unknown.
func airDate(e *pkgmetadata.Episode) string {
	if e.AirDate == nil {
		return ""
	}
	return e.AirDate.UTC().Format("2006-01-02")
}

func sameTitle(p pkgmetadata.PlexEpisode, e *pkgmetadata.Episode) bool {
	t := release.TitleNorm(p.Title)
	return t != "" && t == release.TitleNorm(e.Title)
}

func sameAirDate(p pkgmetadata.PlexEpisode, e *pkgmetadata.Episode) bool {
	return p.AirDate != "" && p.AirDate == airDate(e)
}
