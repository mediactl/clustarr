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
	"strconv"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// MovieIdentity is the decision.Identity of one Movie: every title it is
// known by, its year, and its tmdb and imdb ids.
//
// It is exported, together with EpisodeIdentity, so that
// catalogarr/worker/rssmatcher builds its decision Target's identity from
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

// idQueryIndexers names the indexers whose query in this search was keyed by
// one of the item's ids, from the per-indexer QueryMode indexarr reports.
// Both sides of the join are the Indexer object's name: indexarr stamps it on
// every release as ReleaseInfo.IndexerRef (indexarr/worker/rss.ProjectRelease)
// and on the outcome as IndexerRef.Name (indexarr/search.newOutcome).
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
