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

package fileimport

import (
	"fmt"
	"slices"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// What this file knows about episode files, shared with
// app/import/worker/rescan so the importer and the scanner attribute an
// episode file the same way.

// EpisodeCandidate is one existing Episode of a series, reduced to what
// attributing a file to it needs.
type EpisodeCandidate struct {
	// Name is the Episode object's name.
	Name string

	// Season and Number are spec.seasonNumber and spec.episodeNumber: the
	// series' official numbering.
	Season, Number int

	// Absolute is status.absoluteNumber; 0 when the episode has none.
	Absolute int

	// AirDate is status.airDate as a UTC calendar date ("2006-01-02"), or
	// "" when unknown.
	AirDate string

	// Title is status.title, for the rendered file name.
	Title string

	// SceneSeason, SceneNumber and SceneAbsolute are
	// status.sceneNumbering, the numbering scene releases use where it
	// differs from the official one; nil when unknown.
	SceneSeason, SceneNumber, SceneAbsolute *int32
}

// EpisodeCandidateFor projects an Episode onto an EpisodeCandidate. The air
// date is read in UTC (see ReleaseYear for why metav1.Time's local zone
// would give the wrong day west of UTC).
func EpisodeCandidateFor(ep *catalogv1alpha1.Episode) EpisodeCandidate {
	c := EpisodeCandidate{
		Name:   ep.Name,
		Season: int(ep.Spec.SeasonNumber),
		Number: int(ep.Spec.EpisodeNumber),
		Title:  ep.Status.Title,
	}
	if ep.Status.AbsoluteNumber != nil {
		c.Absolute = int(*ep.Status.AbsoluteNumber)
	}
	if ep.Status.AirDate != nil && !ep.Status.AirDate.IsZero() {
		c.AirDate = ep.Status.AirDate.UTC().Format("2006-01-02")
	}
	if s := ep.Status.SceneNumbering; s != nil {
		c.SceneSeason, c.SceneNumber, c.SceneAbsolute = s.Season, s.Episode, s.Absolute
	}
	return c
}

// EpisodeFileRef is the MediaRef a MediaFile holding eps (sorted, at least
// one) carries: the first episode, and -- for a multi-episode file, one file
// backing several episodes ("S01E01E02") -- every episode it covers in
// Keys, the field MediaRef documents for "the Episode ... names covered by a
// pack release". One MediaFile per file, not one per episode: two
// MediaFiles for one path would transcode and subtitle the file twice.
func EpisodeFileRef(eps []EpisodeCandidate) commonv1.MediaRef {
	ref := commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: eps[0].Name}
	if len(eps) > 1 {
		for _, e := range eps {
			ref.Keys = append(ref.Keys, e.Name)
		}
	}
	return ref
}

// MatchEpisodes attributes a parsed episode file to the episodes of one
// series it holds, by the numbering its name carries -- the way Sonarr's
// parser-driven import maps a file onto a series' episodes -- and never by
// anything looser. reason is non-empty, and eps nil, when the name does not
// settle it: a file that names no episode, names episodes the series does
// not have, or spans seasons.
//
//   - An absolute number (anime) is matched against status.absoluteNumber,
//     and preferred over a season/episode pair when the series is an anime
//     one or the name carries no pair.
//   - An air date (a daily show) is matched against status.airDate.
//   - A season/episode pair is matched against the official numbering and,
//     when that misses, against the scene numbering as a whole: the file
//     is read in one numbering or the other, never a mix.
//
// Every number must match exactly one episode; a number two episodes share
// is ambiguous, and reported rather than resolved.
func MatchEpisodes(p *release.ParsedRelease, seriesType catalogv1alpha1.SeriesType, cands []EpisodeCandidate) (eps []EpisodeCandidate, reason string) {
	switch {
	case len(p.Absolute) > 0 && (seriesType == catalogv1alpha1.SeriesTypeAnime || len(p.Episodes) == 0):
		official := func(c EpisodeCandidate, n int) bool { return c.Absolute == n }
		scene := func(c EpisodeCandidate, n int) bool { return c.SceneAbsolute != nil && int(*c.SceneAbsolute) == n }
		eps, reason = matchNumbers(p.Absolute, cands, official, scene, "absolute episode %d")
	case len(p.Episodes) > 0:
		if len(p.Seasons) != 1 {
			return nil, fmt.Sprintf("the name carries episode numbers across %d seasons, not one", len(p.Seasons))
		}
		season := p.Seasons[0]
		official := func(c EpisodeCandidate, n int) bool { return c.Season == season && c.Number == n }
		scene := func(c EpisodeCandidate, n int) bool {
			return c.SceneSeason != nil && c.SceneNumber != nil && int(*c.SceneSeason) == season && int(*c.SceneNumber) == n
		}
		eps, reason = matchNumbers(p.Episodes, cands, official, scene, fmt.Sprintf("S%02dE%%02d", season))
	case p.AirDate != nil:
		date := p.AirDate.UTC().Format("2006-01-02")
		var hits []EpisodeCandidate
		for _, c := range cands {
			if c.AirDate == date {
				hits = append(hits, c)
			}
		}
		switch len(hits) {
		case 0:
			return nil, fmt.Sprintf("no episode of the series aired on %s", date)
		case 1:
			eps = hits
		default:
			return nil, fmt.Sprintf("%d episodes of the series aired on %s (%s); the name does not say which",
				len(hits), date, strings.Join(names(hits), ", "))
		}
	case p.FullSeason:
		return nil, "the name is a whole season's, not an episode's"
	default:
		return nil, "the name carries no season and episode, absolute number or air date"
	}
	if reason != "" {
		return nil, reason
	}
	slices.SortFunc(eps, func(a, b EpisodeCandidate) int {
		if a.Season != b.Season {
			return a.Season - b.Season
		}
		return a.Number - b.Number
	})
	return slices.CompactFunc(eps, func(a, b EpisodeCandidate) bool { return a.Name == b.Name }), ""
}

// matchNumbers maps every one of numbers onto exactly one candidate, first
// through official and, if that misses any, through scene. label formats one
// number for a reason.
func matchNumbers(
	numbers []int, cands []EpisodeCandidate, official, scene func(EpisodeCandidate, int) bool, label string,
) ([]EpisodeCandidate, string) {
	eps, reason := matchEach(numbers, cands, official, label)
	if reason == "" {
		return eps, ""
	}
	if sceneEps, sceneReason := matchEach(numbers, cands, scene, label); sceneReason == "" {
		return sceneEps, ""
	}
	return nil, reason
}

func matchEach(numbers []int, cands []EpisodeCandidate, match func(EpisodeCandidate, int) bool, label string) ([]EpisodeCandidate, string) {
	var eps []EpisodeCandidate
	for _, n := range numbers {
		var hits []EpisodeCandidate
		for _, c := range cands {
			if match(c, n) {
				hits = append(hits, c)
			}
		}
		switch len(hits) {
		case 0:
			return nil, fmt.Sprintf("the series has no "+label, n)
		case 1:
			eps = append(eps, hits[0])
		default:
			return nil, fmt.Sprintf("%d episodes of the series are "+label+" (%s); the name does not say which",
				len(hits), n, strings.Join(names(hits), ", "))
		}
	}
	return eps, ""
}

func names(eps []EpisodeCandidate) []string {
	out := make([]string, 0, len(eps))
	for _, e := range eps {
		out = append(out, e.Name)
	}
	return out
}
