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

package naming

import (
	"fmt"
	"strings"
)

const (
	episodeFileStandardTemplate = "{Series CleanTitleWithoutYear}{ (Series Year)} - S{season:00}E{episode:00}{ - Episode CleanTitle:90}{ [Quality Full]}{-Release Group}"
	episodeFileAnimeTemplate    = "{Series CleanTitleWithoutYear}{ (Series Year)} - S{season:00}E{episode:00} - {absolute:000}{ - Episode CleanTitle:90}{ [Quality Full]}{-Release Group}"
	episodeFileDailyTemplate    = "{Series CleanTitleWithoutYear}{ (Series Year)} - {Air-Date}{ - Episode CleanTitle:90}{ [Quality Full]}{-Release Group}"
)

// formatAbsoluteRange joins anime absolute episode numbers, following the
// same multi-episode style as formatEpisodeRange but zero-padded to three
// digits per the *arr absolute-numbering convention.
func formatAbsoluteRange(absolute []int, style MultiEpisodeStyle) string {
	if len(absolute) == 0 {
		return ""
	}
	if len(absolute) == 1 {
		return fmt.Sprintf("%03d", absolute[0])
	}
	switch style {
	case MultiEpisodeRange, MultiEpisodePrefixedRange:
		return fmt.Sprintf("%03d-%03d", absolute[0], absolute[len(absolute)-1])
	case MultiEpisodeDuplicate:
		parts := make([]string, len(absolute))
		for i, a := range absolute {
			parts[i] = fmt.Sprintf("%03d", a)
		}
		return strings.Join(parts, ".")
	default: // extend, repeat, scene: dash-joined list
		parts := make([]string, len(absolute))
		for i, a := range absolute {
			parts[i] = fmt.Sprintf("%03d", a)
		}
		return strings.Join(parts, "-")
	}
}

// formatEpisodeRange joins season/episode numbers per the six
// MultiEpisodeStyle values, following the *arr naming-token tables. A
// single episode always renders as the plain "S01E01" form regardless of
// style.
func formatEpisodeRange(season int, episodes []int, style MultiEpisodeStyle) string {
	if len(episodes) == 0 {
		return ""
	}
	if len(episodes) == 1 {
		return fmt.Sprintf("S%02dE%02d", season, episodes[0])
	}
	switch style {
	case MultiEpisodeDuplicate:
		parts := make([]string, len(episodes))
		for i, ep := range episodes {
			parts[i] = fmt.Sprintf("S%02dE%02d", season, ep)
		}
		return strings.Join(parts, ".")
	case MultiEpisodeRepeat:
		var b strings.Builder
		fmt.Fprintf(&b, "S%02d", season)
		for _, ep := range episodes {
			fmt.Fprintf(&b, "E%02d", ep)
		}
		return b.String()
	case MultiEpisodeScene:
		var b strings.Builder
		fmt.Fprintf(&b, "S%02dE%02d", season, episodes[0])
		for _, ep := range episodes[1:] {
			fmt.Fprintf(&b, "-E%02d", ep)
		}
		return b.String()
	case MultiEpisodeRange:
		return fmt.Sprintf("S%02dE%02d-%02d", season, episodes[0], episodes[len(episodes)-1])
	case MultiEpisodePrefixedRange:
		return fmt.Sprintf("S%02dE%02d-E%02d", season, episodes[0], episodes[len(episodes)-1])
	default: // MultiEpisodeExtend and the zero value
		var b strings.Builder
		fmt.Fprintf(&b, "S%02dE%02d", season, episodes[0])
		for _, ep := range episodes[1:] {
			fmt.Fprintf(&b, "-%02d", ep)
		}
		return b.String()
	}
}

// EpisodeFile renders the episode file name, picking the standard, anime or
// daily form: non-empty Absolute numbering selects anime, else a non-nil
// AirDate selects daily, else standard.
func (e Engine) EpisodeFile(c Context) (string, error) {
	key, tmpl := TokenEpisodeFile, episodeFileStandardTemplate
	switch {
	case len(c.Absolute) > 0:
		key, tmpl = TokenAnimeFile, episodeFileAnimeTemplate
	case c.AirDate != nil:
		key, tmpl = TokenDailyFile, episodeFileDailyTemplate
	}
	return e.Render(e.overrideOr(key, tmpl), c)
}

// SeasonFolder renders the season subfolder name. Season 0 is specials:
// every dialect but Kodi names it "Season 00"; Kodi's own convention (and
// its NFO-driven scrapers) expects the literal folder name "Specials".
func (e Engine) SeasonFolder(c Context) (string, error) {
	if c.Season == 0 {
		if e.Config.Dialect == DialectKodi {
			return "Specials", nil
		}
		return "Season 00", nil
	}
	return fmt.Sprintf("Season %02d", c.Season), nil
}

// SeriesFolder renders the series folder name using the dialect's preset
// (or Config.Overrides[TokenSeriesFolder] when set).
func (e Engine) SeriesFolder(c Context) (string, error) {
	tmpl := e.overrideOr(TokenSeriesFolder, seriesFolderTemplate(e.Config.Dialect))
	return e.Render(tmpl, c)
}

// seriesFolderTemplate is the per-dialect default for SeriesFolder. It is
// inlined here for Step 6; Step 12 relocates the equivalent movie preset to
// preset.go without changing behaviour -- this one is left in series.go
// deliberately, so a later commit does not carry an unrelated file move.
func seriesFolderTemplate(d Dialect) string {
	switch d {
	case DialectPlex:
		return "{Series CleanTitleWithoutYear}{ (Series Year)} {tvdb-{TvdbId}}"
	case DialectEmby:
		return "{Series CleanTitleWithoutYear}{ (Series Year)} [tvdb-{TvdbId}]"
	case DialectKodi:
		return "{Series CleanTitleWithoutYear}{ (Series Year)}"
	default: // Jellyfin
		return "{Series CleanTitleWithoutYear}{ (Series Year)} [tvdbid-{TvdbId}]"
	}
}
