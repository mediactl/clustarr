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
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/release"
)

// MatchDownloaded attributes a downloaded file's (or its release's)
// numbering to the series' episodes as the grab read it: through the
// series' numbering (decision.CoverageFor over id -- its scene table, its
// scene season names, the numbers a broadcast's release lists, an
// episode's own title) when the name carries one season's episode numbers
// and id or the name has anything to read them through, or an anime's
// number under a title that names its season ("Show S2 - 01"), and as
// MatchEpisodes does otherwise -- an anime's absolute number, an air date,
// and any name whose covered episodes the series does not all have.
func MatchDownloaded(
	p *release.ParsedRelease, seriesType catalogv1alpha1.SeriesType, cands []EpisodeCandidate, id decision.Identity,
) (eps []EpisodeCandidate, reason string) {
	absolute := len(p.Absolute) > 0 && seriesType == catalogv1alpha1.SeriesTypeAnime
	read := len(id.SceneMappings) > 0 || len(id.SceneSeasonNames) > 0 || len(p.ListedEpisodes) > 0
	if len(p.Episodes) > 0 && len(p.Seasons) == 1 && !p.FullSeason && !absolute && read {
		if eps, ok := candidatesAt(decision.CoverageFor(p, id).Episodes, cands); ok {
			return eps, ""
		}
	}
	if absolute && (p.TitleSeason > 1 || len(id.SceneSeasonNames) > 0) {
		// "Show S2 - 01": the number is the episode of the season the
		// title names, as the grab read it (decision's addTitleSeason);
		// and on a series whose scene seasons have names of their own
		// ("Shingeki no Bahamut - Virgin Soul - 01"), the season its title
		// is the name of (sceneIndex.absolute). Read by TVDB's absolute
		// number, that file was season 1's first episode, held against the
		// file there, while its release had been grabbed for season 2's
		// (talos, 2026-11-27). A name whose episode the series lacks, or
		// that the scene table does not place, is read as MatchEpisodes
		// reads it, by the absolute number.
		if eps, ok := candidatesAt(decision.CoverageFor(p, id).Episodes, cands); ok {
			return eps, ""
		}
	}
	return MatchEpisodes(p, seriesType, cands)
}

// candidatesAt are the episodes at the given places, in order; ok is false
// when there are none or the series lacks one of them.
func candidatesAt(places []decision.SeasonEpisode, cands []EpisodeCandidate) ([]EpisodeCandidate, bool) {
	if len(places) == 0 {
		return nil, false
	}
	out := make([]EpisodeCandidate, 0, len(places))
	for _, pl := range places {
		i := slices.IndexFunc(cands, func(c EpisodeCandidate) bool { return c.Season == pl.Season && c.Number == pl.Episode })
		if i < 0 {
			return nil, false
		}
		out = append(out, cands[i])
	}
	return out, true
}

// narrowToGranted keeps, of the episodes a download's one file was
// attributed to, those the download was grabbed for: a broadcast's file
// holds every episode of the broadcast, and the grab took it for the ones
// still wanted, so the others keep the files they have. A file attributed
// to none of the granted episodes is left as its name reads -- the name,
// not the grab, says what a file is -- and so is one with nothing granted.
func narrowToGranted(eps, granted []EpisodeCandidate) []EpisodeCandidate {
	if len(granted) == 0 || len(eps) < 2 {
		return eps
	}
	var kept []EpisodeCandidate
	for _, e := range eps {
		if slices.ContainsFunc(granted, func(g EpisodeCandidate) bool { return g.Name == e.Name }) {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		return eps
	}
	return kept
}

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

// bareSeasonEpisodeRegex and bareEpisodeRegex read a file named for its
// episode alone, after any bracketed group: "S02EP01", "S02 E01", and
// "Episode 01 - The Wind Blows", "Ep 01", "E01".
var (
	bareSeasonEpisodeRegex = regexp.MustCompile(`(?i)^(?:\[[^\]]*\][\s._-]*)*S(\d{1,2})[\s._-]*E(?:P|pisode)?[\s._-]*(\d{1,3})(?:v\d+)?(?:$|[\s._\-\[\(])`)
	bareEpisodeRegex       = regexp.MustCompile(`(?i)^(?:\[[^\]]*\][\s._-]*)*E(?:P|pisode)?[\s._-]*(\d{1,3})(?:v\d+)?(?:$|[\s._\-\[\(])`)
)

// bareEpisode reads a downloaded file named for its episode alone, which no
// release pattern does: a release name opens with its title, and a pack's
// files often carry none. The season is the one the name writes ("S02EP01")
// or, for a name with the episode only ("Episode 01 - The Wind Blows"),
// packSeason: the one season its download's release title is a pack of, 0
// when it is not such a pack. Nil when the name is not of that shape or
// names no season either way. The numbering is the release's, so it is
// read through the series' numbering as any other (MatchDownloaded); the
// quality, group and languages are the release's (fillFromRelease).
//
// On talos two packs downloaded whole were held as unparseable and removed
// (Fog Hill of Five Elements' season 2, Ushio and Tora's, 2026-11-24).
func bareEpisode(path string, packSeason int) *release.ParsedRelease {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	season, number := 0, ""
	if m := bareSeasonEpisodeRegex.FindStringSubmatch(base); m != nil {
		season, _ = strconv.Atoi(m[1])
		number = m[2]
	} else if m := bareEpisodeRegex.FindStringSubmatch(base); m != nil {
		season, number = packSeason, m[1]
	}
	n, err := strconv.Atoi(number)
	if err != nil || season < 1 || n < 1 {
		return nil
	}
	return &release.ParsedRelease{
		Seasons: []int{season}, Episodes: []int{n},
		LanguageUnknown: true,
		ReleaseType:     commonv1.ReleaseTypeSingle,
	}
}
