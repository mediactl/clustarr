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
	"strconv"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// MovieIdentity is the decision.Identity of one Movie: every title it is
// known by, its year, and its tmdb and imdb ids.
//
// It is exported, together with EpisodeIdentity, so that
// app/catalog/worker/rssmatcher builds its decision Target's identity from
// this same code: an RSS decision and a search decision about one item must
// agree on what that item IS, and two builders are how they would come to
// disagree. The tmdb id comes from spec, not metadata, so a movie whose
// metadata has not landed is still identifiable by id.
func MovieIdentity(m *catalogv1alpha1.Movie) decision.Identity {
	id := decision.Identity{IDs: map[string]string{}}
	if m.Spec.TmdbID != 0 {
		id.IDs[commonv1.IDKeyTMDB] = strconv.FormatInt(m.Spec.TmdbID, 10)
	}
	md := m.Status.Metadata
	if md == nil {
		return id
	}
	id.Titles = appendTitles(id.Titles, md.Title, md.OriginalTitle)
	id.Titles = appendTitles(id.Titles, md.AlternateTitles...)
	id.Year = int(md.Year)
	// Radarr's own rule is "Year or SecondaryYear" (ruling R-7): a release
	// named by the festival year of a film dated by its general release is
	// the same film, however far apart the two sit.
	id.SecondaryYear = int(md.SecondaryYear)
	if v := md.ExternalIDs[commonv1.IDKeyIMDB]; v != "" {
		id.IDs[commonv1.IDKeyIMDB] = v
	}
	return id
}

// EpisodeIdentity is the decision.Identity of one or more Episodes of s: the
// SERIES' titles and tvdb id (an Episode carries no title or id a release
// would), plus the episodes' numbering.
//
// One episode is a single-episode search's target. Several are an RSS pack
// target -- the episodes the matcher resolved the release to -- which is
// always a single season (rssmatcher refuses a multi-season pack), so a set
// spanning seasons gets no in-season numbering at all rather than a wrong
// one, and the decision engine then fails it closed. Absolute numbering is
// carried only when every episode has one, because a partial list would
// read as "the target is only these". An air date is carried only for a
// single episode: it is the whole identity of a daily episode, and a pack
// has no one date.
func EpisodeIdentity(s *catalogv1alpha1.Series, eps ...*catalogv1alpha1.Episode) decision.Identity {
	id := decision.Identity{IDs: map[string]string{}}
	if s.Spec.TvdbID != 0 {
		id.IDs[commonv1.IDKeyTVDB] = strconv.FormatInt(s.Spec.TvdbID, 10)
	}
	if md := s.Status.Metadata; md != nil {
		id.Titles = appendTitles(id.Titles, md.Title)
		for _, alt := range md.AlternateTitles {
			id.Titles = appendTitles(id.Titles, alt.Title)
		}
		id.Year = int(md.Year)
	}
	if len(eps) == 0 {
		return id
	}

	season := eps[0].Spec.SeasonNumber
	oneSeason, allAbsolute := true, true
	for _, e := range eps {
		oneSeason = oneSeason && e.Spec.SeasonNumber == season
		allAbsolute = allAbsolute && e.Status.AbsoluteNumber != nil
	}
	if oneSeason {
		id.Season = int(season)
		for _, e := range eps {
			id.Episodes = append(id.Episodes, int(e.Spec.EpisodeNumber))
		}
	}
	if allAbsolute {
		for _, e := range eps {
			id.Absolute = append(id.Absolute, int(*e.Status.AbsoluteNumber))
		}
	}
	if len(eps) == 1 && eps[0].Status.AirDate != nil {
		t := eps[0].Status.AirDate.UTC()
		id.AirDate = &t
	}
	return id
}

// SceneMappings is the decision.Identity.SceneMappings of one TVDB series:
// TheXEM's WHOLE scene-numbering table for it, read from src.
//
// It is exported so app/catalog/worker/rssmatcher reads the same table
// through the same code: a search decision and an RSS decision about one
// episode must read a scene number the same way. It must be the whole table,
// never only the target's rows -- a scene number that maps to a different
// episode is that other episode, and only its row says so (see
// decision.Identity.SceneMappings).
//
// Every series with a tvdb id is asked, not only an anime one: Sonarr applies
// TheXEM to every series TheXEM maps (XemService sets UseSceneNumbering on
// any series in /map/havemap, and ParsingService reads scene numbers for
// any series that uses it), and scenemap.Cached answers an unmapped series
// from its cached havemap without a request of its own.
//
// Nil means read every number literally: no source is wired, the series has
// no tvdb id, TheXEM does not map it, or TheXEM could not be asked. The last
// is logged and not fatal -- scenemap's own contract is "proceed without
// scene numbering rather than treat the series as unmapped", and failing the
// search over it would stop every episode of every series from being
// searched while TheXEM is down.
func SceneMappings(ctx context.Context, src scenemap.Source, tvdbID int64) []decision.SceneMapping {
	if src == nil || tvdbID == 0 {
		return nil
	}
	m, err := src.SceneMap(ctx, tvdbID)
	if err != nil {
		logging.FromContext(ctx).Warn("search: TheXEM scene numbering unavailable; reading release numbers literally",
			"tvdbID", tvdbID, "err", err)
		return nil
	}
	if m == nil || len(m.Mappings) == 0 {
		return nil
	}
	out := make([]decision.SceneMapping, 0, len(m.Mappings))
	for _, row := range m.Mappings {
		// scenemap.Numbering and decision.EpisodeNumbering are the same
		// fields in the same order (scenemap's package doc; its contract
		// test keeps them convertible).
		out = append(out, decision.SceneMapping{
			Scene: decision.EpisodeNumbering(row.Scene),
			TVDB:  decision.EpisodeNumbering(row.TVDB),
		})
	}
	return out
}

// searchNumbering is the season, episode and absolute number a search for e
// asks an indexer for: the scene numbering of e's row in the series' TheXEM
// table (scene), falling back field by field to e's own TVDB numbering.
//
// This is Sonarr's search, not its parser. XemService.PerformUpdate copies
// each XEM row's scene numbers onto the TVDB episode it names
// (Episode.SceneSeasonNumber/SceneEpisodeNumber/SceneAbsoluteEpisodeNumber),
// and ReleaseSearchService searches with them wherever they are set:
// GetSceneEpisodeMappings's "SceneSeasonNumber ?? SeasonNumber" and
// "SceneEpisodeNumber ?? EpisodeNumber" for a standard series, SearchAnime's
// "SceneAbsoluteEpisodeNumber ?? AbsoluteEpisodeNumber" for anime (Sonarr
// src/NzbDrone.Core/DataAugmentation/Xem/XemService.cs and
// src/NzbDrone.Core/IndexerSearch/ReleaseSearchService.cs, develop). An
// indexer keyed by scene numbers -- every anime tracker whose season 2 is
// TVDB's season 1 episodes 14-26 -- answers a TVDB-numbered query with
// nothing, or with the wrong episode. What comes back is still read through
// the same table by the identity check (decision.Identity.SceneMappings), so
// a scene-numbered answer is compared as the TVDB episode it names.
//
// Sonarr applies the rows in table order and a later row for the same TVDB
// episode overwrites an earlier one, so the LAST matching row wins here too.
// A row with no scene episode leaves the in-season numbers alone, and a row
// with no scene absolute leaves the absolute number alone: Sonarr's
// "?? fallback" is per field. Sonarr's ExtrapolateMappings, which invents
// scene numbers for TVDB episodes past the end of a table, is not ported --
// neither is it on the identity side, so an extrapolated query would ask for
// numbers the check then reads literally.
//
// absolute is nil when neither the row nor the episode has one.
func searchNumbering(scene []decision.SceneMapping, e *catalogv1alpha1.Episode) (season, episode int32, absolute *int32) {
	season, episode, absolute = e.Spec.SeasonNumber, e.Spec.EpisodeNumber, e.Status.AbsoluteNumber
	var row *decision.EpisodeNumbering
	for i := range scene {
		if scene[i].TVDB.Season == int(season) && scene[i].TVDB.Episode == int(episode) {
			row = &scene[i].Scene
		}
	}
	if row == nil {
		return season, episode, absolute
	}
	if row.Episode > 0 {
		season, episode = int32(row.Season), int32(row.Episode)
	}
	if row.Absolute > 0 {
		a := int32(row.Absolute)
		absolute = &a
	}
	return season, episode, absolute
}

// idQueryIndexers names the indexers whose query in this search was keyed by
// one of the item's ids, from the per-indexer QueryMode indexarr reports.
// Both sides of the join are the Indexer object's name: indexarr stamps it on
// every release as ReleaseInfo.IndexerRef (app/indexer/worker/rss.ProjectRelease)
// and on the outcome as IndexerRef.Name (app/indexer/search.newOutcome).
//
// One torrent offered by two indexers is collapsed into one release carrying
// ONE IndexerRef. When the survivor is the text-mode indexer's copy, the
// release loses the id-mode indexer's vouching and must identify itself by
// its own ids or title -- the conservative direction, so it is left that way.
func idQueryIndexers(outcomes []schema.SearchOutcome) map[string]bool {
	out := map[string]bool{}
	for _, o := range outcomes {
		if o.QueryMode == schema.SearchQueryModeID && o.IndexerRef.Name != "" {
			out[o.IndexerRef.Name] = true
		}
	}
	return out
}

func appendTitles(dst []string, titles ...string) []string {
	for _, t := range titles {
		if t == "" {
			continue
		}
		dup := false
		for _, have := range dst {
			if have == t {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, t)
		}
	}
	return dst
}
