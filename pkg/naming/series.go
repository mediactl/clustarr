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

import "fmt"

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
		return "{Series CleanTitleWithoutYear} ({Series Year}) {tvdb-{TvdbId}}"
	case DialectEmby:
		return "{Series CleanTitleWithoutYear} ({Series Year}) [tvdb-{TvdbId}]"
	case DialectKodi:
		return "{Series CleanTitleWithoutYear} ({Series Year})"
	default: // Jellyfin
		return "{Series CleanTitleWithoutYear} ({Series Year}) [tvdbid-{TvdbId}]"
	}
}
