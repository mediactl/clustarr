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

package overlay

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder image.Decode below needs
	"sync"
)

// logoFS embeds the seven rating-source marks. Ruling R6 (the owner's
// decision over the trademark concern, 2026-09-24): they ship embedded with
// pkg/overlay/logos/NOTICE naming each mark's owner, the same nominative use
// Kometa (https://github.com/Kometa-Team/Kometa) makes of them.
//
//go:embed logos/*.png
var logoFS embed.FS

// logoFiles maps a rating source to its embedded PNG's path within logoFS,
// named for the badge it draws (rt-critic.png, rt-audience.png) rather than
// the source string (rottenTomatoesCritic, rottenTomatoesAudience) --
// pkg/overlay/logos/NOTICE records where each file came from.
var logoFiles = map[string]string{
	SourceIMDb:       "logos/imdb.png",
	SourceTMDB:       "logos/tmdb.png",
	SourceMetacritic: "logos/metacritic.png",
	SourceRTCritic:   "logos/rt-critic.png",
	SourceRTAudience: "logos/rt-audience.png",
	SourceTrakt:      "logos/trakt.png",
	SourceLetterboxd: "logos/letterboxd.png",
}

var (
	logoOnce  sync.Once
	logoCache map[string]image.Image
)

// Logo returns the embedded mark for source, decoded once and cached for
// every later call, and reports whether one exists. A caller building a
// Badge for a source Logo does not recognize should leave Badge.Logo nil
// rather than treat the missing mark as an error -- the renderer role
// (spec §C.6) still draws the score text.
func Logo(source string) (image.Image, bool) {
	logoOnce.Do(loadLogos)
	img, ok := logoCache[source]
	return img, ok
}

// loadLogos decodes every embedded logo once. A decode failure here is a
// packaging defect (the PNGs are embedded at build time, not fetched at
// runtime), so it panics rather than surfacing as a per-call error every
// caller of Logo would have to handle for a condition that can only be
// fixed by rebuilding the binary.
func loadLogos() {
	logoCache = make(map[string]image.Image, len(logoFiles))
	for source, path := range logoFiles {
		data, err := logoFS.ReadFile(path)
		if err != nil {
			panic(fmt.Sprintf("pkg/overlay: embedded logo missing %s: %v", path, err))
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			panic(fmt.Sprintf("pkg/overlay: embedded logo %s did not decode: %v", path, err))
		}
		logoCache[source] = img
	}
}
