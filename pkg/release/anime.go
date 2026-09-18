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
	"strings"

	"github.com/dlclark/regexp2"
)

// This file exists because docs/research/quality.md §7.3 verified that
// moistari/rls gets anime bracket-groups wrong: it drops the group entirely
// for "[SubsPlease] Frieren - 28 ..." and mis-sets it to a language token
// ("ARA") for "[Erai-raws] One Piece - 1090 ...". These are exactly the two
// titles that pattern is quoted from, covered verbatim below and in the
// anime.json fixture.

// animeGroupPrefixRegex matches a leading bracketed sub-group token, e.g.
// "[SubsPlease] ".
var animeGroupPrefixRegex = mustCompile(`^\[(?<group>[^\]]+)\][\s_.]*`, regexp2.IgnoreCase)

// animeSeasonEpisodeRegex matches an anime title that also carries a
// standard S/E pair, e.g. "Attack on Titan - S04E28".
var animeSeasonEpisodeRegex = mustCompile(`(?<title>.+?)\s*-\s*S(?<season>\d{1,2})E(?<episode>\d{1,4})`, regexp2.IgnoreCase)

// animeAbsoluteRegex matches an anime title with a bare absolute episode
// number, e.g. "Frieren - 28 (1080p)".
var animeAbsoluteRegex = mustCompile(`(?<title>.+?)\s*-\s*(?<abs>\d{2,4})(?:\s*\((?<res>\d{3,4}p)\))?`, regexp2.IgnoreCase)

// parseAnime consumes the leading bracket group (if any) from title, then
// matches the remainder against the season+episode pattern before falling
// back to the absolute-episode pattern, filling p.Title/p.Group/p.Seasons/
// p.Episodes/p.Absolute directly. It deliberately does not call the generic
// parseGroup (group.go): that regex has no anime-bracket awareness and would
// reintroduce the very rls bug this file exists to avoid.
func parseAnime(title string, p *ParsedRelease) error {
	work := title

	m, err := animeGroupPrefixRegex.FindStringMatch(work)
	if err != nil {
		return fmt.Errorf("release: anime: group match: %w", err)
	}
	if m != nil {
		if g := m.GroupByName("group"); g != nil && len(g.Captures) > 0 {
			p.Group = g.String()
		}
		work = work[len(m.String()):]
	}

	if m, err := animeSeasonEpisodeRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: season/episode match: %w", err)
	} else if m != nil {
		p.Title = strings.TrimSpace(m.GroupByName("title").String())
		season, convErr := strconv.Atoi(m.GroupByName("season").String())
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing season: %w", convErr)
		}
		episode, convErr := strconv.Atoi(m.GroupByName("episode").String())
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing episode: %w", convErr)
		}
		p.Seasons, p.Episodes = []int{season}, []int{episode}
		return nil
	}

	if m, err := animeAbsoluteRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: absolute match: %w", err)
	} else if m != nil {
		p.Title = strings.TrimSpace(m.GroupByName("title").String())
		abs, convErr := strconv.Atoi(m.GroupByName("abs").String())
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing absolute: %w", convErr)
		}
		p.Absolute = []int{abs}
		return nil
	}

	return fmt.Errorf("release: %q does not match any anime title pattern", title)
}

// parseAnimeSeries parses an anime release title. Quality/revision are
// parsed from the whole original title (parseAnime only consumes the
// leading group and title/episode tokens; resolution/source tokens live
// after them), and the release group comes from parseAnime, not the
// generic parseGroup.
func parseAnimeSeries(title string) (*ParsedRelease, error) {
	p := &ParsedRelease{}
	if err := parseAnime(title, p); err != nil {
		return nil, err
	}

	q, rev, _, _ := parseQualityTags(title)
	p.Quality = q
	p.Revision = rev
	p.ReleaseType = releaseTypeForEpisodes(p.Episodes)
	p.Hints = parseHints(title)
	return p, nil
}
