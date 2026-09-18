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
	"regexp"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

var tokenRe = regexp.MustCompile(`\{([^{}]*)\}`)

func (e Engine) Render(tmpl string, c Context) (string, error) {
	var errOut error
	out := tokenRe.ReplaceAllStringFunc(tmpl, func(raw string) string {
		if errOut != nil {
			return raw
		}
		prefix, name, suffix := splitWrapper(raw[1 : len(raw)-1])
		base, pad, trunc := splitModifier(name)
		key := normalizeTokenName(base)

		var val string
		switch key {
		// episoderange (and Step 8's absoluterange) need e.Config, which a
		// package-level tokenFuncs closure cannot reach -- see these two
		// cases handled specially here instead of rebuilding the whole map
		// per Engine on every Render call.
		case "episoderange":
			val = formatEpisodeRange(c.Season, c.Episodes, e.Config.MultiEpisodeStyle)
		case "absoluterange":
			val = formatAbsoluteRange(c.Absolute, e.Config.MultiEpisodeStyle)
		default:
			entry, ok := tokenFuncs[key]
			if !ok {
				errOut = fmt.Errorf("%w: %q", ErrUnknownToken, base)
				return raw
			}
			val = entry.fn(c, pad, trunc)
			if entry.colonSensitive {
				val = replaceColon(val, e.Config.ColonReplacement)
			}
		}
		if val == "" {
			return ""
		}
		return prefix + val + suffix
	})
	if errOut != nil {
		return "", errOut
	}
	return out, nil
}

func splitWrapper(spec string) (prefix, name, suffix string) {
	name = spec
	if len(name) > 0 {
		switch name[0] {
		case '-', '[', '(', ' ':
			prefix, name = string(name[0]), name[1:]
		}
	}
	if len(name) > 0 {
		switch name[len(name)-1] {
		case ']', ')':
			suffix, name = string(name[len(name)-1]), name[:len(name)-1]
		}
	}
	return prefix, name, suffix
}

func splitModifier(name string) (base string, pad, trunc int) {
	i := lastColon(name)
	if i < 0 {
		return name, 0, 0
	}
	mod := name[i+1:]
	if mod != "" && allZero(mod) {
		return name[:i], len(mod), 0
	}
	if n, err := strconv.Atoi(mod); err == nil && n > 0 {
		return name[:i], 0, n
	}
	return name, 0, 0
}

func lastColon(s string) int {
	return strings.LastIndex(s, ":")
}

func allZero(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return true
}

func yearString(y int) string {
	if y == 0 {
		return ""
	}
	return strconv.Itoa(y)
}

func normalizeTokenName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// tokenEntry is one registered token: the function that renders it, and
// whether its output should be passed through the engine's configured
// ColonReplacement mode. Every *arr title-shaped token applies colon
// replacement, not just CleanTitle -- see the note's "Illegal characters
// and CleanTitle" section, which discusses colon replacement and
// CleanTitle together as the same normalisation pass.
type tokenEntry struct {
	fn             func(c Context, pad, trunc int) string
	colonSensitive bool
}

// tokenFuncs is grown by every later step; this step seeds it with the
// tokens exercised so far.
var tokenFuncs = map[string]tokenEntry{
	"movie title":                     {fn: func(c Context, _, _ int) string { return c.Title }, colonSensitive: true},
	"movie cleantitle":                {fn: func(c Context, _, _ int) string { return cleanTitle(c.Title) }, colonSensitive: true},
	"movie titlethe":                  {fn: func(c Context, _, _ int) string { return titleThe(c.Title) }, colonSensitive: true},
	"release year":                    {fn: func(c Context, _, _ int) string { return yearString(c.Year) }},
	"release group":                   {fn: func(c Context, _, _ int) string { return c.ReleaseGroup }},
	"tmdbid":                          {fn: func(c Context, _, _ int) string { return c.TmdbID }},
	"mediainfo audiocodec":            {fn: func(c Context, _, _ int) string { return firstAudioCodec(c.MediaInfo) }},
	"mediainfo audiochannels":         {fn: func(c Context, _, _ int) string { return firstAudioChannels(c.MediaInfo) }},
	"season":                          {fn: func(c Context, pad, _ int) string { return padInt(c.Season, pad) }},
	"episode":                         {fn: func(c Context, pad, _ int) string { return padInt(firstOr(c.Episodes), pad) }},
	"absolute":                        {fn: func(c Context, pad, _ int) string { return padInt(firstOr(c.Absolute), pad) }},
	"episode cleantitle":              {fn: func(c Context, _, trunc int) string { return truncate(cleanTitle(c.EpisodeTitle), trunc) }},
	"quality full":                    {fn: func(c Context, _, _ int) string { return qualityFull(c.Quality, c.Revision) }},
	"mediainfo videodynamicrangetype": {fn: func(c Context, _, _ int) string { return hdrDisplay[c.MediaInfo.Hdr] }},
	"edition tags":                    {fn: func(c Context, _, _ int) string { return c.Edition }},
	"custom formats":                  {fn: func(c Context, _, _ int) string { return strings.Join(c.CustomFormats, " ") }},
	"series cleantitlewithoutyear":    {fn: func(c Context, _, _ int) string { return cleanTitle(c.SeriesTitle) }, colonSensitive: true},
	"series year":                     {fn: func(c Context, _, _ int) string { return yearString(c.SeriesYear) }},
	"tvdbid":                          {fn: func(c Context, _, _ int) string { return c.TvdbID }},
	"air-date": {fn: func(c Context, _, _ int) string {
		if c.AirDate == nil {
			return ""
		}
		return c.AirDate.Format("2006-01-02")
	}},
	"artist name":         {fn: func(c Context, _, _ int) string { return c.ArtistName }},
	"album title":         {fn: func(c Context, _, _ int) string { return c.AlbumTitle }},
	"track title":         {fn: func(c Context, _, _ int) string { return c.TrackTitle }},
	"track":               {fn: func(c Context, pad, _ int) string { return padInt(c.Track, pad) }},
	"author name":         {fn: func(c Context, _, _ int) string { return c.AuthorName }},
	"book title":          {fn: func(c Context, _, _ int) string { return c.BookTitle }},
	"book series":         {fn: func(c Context, _, _ int) string { return c.BookSeries }},
	"book seriesposition": {fn: func(c Context, _, _ int) string { return c.BookSeriesPosition }},
	"narrator":            {fn: func(c Context, _, _ int) string { return c.Narrator }},
}

// overrideOr looks up key in the engine's Config.Overrides, returning
// fallback when the key is absent or maps to an empty string.
func (e Engine) overrideOr(key, fallback string) string {
	if v, ok := e.Config.Overrides[key]; ok && v != "" {
		return v
	}
	return fallback
}

func padInt(n, width int) string {
	if width > 0 {
		return fmt.Sprintf("%0*d", width, n)
	}
	return strconv.Itoa(n)
}

func firstOr(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	return xs[0]
}

// truncate is rune-safe: it never splits a multi-byte rune.
func truncate(s string, n int) string {
	runes := []rune(s)
	if n <= 0 || len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func firstAudioCodec(mi commonv1.MediaInfo) string {
	if len(mi.Audio) == 0 {
		return ""
	}
	return mi.Audio[0].Codec
}

// audioChannelLayout maps a raw channel count to the *arr channel-layout
// label. 6 physical channels is the well-known "5.1" layout (5 full-range +
// 1 low-frequency effects channel), not a bare "6.0".
var audioChannelLayout = map[int32]string{1: "1.0", 2: "2.0", 6: "5.1", 8: "7.1"}

func firstAudioChannels(mi commonv1.MediaInfo) string {
	if len(mi.Audio) == 0 {
		return ""
	}
	n := mi.Audio[0].Channels
	if label, ok := audioChannelLayout[n]; ok {
		return label
	}
	return fmt.Sprintf("%d.0", n)
}
