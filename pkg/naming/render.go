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
		fn, ok := tokenFuncs[normalizeTokenName(base)]
		if !ok {
			errOut = fmt.Errorf("%w: %q", ErrUnknownToken, base)
			return raw
		}
		val := fn(c, pad, trunc)
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

// tokenFuncs is grown by every later step; this step seeds it with the
// tokens exercised so far.
var tokenFuncs = map[string]func(c Context, pad, trunc int) string{
	"movie title":             func(c Context, _, _ int) string { return c.Title },
	"release year":            func(c Context, _, _ int) string { return yearString(c.Year) },
	"release group":           func(c Context, _, _ int) string { return c.ReleaseGroup },
	"tmdbid":                  func(c Context, _, _ int) string { return c.TmdbID },
	"mediainfo audiocodec":    func(c Context, _, _ int) string { return firstAudioCodec(c.MediaInfo) },
	"mediainfo audiochannels": func(c Context, _, _ int) string { return firstAudioChannels(c.MediaInfo) },
	"season":                  func(c Context, pad, _ int) string { return padInt(c.Season, pad) },
	"episode":                 func(c Context, pad, _ int) string { return padInt(firstOr(c.Episodes), pad) },
	"absolute":                func(c Context, pad, _ int) string { return padInt(firstOr(c.Absolute), pad) },
	"episode cleantitle":      func(c Context, _, trunc int) string { return truncate(cleanTitle(c.EpisodeTitle), trunc) },
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
