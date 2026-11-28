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
// scene season names, an anime's own count through its episodes, the
// numbers a broadcast's release lists, an episode's own title) when the
// name carries one season's episode numbers
// and id or the name has anything to read them through, or an anime's
// number under a title that names its season ("Show S2 - 01"), and as
// MatchEpisodes does otherwise -- an anime's absolute number, an air date,
// and any name whose covered episodes the series does not all have.
func MatchDownloaded(
	p *release.ParsedRelease, seriesType catalogv1alpha1.SeriesType, cands []EpisodeCandidate, id decision.Identity,
) (eps []EpisodeCandidate, reason string) {
	absolute := len(p.Absolute) > 0 && seriesType == catalogv1alpha1.SeriesTypeAnime
	read := len(id.SceneMappings) > 0 || len(id.SceneSeasonNames) > 0 || len(id.Numbering) > 0 || len(p.ListedEpisodes) > 0
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

// downloadSeason is the one season a download is of, 0 when nothing says or
// it is of several: the season its release title names -- a pack's or an
// episode's season, read through the series' numbering as the grab read it
// (decision.CoverageFor), or the season its title is the name of
// (decision.NamedSeason) -- and, under a title that names none ("[Group]
// Show - 07", a batch titled by the series alone), the season of the
// episodes it was grabbed for. The specials are no season to read a number
// in.
//
// It is what a file's name is not asked for: a name that gives a number and
// no season gives the episode, and the download says the season
// (inDownloadSeason, bareEpisode, titledEpisode).
func downloadSeason(titled *release.ParsedRelease, id decision.Identity, grabbed []EpisodeCandidate) int {
	named := map[int]bool{}
	if titled != nil {
		if titled.FullSeason || len(titled.Seasons) > 0 {
			cov := decision.CoverageFor(titled, id)
			for _, s := range cov.FullSeasons {
				named[s] = true
			}
			for _, e := range cov.Episodes {
				named[e.Season] = true
			}
		} else if s := decision.NamedSeason(titled, id); s > 0 {
			named[s] = true
		}
	}
	if len(named) == 0 {
		for _, g := range grabbed {
			named[g.Season] = true
		}
	}
	if len(named) != 1 {
		return 0
	}
	for s := range named {
		if s >= 1 {
			return s
		}
	}
	return 0
}

// inDownloadSeason reads a file named by a number and no season ("Show -
// 07") in the season its download is of. eps and reason are what the count
// through the series gave (MatchDownloaded); ok is true, and own the
// season's episodes, when that count did not land in the season -- it named
// another season's episodes, or none -- and the season has every number.
//
// A count that lands in the season stands ("Show - 14" in a pack of season
// 2 is its second episode where the group counts on), and so does one the
// season has no such episode for: a pack titled for one season that holds
// the cour TVDB lists as the next is filed by its count, as before. A name
// that says its season -- "S02E07", "Show S2 - 07", a title that is a later
// season's scene name -- is not asked: the name, not the download, says
// what such a file is.
//
// Before this a number was counted through the series whatever the
// download was: a season 2 pack whose files restart at 01 was filed as
// season 1, over the files there or beside them (talos: Ranma ½ (2024)'s
// season 2 pack on 2026-10-28, twelve files that are season 2's by their
// running times, and "[Cleo]Date A Live II - 07" on 2026-11-05 where the
// series' scene names were not yet read; found 2026-11-27).
func inDownloadSeason(
	p *release.ParsedRelease, eps []EpisodeCandidate, reason string, cands []EpisodeCandidate, id decision.Identity, season int,
) (own []EpisodeCandidate, ok bool) {
	if season < 1 || len(p.Absolute) == 0 || len(p.Seasons) > 0 || len(p.Episodes) > 0 || decision.NamedSeason(p, id) > 0 {
		return nil, false
	}
	if reason == "" && !slices.ContainsFunc(eps, func(e EpisodeCandidate) bool { return e.Season != season }) {
		return nil, false
	}
	places := make([]decision.SeasonEpisode, len(p.Absolute))
	for i, n := range p.Absolute {
		places[i] = decision.SeasonEpisode{Season: season, Episode: n}
	}
	return candidatesAt(places, cands)
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
//   - On an anime series, a season 1 pair neither numbering has is read as
//     counting through the series (countedThrough): "One.Piece.S01E1157"
//     is the episode whose absolute number is 1157.
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
		if reason != "" && season == 1 && seriesType == catalogv1alpha1.SeriesTypeAnime {
			if counted, ok := countedThrough(p.Episodes, cands); ok {
				eps, reason = counted, ""
			}
		}
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

// countedThrough reads season 1 numbers that count through the whole series
// -- a streaming service's one season for a long-running anime, kept by
// the groups that take its files ("One.Piece.S01E1157", TVDB's S23E02) --
// as pkg/decision's identity check reads them for the grab
// (sceneIndex.countedThrough): a number season 1 has is season 1's, and one
// it lacks is the one regular episode holding it as its absolute number.
// ok is false when any number is neither; nothing is guessed between two
// episodes that share one.
//
// Before this such a file named an episode the series does not have, in the
// importer and the scanner alike (2026-11-28).
func countedThrough(numbers []int, cands []EpisodeCandidate) ([]EpisodeCandidate, bool) {
	out := make([]EpisodeCandidate, 0, len(numbers))
	for _, n := range numbers {
		if n < 1 {
			return nil, false // 0 is "no absolute number", not one to look up
		}
		if i := slices.IndexFunc(cands, func(c EpisodeCandidate) bool { return c.Season == 1 && c.Number == n }); i >= 0 {
			out = append(out, cands[i])
			continue
		}
		at := -1
		for i, c := range cands {
			if c.Season < 1 || c.Absolute != n {
				continue
			}
			if at >= 0 {
				return nil, false
			}
			at = i
		}
		if at < 0 {
			return nil, false
		}
		out = append(out, cands[at])
	}
	return out, len(out) > 0
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

// titledNumberRegex finds a number that may follow a title in a file name:
// set off by a separator and ending the name or followed by one. leadGroups
// are the bracketed groups a name may open with.
var (
	titledNumberRegex = regexp.MustCompile(`[\s._-]+(\d{1,3})(?:v\d+)?(?:$|[\s._\-\[\(])`)
	leadGroupsRegex   = regexp.MustCompile(`^(?:\[[^\]]*\][\s._-]*)+`)
)

// titledEpisode reads a pack's file named "<the series' title> <number>",
// with or without the episode's own title after it, and no dash, group or
// season for a release pattern to hold on to: "Wandering Witch The Journey
// of Elaina 01", "[Group] Show 07v2 [1080p]", "Gosick 01 The Dark Reaper
// Finds the Golden Fairy". A release pattern cannot take that shape, since a
// title may itself end in a number and an episode's title may hold one.
// Here it can be read, because the title is known: what stands before the
// number must be one of titles, whole (the series' own and its alternates,
// compared as release.CleanTitle), so the number is the first one a title
// ends at, and the season is its download's (downloadSeason), as for
// bareEpisode. Nil otherwise.
//
// On talos a twelve-file pack of Wandering Witch: The Journey of Elaina
// ("Wandering Witch The Journey of Elaina 01.mkv" to 12) was downloaded
// whole and held for a person, eleven of its episodes missing, and so was a
// twenty-four-file pack of Gosick, named with each episode's title
// (2026-11-27).
func titledEpisode(path string, titles []string, season int) *release.ParsedRelease {
	if season < 1 {
		return nil
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = base[len(leadGroupsRegex.FindString(base)):]
	clean := make(map[string]bool, len(titles))
	for _, t := range titles {
		if c := release.CleanTitle(t); c != "" {
			clean[c] = true
		}
	}
	// Each number in turn, a title's own among them ("Mob Psycho 100 01"):
	// the search moves on from a number's last digit, so the separator after
	// it still opens the next.
	for from := 0; from < len(base); {
		m := titledNumberRegex.FindStringSubmatchIndex(base[from:])
		if m == nil {
			return nil
		}
		name, number := base[:from+m[0]], base[from+m[2]:from+m[3]]
		from += m[3]
		n, err := strconv.Atoi(number)
		if err != nil || n < 1 || !clean[release.CleanTitle(name)] || numberNext(base[from:]) {
			continue
		}
		return &release.ParsedRelease{
			Seasons: []int{season}, Episodes: []int{n},
			LanguageUnknown: true,
			ReleaseType:     commonv1.ReleaseTypeSingle,
		}
	}
	return nil
}

// numberNext reports a rest of a file name that goes on with another number
// ("Show 2 01": a sequel's title and its episode, not this series' second).
func numberNext(rest string) bool {
	rest = strings.TrimLeft(rest, " ._-")
	if i := strings.IndexAny(rest, " ._-[("); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// seriesTitles are the titles a series is released under: its own and its
// alternates.
func seriesTitles(s *catalogv1alpha1.Series) []string {
	if s == nil {
		return nil
	}
	var out []string
	if md := s.Status.Metadata; md != nil {
		out = append(out, md.Title)
		for _, alt := range md.AlternateTitles {
			out = append(out, alt.Title)
		}
	}
	return out
}

// bareEpisode reads a downloaded file named for its episode alone, which no
// release pattern does: a release name opens with its title, and a pack's
// files often carry none. The season is the one the name writes ("S02EP01")
// or, for a name with the episode only ("Episode 01 - The Wind Blows"),
// downloadSeason: the one season its download is of, by its release title
// or the episodes it was grabbed for, 0 when it is of none or several. Nil
// when the name is not of that shape or names no season either way. The numbering is the release's, so it is
// read through the series' numbering as any other (MatchDownloaded); the
// quality, group and languages are the release's (fillFromRelease).
//
// On talos two packs downloaded whole were held as unparseable and removed
// (Fog Hill of Five Elements' season 2, Ushio and Tora's, 2026-11-24).
func bareEpisode(path string, downloadSeason int) *release.ParsedRelease {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	season, number := 0, ""
	if m := bareSeasonEpisodeRegex.FindStringSubmatch(base); m != nil {
		season, _ = strconv.Atoi(m[1])
		number = m[2]
	} else if m := bareEpisodeRegex.FindStringSubmatch(base); m != nil {
		season, number = downloadSeason, m[1]
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
