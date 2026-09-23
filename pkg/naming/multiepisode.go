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
	"regexp"
	"slices"
	"strings"
)

// The multi-episode expansion is a port of Sonarr's FileNameBuilder
// (src/NzbDrone.Core/Organizer/FileNameBuilder.cs, develop):
// AddSeasonEpisodeNumberingTokens, AddAbsoluteNumberingTokens,
// FormatNumberTokens and FormatRangeNumberTokens. Sonarr does not give
// {episode} a multi-episode value of its own. It finds the whole
// season-episode PATTERN in the template -- "S{season:00}E{episode:00}",
// "{season}x{episode:00}", ... -- and replaces it with that pattern repeated
// or ranged per MultiEpisodeStyle, so a file holding S01E01 and S01E02 is
// named "S01E01-E02" under the default style instead of "S01E01". Doing it
// on the pattern rather than in a preset is what makes a user's own
// override template, written in Sonarr's syntax, come out right too.

// seasonEpisodePattern is the seasonEpisode group of Sonarr's
// SeasonEpisodePatternRegex, `s?{season(?:\:0+)?}(?<episodeSeparator>[- ._]?[ex])(?<episode>{episode(?:\:0+)?})`
// (IgnoreCase). Sonarr's surrounding separator groups use lookaround, which
// RE2 lacks; patternSeparator finds them by scanning instead.
var seasonEpisodePattern = regexp.MustCompile(`(?i)s?\{season(?::0+)?\}([- ._]?[ex])(\{episode(?::0+)?\})`)

// absoluteEpisodePattern is the absolute group of Sonarr's
// AbsoluteEpisodePatternRegex.
var absoluteEpisodePattern = regexp.MustCompile(`(?i)\{absolute(?::0+)?\}`)

// numberToken matches one {season}, {episode} or {absolute} token with its
// optional zero-padding, as Sonarr's SeasonRegex, EpisodeRegex and
// AbsoluteEpisodeRegex do.
var numberToken = regexp.MustCompile(`(?i)\{(season|episode|absolute)(?::(0+))?\}`)

const patternSeparatorChars = "- ._"

// orDefault is the style a Config's zero value means: prefixedRange, which
// is both Sonarr's NamingConfig.Default (MultiEpisodeStyle.PrefixedRange)
// and RootFolder's CRD default (+kubebuilder:default=prefixedRange).
func (s MultiEpisodeStyle) orDefault() MultiEpisodeStyle {
	if s == "" {
		return MultiEpisodePrefixedRange
	}
	return s
}

// expandMultiEpisode rewrites every season-episode pattern and every
// absolute token in tmpl into its rendered multi-episode form, leaving the
// rest of the template for Render. A single episode renders exactly as the
// pattern would on its own. With no episodes (or no absolute numbers) the
// template is returned untouched, as it always has been.
//
// Every span and its separator is found on the ORIGINAL template and all are
// replaced in one pass. Sonarr swaps each season-episode pattern for a
// "{Season Episode1}" token before it expands absolute numbers, so an
// absolute token right after the pattern still sees a "}" before its
// separator; expanding the pattern into literal text first would lose that
// separator.
func (e Engine) expandMultiEpisode(tmpl string, c Context) string {
	style := e.Config.MultiEpisodeStyle.orDefault()
	var spans []expansion
	if len(c.Episodes) > 0 {
		spans = append(spans, findPatterns(tmpl, seasonEpisodePattern, func(pattern string, sub []string, sep string) string {
			episodeSep, episodeTok := sub[1], sub[2]
			eps := c.Episodes
			var format string
			switch style {
			case MultiEpisodeDuplicate:
				format = sep + pattern
			case MultiEpisodeRepeat:
				format = episodeSep + episodeTok
			case MultiEpisodeScene:
				format = "-" + episodeSep + episodeTok
			case MultiEpisodeRange:
				format, eps = "-"+episodeTok, firstAndLast(eps)
			case MultiEpisodePrefixedRange:
				format, eps = "-"+episodeSep+episodeTok, firstAndLast(eps)
			default: // MultiEpisodeExtend
				format = "-" + episodeTok
			}
			return formatNumbers(pattern, format, c.Season, eps, "episode")
		})...)
	}
	if len(c.Absolute) > 0 {
		spans = append(spans, findPatterns(tmpl, absoluteEpisodePattern, func(pattern string, _ []string, sep string) string {
			// AbsoluteEpisodeFormat.Separator defaults to "-" when the
			// template has none (GetAbsoluteFormat).
			if strings.TrimSpace(sep) == "" {
				sep = "-"
			}
			abs := c.Absolute
			var format string
			switch style {
			case MultiEpisodeDuplicate:
				format = sep + pattern
			case MultiEpisodeRepeat:
				repeatSep := strings.TrimSpace(sep)
				if repeatSep == "" {
					repeatSep = " "
				}
				format = repeatSep + pattern
			case MultiEpisodeRange, MultiEpisodePrefixedRange:
				format, abs = "-"+pattern, firstAndLast(abs)
			default: // MultiEpisodeScene, MultiEpisodeExtend
				format = "-" + pattern
			}
			return formatNumbers(pattern, format, c.Season, abs, "absolute")
		})...)
	}
	if len(spans) == 0 {
		return tmpl
	}
	slices.SortFunc(spans, func(a, b expansion) int { return a.start - b.start })
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(tmpl[last:sp.start])
		b.WriteString(sp.text)
		last = sp.end
	}
	b.WriteString(tmpl[last:])
	return b.String()
}

// expansion is one pattern's span in the template and what replaces it.
// The two pattern regexes cannot overlap: one needs {season} and
// {episode}, the other is {absolute} alone.
type expansion struct {
	start, end int
	text       string
}

// findPatterns expands every match of re in tmpl. expand gets the matched
// pattern, its submatches, and the separator the template puts around it:
// Sonarr's pattern regexes capture a "separator" group both before the
// pattern (a run of "- ._" right after a "}") and after it (a run right
// before a "{"), and .NET's Groups["separator"] is the last capture, so the
// trailing run wins and the leading one is the fallback.
func findPatterns(tmpl string, re *regexp.Regexp, expand func(pattern string, sub []string, sep string) string) []expansion {
	var out []expansion
	for _, m := range re.FindAllStringSubmatchIndex(tmpl, -1) {
		sub := make([]string, 0, len(m)/2)
		for i := 0; i < len(m); i += 2 {
			if m[i] < 0 {
				sub = append(sub, "")
				continue
			}
			sub = append(sub, tmpl[m[i]:m[i+1]])
		}
		out = append(out, expansion{start: m[0], end: m[1], text: expand(sub[0], sub, patternSeparator(tmpl, m[0], m[1]))})
	}
	return out
}

// patternSeparator is the separator Sonarr captures around the pattern at
// tmpl[start:end]: the run of separator characters after it when a "{"
// follows that run, else the run before it when a "}" precedes that run,
// else "".
func patternSeparator(tmpl string, start, end int) string {
	j := end
	for j < len(tmpl) && strings.IndexByte(patternSeparatorChars, tmpl[j]) >= 0 {
		j++
	}
	if j > end && j < len(tmpl) && tmpl[j] == '{' {
		return tmpl[end:j]
	}
	i := start
	for i > 0 && strings.IndexByte(patternSeparatorChars, tmpl[i-1]) >= 0 {
		i--
	}
	if i < start && i > 0 && tmpl[i-1] == '}' {
		return tmpl[i:start]
	}
	return ""
}

// formatNumbers is Sonarr's FormatNumberTokens: the first number renders
// through the pattern itself, each later one through format, then every
// {season} token takes the season. which names the token ("episode" or
// "absolute") the numbers fill.
func formatNumbers(pattern, format string, season int, numbers []int, which string) string {
	var b strings.Builder
	for i, n := range numbers {
		p := pattern
		if i > 0 {
			p = format
		}
		b.WriteString(fillNumber(p, which, n))
	}
	return fillNumber(b.String(), "season", season)
}

// fillNumber renders every {which} / {which:00} token in s as n, padded to
// the number of zeros given (Sonarr's ReplaceNumberToken).
func fillNumber(s, which string, n int) string {
	return numberToken.ReplaceAllStringFunc(s, func(tok string) string {
		m := numberToken.FindStringSubmatch(tok)
		if !strings.EqualFold(m[1], which) {
			return tok
		}
		return padInt(n, len(m[2]))
	})
}

func firstAndLast(xs []int) []int {
	if len(xs) <= 1 {
		return xs
	}
	return []int{xs[0], xs[len(xs)-1]}
}
