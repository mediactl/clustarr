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

package subtitles_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestSidecarName(t *testing.T) {
	tests := []struct {
		name      string
		videoPath string
		key       subtitles.LangKey
		hiExt     string
		want      string
	}{
		{"plain", "/data/movies/Inception (2010)/Inception.mkv", "en", "sdh", "/data/movies/Inception (2010)/Inception.en.srt"},
		{"forced", "/data/movies/Inception (2010)/Inception.mkv", "en:forced", "sdh", "/data/movies/Inception (2010)/Inception.en.forced.srt"},
		{"hi sdh", "/data/movies/Inception (2010)/Inception.mkv", "en:hi", "sdh", "/data/movies/Inception (2010)/Inception.en.sdh.srt"},
		{"hi hi", "/data/movies/Inception (2010)/Inception.mkv", "en:hi", "hi", "/data/movies/Inception (2010)/Inception.en.hi.srt"},
		{"hi cc", "/data/movies/Inception (2010)/Inception.mkv", "en:hi", "cc", "/data/movies/Inception (2010)/Inception.en.cc.srt"},
		{"hi bad enum falls back to sdh", "/data/movies/Inception (2010)/Inception.mkv", "en:hi", "bogus", "/data/movies/Inception (2010)/Inception.en.sdh.srt"},
		{"region subtag", "/data/movies/Filme.mkv", "pt-BR", "sdh", "/data/movies/Filme.pt-BR.srt"},
		{"stem with dots", "/data/movies/Show.S01E02.mkv", "en", "sdh", "/data/movies/Show.S01E02.en.srt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, subtitles.SidecarName(tt.videoPath, tt.key, tt.hiExt))
		})
	}
}

func TestParseSidecar(t *testing.T) {
	tests := []struct {
		name      string
		videoStem string
		filename  string
		wantKey   subtitles.LangKey
		wantOK    bool
	}{
		{"plain srt", "Movie", "Movie.en.srt", "en", true},
		{"forced", "Movie", "Movie.en.forced.srt", "en:forced", true},
		{"hi via hi infix", "Movie", "Movie.en.hi.srt", "en:hi", true},
		{"hi via cc infix", "Movie", "Movie.en.cc.srt", "en:hi", true},
		{"hi via sdh infix, eng and region case", "Movie", "Movie.eng.sdh.srt", "eng:hi", true},
		{"region subtag", "Movie", "Movie.pt-BR.srt", "pt-BR", true},
		{"region subtag forced", "Movie", "Movie.es-MX.forced.srt", "es-MX:forced", true},
		{"underscore region normalised to hyphen", "Movie", "Movie.pt_BR.srt", "pt-BR", true},
		{"ass extension", "Movie", "Movie.en.ass", "en", true},
		{"ssa extension", "Movie", "Movie.fr.ssa", "fr", true},
		{"vtt extension", "Movie", "Movie.fr.vtt", "fr", true},
		{"case-insensitive extension", "Movie", "Movie.en.SRT", "en", true},
		{"stem containing dots", "Show.S01E02", "Show.S01E02.en.srt", "en", true},

		// Right-to-left / never-guess rejections.
		{"tag not last does not parse as forced", "Movie", "Movie.forced.en.srt", "", false},
		{"bare stem, unknown language, never guessed", "Movie", "Movie.srt", "", false},
		{"tag with no language segment", "Movie", "Movie.forced.srt", "", false},
		{"wrong stem entirely", "Movie", "OtherMovie.en.srt", "", false},
		{"stem prefix only, not exact (strict match)", "Movie", "Movie.Extended.en.srt", "", false},
		{"exotic extension excluded by default", "Movie", "Movie.en.sub", "", false},
		{"no extension at all", "Movie", "Movie.en", "", false},
		{"empty name", "Movie", ".srt", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := subtitles.ParseSidecar(tt.videoStem, tt.filename)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantKey, key)
			}
		})
	}
}

// TestSidecarRoundTrip is the round-trip property the task calls for:
// ParseSidecar(SidecarName(x)) == x for every valid x, exercised over a
// combinatorial grid of languages (including region subtags and a
// multi-dot stem), forced/HI flags and every HIExtension enum value,
// rather than only the worked examples above.
func TestSidecarRoundTrip(t *testing.T) {
	videoPaths := []string{
		"/data/movies/Inception (2010)/Inception.mkv",
		"/data/tv/Show/Season 01/Show.S01E02.Title.mkv",
		"Relative.Path.mkv",
	}
	langs := []string{"en", "fr", "pt-BR", "zh-Hant", "es-MX"}
	hiExts := []string{"sdh", "hi", "cc"}

	for _, videoPath := range videoPaths {
		for _, lang := range langs {
			for _, forced := range []bool{false, true} {
				for _, hi := range []bool{false, true} {
					if forced && hi {
						continue // LangKey forbids both; not a valid x.
					}
					for _, hiExt := range hiExts {
						key := subtitles.FormatLangKey(lang, forced, hi)
						name := fmt.Sprintf("%s/forced=%v/hi=%v/hiExt=%s", videoPath, forced, hi, hiExt)
						t.Run(name, func(t *testing.T) {
							full := subtitles.SidecarName(videoPath, key, hiExt)

							videoStem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
							got, ok := subtitles.ParseSidecar(videoStem, filepath.Base(full))
							if assert.True(t, ok, "ParseSidecar must parse what SidecarName wrote") {
								assert.Equal(t, key, got, "ParseSidecar(SidecarName(x)) must equal x")
							}
						})
						if !hi {
							break // hiExt is irrelevant when hi is false; do not triple-count.
						}
					}
				}
			}
		}
	}
}
