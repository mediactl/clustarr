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

package release

import (
	"fmt"
	"strconv"

	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// dashRangeEpisodeRegex matches Sonarr's dash-range multi-episode form,
// "S05E01-E03".
var dashRangeEpisodeRegex = mustCompile(
	`(?<title>.+?)[.\s_]S(?<season>\d{1,2})E(?<start>\d{1,4})-E?(?<end>\d{1,4})(?:[.\s_]|$)`,
	regexp2.IgnoreCase,
)

// multiEpisodeRegex matches both the single-episode form ("S02E03") and the
// dual/triple-token multi-episode form ("S03E01E02"): the repeated "ep"
// group collects one capture per E-token (regexp2, like .NET, keeps every
// capture of a repeated group in Group.Captures).
var multiEpisodeRegex = mustCompile(
	`(?<title>.+?)[.\s_]S(?<season>\d{1,2})(?:E(?<ep>\d{1,4}))+(?:[.\s_]|$)`,
	regexp2.IgnoreCase,
)

// seasonOnlyRegex matches a season pack, with an optional "-S03"-style
// range for a multi-season pack. It is only tried once the episode regexes
// above have both failed to match.
var seasonOnlyRegex = mustCompile(
	`(?<title>.+?)[.\s_]S(?<s1>\d{1,2})(?:-S?(?<s2>\d{1,2}))?(?:[.\s_]|$)`,
	regexp2.IgnoreCase,
)

// partialSeasonRegex detects a "Part2"-style suffix on a season pack.
var partialSeasonRegex = mustCompile(`part\s?\d`, regexp2.IgnoreCase)

// parseSeries parses a TV release title according to o.SeriesType
// ("standard" when empty, "daily" or "anime").
//
// When SeriesType is pinned, only that family is tried — a caller who
// already knows a series is daily or anime gets that family's errors
// verbatim, not a confusing standard-family mismatch. When it is left at
// "standard" (the default), ParseKind and ClassifyKind have no way to
// signal SeriesType at all (per their documented signatures), so this is
// what makes ParseKind(title, MediaKindEpisode) usable for a daily or anime
// title without the caller reaching for the lower-level Parse+Options entry
// point: a leading "[Group] " bracket is tried as anime first, since it's
// an unambiguous anime signal even when the rest of the title also happens
// to contain a syntactically valid S/E token (e.g.
// "[SubsPlease] Attack on Titan - S04E28 ..." would otherwise match the
// standard family "accidentally", leaving the bracket group embedded in the
// title instead of parsed out); anything else tries standard, then daily,
// then anime, before giving up.
//
// At every stage, a regexp2 MatchTimeout (isRegexTimeout, quality.go) is
// returned immediately rather than treated as "this family didn't match" —
// a timeout means the engine gave up on a pathological input, not that the
// title is genuinely some other shape, so falling through to the next
// family would both waste the same budget again and mask the real failure.
func parseSeries(title string, o Options) (*ParsedRelease, error) {
	switch o.SeriesType {
	case "daily":
		return parseDailySeries(title)
	case "anime":
		return parseAnimeSeries(title)
	default:
		if ok, err := animeBracketPrefixRegex.MatchString(title); err != nil {
			if isRegexTimeout(err) {
				return nil, fmt.Errorf("release: series: anime-prefix check: %w", err)
			}
		} else if ok {
			p, err := parseAnimeSeries(title)
			if err == nil {
				return p, nil
			}
			if isRegexTimeout(err) {
				return nil, err
			}
		}

		if p, err := parseStandardSeries(title); err == nil {
			return p, nil
		} else if isRegexTimeout(err) {
			return nil, err
		}

		if p, err := parseDailySeries(title); err == nil {
			return p, nil
		} else if isRegexTimeout(err) {
			return nil, err
		}

		return parseAnimeSeries(title)
	}
}

func intRange(a, b int) []int {
	if b < a {
		a, b = b, a
	}
	r := make([]int, 0, b-a+1)
	for i := a; i <= b; i++ {
		r = append(r, i)
	}
	return r
}

// maxEpisodeRangeSize caps how large a dash-range multi-episode span
// (dashRangeEpisodeRegex, below) or an anime batch range (animeBatchRegex,
// anime.go) is allowed to expand to. A real release never spans this many
// episodes, so anything bigger is a parse collision, not a legitimate
// multi-episode/batch release.
const maxEpisodeRangeSize = 500

// validEpisodeRange reports whether [start, end] is a plausible inclusive
// episode range: ascending (not descending — unlike intRange, this does not
// silently swap the endpoints) and no larger than maxEpisodeRangeSize.
func validEpisodeRange(start, end int) bool {
	return end >= start && end-start+1 <= maxEpisodeRangeSize
}

func releaseTypeForEpisodes(episodes []int) commonv1.ReleaseType {
	if len(episodes) > 1 {
		return commonv1.ReleaseTypeMulti
	}
	return commonv1.ReleaseTypeSingle
}

// finishSeries fills in the fields common to every standard/daily series
// match from the full, original title (quality/revision/group tokens live
// after the part of the title the episode regexes consumed).
func finishSeries(title string, p *ParsedRelease) *ParsedRelease {
	q, rev, _, _ := parseQualityTags(title)
	group, hash, edition := parseGroup(title)
	p.Quality = q
	p.Revision = rev
	p.Group = group
	p.Hash = hash
	p.Edition = edition
	p.Hints = parseHints(title)
	p.Languages = parseLanguages(title)
	return p
}

// atoiGroup parses the named capture group's text as a base-10 int, wrapping
// any error with the group name for context. It is shared by every file in
// this package that pulls numeric fields out of a regexp2.Match.
func atoiGroup(m *regexp2.Match, name string) (int, error) {
	v, err := strconv.Atoi(m.GroupByName(name).String())
	if err != nil {
		return 0, fmt.Errorf("release: parsing %s: %w", name, err)
	}
	return v, nil
}

// parseStandardSeries tries, in order: dash-range multi-episode, single/
// multi-episode, then season-only (full/multi/partial season pack).
func parseStandardSeries(title string) (*ParsedRelease, error) {
	if m, err := dashRangeEpisodeRegex.FindStringMatch(title); err != nil {
		return nil, fmt.Errorf("release: series: dash-range match: %w", err)
	} else if m != nil {
		season, err := atoiGroup(m, "season")
		if err != nil {
			return nil, err
		}
		start, err := atoiGroup(m, "start")
		if err != nil {
			return nil, err
		}
		end, err := atoiGroup(m, "end")
		if err != nil {
			return nil, err
		}
		if validEpisodeRange(start, end) {
			p := &ParsedRelease{
				Title:    cleanTitleSeparators(m.GroupByName("title").String()),
				Seasons:  []int{season},
				Episodes: intRange(start, end),
			}
			p.ReleaseType = releaseTypeForEpisodes(p.Episodes)
			return finishSeries(title, p), nil
		}
		// Descending or implausibly long: not a legitimate range — fall
		// through to the other standard-family patterns below rather than
		// silently reordering or truncating it.
	}

	if m, err := multiEpisodeRegex.FindStringMatch(title); err != nil {
		return nil, fmt.Errorf("release: series: multi-episode match: %w", err)
	} else if m != nil {
		season, err := atoiGroup(m, "season")
		if err != nil {
			return nil, err
		}
		epGroup := m.GroupByName("ep")
		episodes := make([]int, 0, len(epGroup.Captures))
		for _, c := range epGroup.Captures {
			v, convErr := strconv.Atoi(c.String())
			if convErr != nil {
				return nil, fmt.Errorf("release: series: parsing episode: %w", convErr)
			}
			episodes = append(episodes, v)
		}
		p := &ParsedRelease{
			Title:    cleanTitleSeparators(m.GroupByName("title").String()),
			Seasons:  []int{season},
			Episodes: episodes,
		}
		p.ReleaseType = releaseTypeForEpisodes(episodes)
		return finishSeries(title, p), nil
	}

	if m, err := seasonOnlyRegex.FindStringMatch(title); err != nil {
		return nil, fmt.Errorf("release: series: season-only match: %w", err)
	} else if m != nil {
		s1, err := atoiGroup(m, "s1")
		if err != nil {
			return nil, err
		}
		p := &ParsedRelease{Title: cleanTitleSeparators(m.GroupByName("title").String())}
		if s2grp := m.GroupByName("s2"); s2grp != nil && len(s2grp.Captures) > 0 {
			s2, convErr := strconv.Atoi(s2grp.String())
			if convErr != nil {
				return nil, fmt.Errorf("release: series: parsing s2: %w", convErr)
			}
			p.Seasons = intRange(s1, s2)
			p.MultiSeason = true
		} else {
			p.Seasons = []int{s1}
		}
		partial, perr := partialSeasonRegex.MatchString(title)
		if perr != nil {
			return nil, fmt.Errorf("release: series: partial-season match: %w", perr)
		}
		if partial {
			p.Partial = true
		} else {
			p.FullSeason = true
		}
		p.ReleaseType = commonv1.ReleaseTypeSeasonPack
		return finishSeries(title, p), nil
	}

	return nil, fmt.Errorf("release: %q does not match any standard series title pattern", title)
}
