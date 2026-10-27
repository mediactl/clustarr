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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
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
// standard S/E pair, after a dash ("Attack on Titan - S04E28") or a bare
// separator ("Vivy_-_Fluorite_Eye's_Song_S01E01v1_2021_1080p_WEB-DL...",
// whose title holds the only dash; 2026-10-27), the episode's fansub
// version allowed as in tv.go's patterns. The title ends in a letter or
// digit, so a name that opens with its numbering ("[tag]-[S01E01-...")
// stays titleless, and no dash-range follows the episode, so the standard
// parser's refusal of "S01E12-E01" is not rescued here (tv_test's guard).
var animeSeasonEpisodeRegex = mustCompile(
	`(?<title>.*?[\p{L}\p{N}])[\s._]*(?:-[\s._]*)?S(?<season>\d{1,2})E(?<episode>\d{1,4})(?:v\d+)?(?!-E?\d)(?:[\s._\[\(-]|$)`,
	regexp2.IgnoreCase,
)

// animeAbsoluteRegex matches an anime title with a bare absolute episode
// number, e.g. "Frieren - 28 (1080p)". The optional
// "(?:(?:Episode|Ep|E|#)\s+)?" before the number tolerates exactly the
// small, explicit set of episode-marker words/symbols real releases use
// ("Show - Episode 12", "Show - Ep 12") — not an arbitrary word. An earlier
// version of this pattern tolerated *any* single word here (fix round 3,
// finding 2), which also silently accepted a genuinely different word like
// "Season" ("Some Show - Season 12" wrongly yielding absolute episode 12);
// this allowlist deliberately does not, so a title using an unrecognized
// word falls through to the "no anime pattern" error instead.
//
// The marker requires *mandatory* trailing whitespace (\s+, not \s*): a
// zero-width version briefly regressed the standard-series dash-range
// guard tests (tv.go's validEpisodeRange) — "The.Wire.S01E12-E01..."'s
// rejected, invalid range was being "rescued" by parseSeries's anime
// last-resort fallback, because the "E" marker's zero-width trailing space
// let it match the "E" in "E01" directly (no space before "01") the way a
// real "Show - E01" would with a space. Requiring at least one space after
// the marker closes that gap — verified with a scratch regexp2 check
// against exactly that title before landing this — while still matching
// every review-required case ("Show - Episode 12") and every title in the
// anime fixture corpus (test/data/releases/anime.json) unchanged, since
// none of them has a marker word between the dash and the digits at all.
var animeAbsoluteRegex = mustCompile(
	`(?<title>.+?)\s*-\s*(?:(?:Episode|Ep|E|#)\s+)?(?<abs>\d{2,4})(?:\s*\((?<res>\d{3,4}p)\))?`,
	regexp2.IgnoreCase,
)

// animeBatchRegex matches an anime batch/pack release naming its absolute
// episode range: "Title - 01-12", "Title - 01~12", "Title - 01 - 12" and
// "Title - (01-24)"/"Title (01-24)" (the range itself optionally
// parenthesized). The leading "-" and "(" are both optional so a single
// lazy title-group expansion converges on the right boundary in one
// attempt, rather than possibly absorbing the dash into the title before
// backtracking onto the paren (which previously produced "Frieren -"
// instead of "Frieren" for the parenthesized form).
var animeBatchRegex = mustCompile(
	`(?<title>.+?)\s*-?\s*\(?(?<start>\d{2,4})\s*[-~]\s*(?<end>\d{2,4})\)?(?:\s*\((?<res>\d{3,4}p)\))?`,
	regexp2.IgnoreCase,
)

// animeSpecialRegex matches an anime special/OVA/OAD release that names an
// episode-marker token instead of a plain absolute number, e.g.
// "My Hero Academia - OVA 01 (1080p)". Tried before animeBatchRegex and
// animeAbsoluteRegex, both of which require digits immediately after the
// title separator and so can't match this shape at all.
//
// The trailing \b (matching specialTokenRegex's own word boundary,
// classify.go) is load-bearing: without it, "OVA" matches as a bare prefix
// of an unrelated word like "OVAN", wrongly flagging Special and — since
// the match then stops right after "OVA", before the "N" — leaving the
// rest of the tag ("N 01") unconsumed, so the episode number after it
// never gets captured either.
var animeSpecialRegex = mustCompile(
	`(?<title>.+?)\s*-\s*(?<tag>SPECIAL|OVA|OAD|NCOP|NCED)\b\.?\s*(?<num>\d{1,4})?`,
	regexp2.IgnoreCase,
)

// animeSeasonPackRegex matches a fansub season pack naming its season in
// words or as "S01", often in brackets and beside the title's other names:
// "86: Eighty-Six Season 1 (BD 1080p ...)", "Apocalypse Hotel (Season 01)",
// "Eighty Six - Season 01 (S01) (...)", "86 | 86 Eighty-Six (Season 1-2+Specials)".
// The marker must stand alone (not the S of "Six") and be followed by no
// episode -- "Frieren S2 - 05" is episode 5, not a pack (and not seasons 2
// to 5: a range is "1-2" with no spaces), and an S01E02 pair is
// animeSeasonEpisodeRegex's. Talos's episode searches could not parse
// any of these (2026-10-21).
var animeSeasonPackRegex = mustCompile(
	`^(?<title>.+?)[\s._:\-]*[\(\[]?\s*(?<![A-Za-z0-9])(?:Season\s*|S)(?<s1>\d{1,2})(?:[-~](?:S|Season\s*)?(?<s2>\d{1,2}))?(?!\d)(?!\s*-\s*\d)(?![\s._]*E\d)`,
	regexp2.IgnoreCase,
)

// animeYearAbsoluteRegex matches an absolute episode after the series'
// year: "Pocket Monsters (2023) 152 (1080p) [B3545811]".
var animeYearAbsoluteRegex = mustCompile(
	`^(?<title>.+?)\s*\((?<year>(?:19|20)\d{2})\)\s*(?:-\s*)?(?<abs>\d{2,4})(?:v\d)?(?=\s*[\(\[]|\s*$)`,
	regexp2.IgnoreCase,
)

// animeSceneEpisodeRegex matches the German and French scene's anime
// episode, which numbers it E01 with no season, as the series runs:
// "Monster.E01.Herr.Dr.Tenma.German.2004.ANiME.DL...-STARS",
// "Kanata.No.Astra.E01.1080p.WEB.x264-URANiME". The title is one dotted
// token; a season before the E (Show.S01.E01) is not this shape.
var animeSceneEpisodeRegex = mustCompile(`^(?<title>[^\s\[\]()]+?)[._]E(?<abs>\d{2,4})(?=[._-]|$)`, regexp2.IgnoreCase)

// sceneSeasonTokenRegex finds a season token in a dotted title.
var sceneSeasonTokenRegex = mustCompile(`(?:^|[._])S\d{1,2}(?:$|[._])`, regexp2.IgnoreCase)

// animeTitleYearRegex is a "(2021)" ending a pack's title.
var animeTitleYearRegex = mustCompile(`\s*\((?<year>(?:19|20)\d{2})\)\s*$`, regexp2.None)

// animeTitles is a pack's title as the release writes it, then its other
// names: each side of a " | ", and the name outside and the name inside one
// parenthesis -- "86 (Eighty Six)" is 86 and Eighty Six too.
func animeTitles(title string) []string {
	out := []string{title}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !slicesContainsFold(out, s) {
			out = append(out, s)
		}
	}
	if strings.Contains(title, "|") {
		for part := range strings.SplitSeq(title, "|") {
			add(part)
		}
	}
	if open := strings.Index(title, "("); open > 0 {
		if end := strings.Index(title[open:], ")"); end > 1 {
			add(title[:open] + title[open+end+1:])
			add(title[open+1 : open+end])
		}
	}
	return out
}

func slicesContainsFold(s []string, v string) bool {
	for _, x := range s {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

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
		work = textAfterMatch(work, m)
	}

	if m, err := animeSeasonEpisodeRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: season/episode match: %w", err)
	} else if m != nil {
		p.Title = cleanTitleSeparators(groupString(m, "title"))
		season, convErr := strconv.Atoi(groupString(m, "season"))
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing season: %w", convErr)
		}
		episode, convErr := strconv.Atoi(groupString(m, "episode"))
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing episode: %w", convErr)
		}
		p.Seasons, p.Episodes = []int{season}, []int{episode}
		return nil
	}

	if m, err := animeSpecialRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: special match: %w", err)
	} else if m != nil {
		p.Title = strings.TrimSpace(groupString(m, "title"))
		p.Special = true
		if numGrp := m.GroupByName("num"); numGrp != nil && len(numGrp.Captures) > 0 {
			if n, convErr := strconv.Atoi(numGrp.String()); convErr == nil {
				p.Absolute = []int{n}
			}
		}
		return nil
	}

	if m, err := animeSeasonPackRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: season pack match: %w", err)
	} else if m != nil {
		name := strings.TrimRight(strings.TrimSpace(groupString(m, "title")), " -:([")
		if y, yerr := animeTitleYearRegex.FindStringMatch(name); yerr == nil && y != nil {
			p.Year, _ = strconv.Atoi(groupString(y, "year"))
			name = strings.TrimSpace(name[:y.Index])
		}
		s1, convErr := strconv.Atoi(groupString(m, "s1"))
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing season: %w", convErr)
		}
		p.Title, p.Titles = name, animeTitles(name)
		// Every season it names is whole, as a standard "S01-S02" pack's is.
		p.Seasons, p.FullSeason = []int{s1}, true
		if s2, s2Err := strconv.Atoi(groupString(m, "s2")); s2Err == nil && s2 > s1 {
			p.Seasons, p.MultiSeason = intRange(s1, s2), true
		}
		return nil
	}

	if m, err := animeBatchRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: batch match: %w", err)
	} else if m != nil {
		start, startErr := strconv.Atoi(groupString(m, "start"))
		end, endErr := strconv.Atoi(groupString(m, "end"))
		if startErr == nil && endErr == nil && validEpisodeRange(start, end) {
			p.Title = strings.TrimSpace(groupString(m, "title"))
			p.Absolute = intRange(start, end)
			p.Partial = true
			return nil
		}
		// Descending or implausibly long: not a batch after all — fall
		// through to animeAbsoluteRegex, which will pick up the leading
		// number as a single absolute episode instead.
	}

	if m, err := animeAbsoluteRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: absolute match: %w", err)
	} else if m != nil {
		p.Title = strings.TrimSpace(groupString(m, "title"))
		abs, convErr := strconv.Atoi(groupString(m, "abs"))
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing absolute: %w", convErr)
		}
		p.Absolute = []int{abs}
		return nil
	}

	if m, err := animeYearAbsoluteRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: year absolute match: %w", err)
	} else if m != nil {
		p.Title = strings.TrimSpace(groupString(m, "title"))
		p.Year, _ = strconv.Atoi(groupString(m, "year"))
		abs, convErr := strconv.Atoi(groupString(m, "abs"))
		if convErr != nil {
			return fmt.Errorf("release: anime: parsing absolute: %w", convErr)
		}
		p.Absolute = []int{abs}
		return nil
	}

	if m, err := animeSceneEpisodeRegex.FindStringMatch(work); err != nil {
		return fmt.Errorf("release: anime: scene episode match: %w", err)
	} else if m != nil {
		dotted := groupString(m, "title")
		if seasoned, serr := sceneSeasonTokenRegex.MatchString(dotted); serr == nil && !seasoned {
			abs, convErr := strconv.Atoi(groupString(m, "abs"))
			if convErr != nil {
				return fmt.Errorf("release: anime: parsing scene episode: %w", convErr)
			}
			p.Title = cleanTitleSeparators(dotted)
			p.Absolute = []int{abs}
			if p.Group == "" {
				p.Group, _, _ = parseGroup(title)
			}
			return nil
		}
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
	if p.Group == "" {
		// No bracketed sub-group: the group is the scene's trailing one
		// ("-ZR-"), read as the scene-episode shape already does.
		p.Group, _, _ = parseGroup(title)
	}
	switch {
	case p.Partial:
		// An expanded absolute-episode batch range (animeBatchRegex above)
		// is a pack, same as a season pack for standard series.
		p.ReleaseType = commonv1.ReleaseTypeMulti
	case len(p.Absolute) > 1:
		p.ReleaseType = commonv1.ReleaseTypeMulti
	case len(p.Seasons) > 0 && len(p.Episodes) == 0 && len(p.Absolute) == 0:
		// A fansub season pack (animeSeasonPackRegex).
		p.ReleaseType = commonv1.ReleaseTypeSeasonPack
	default:
		p.ReleaseType = releaseTypeForEpisodes(p.Episodes)
	}
	p.Hints = parseHints(title)
	// Sonarr parses languages out of anime titles like any other. Unlike a
	// standard title an anime one keeps no English default when it names
	// none: its Languages stay nil, as they always have here.
	p.Languages, p.DualAudio, p.NamesOriginal = namedLanguages(title)
	p.LanguageUnknown = p.Languages == nil
	return p, nil
}
