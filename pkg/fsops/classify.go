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

package fsops

import (
	"path/filepath"
	"regexp"
	"strings"
)

// MediaExtensions is the set of lowercase, dotted extensions Walk treats
// as ClassMedia absent a Part/Extra/Sample signal. It is a var, not a
// const, so a caller can extend it: the set below is deliberately narrow,
// grounded only in what docs/research/naming.md verifies (the Jellyfin/
// Radarr/Sonarr standard-format examples all use .mkv, and the Jellyfin
// book/comic row lists the rest) rather than a guessed full container
// list.
var MediaExtensions = map[string]bool{
	".mkv":  true,
	".epub": true, ".mobi": true, ".azw": true, ".azw3": true, ".pdf": true,
	".cbz": true, ".cbr": true, ".cb7": true, ".cbt": true,
}

// IsPart reports whether path names an in-progress transfer's partial
// file: anacrolix/torrent's UsePartFiles convention (verified in
// docs/research/download.md §1.6) writes every incomplete file as
// <name>.part.
func IsPart(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".part")
}

var extraDirs = map[string]bool{
	"behind the scenes": true, "deleted scenes": true, "interviews": true,
	"scenes": true, "samples": true, "shorts": true, "featurettes": true,
	"clips": true, "extras": true, "trailers": true, "theme-music": true,
	"backdrops": true,
}

// IsExtra reports whether path lives under a directory Jellyfin/Plex/Emby
// treat as bonus content -- the verified list from docs/research/
// naming.md §A3's Jellyfin row: "behind the scenes", "deleted scenes",
// "interviews", "scenes", "samples", "shorts", "featurettes", "clips",
// "extras", "trailers", "theme-music", "backdrops". (A parallel
// filename-suffix convention, e.g. "-trailer", exists in Jellyfin/Kodi
// but is not verified in the research notes, so it is deliberately not
// implemented here rather than guessed.)
func IsExtra(path string) bool {
	for dir := filepath.Dir(path); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if extraDirs[strings.ToLower(filepath.Base(dir))] {
			return true
		}
		if filepath.Dir(dir) == dir {
			break // reached the filesystem root without a match
		}
	}
	return false
}

var sampleRE = regexp.MustCompile(`(?i)(^|[^a-zA-Z0-9])sample(s)?([^a-zA-Z0-9]|$)`)

const sampleMaxBytes = 50 * 1024 * 1024 // 50 MiB; see the Produces doc comment for why

// IsSample reports whether path is likely a promotional sample clip
// bundled in a release rather than the release itself. A filename
// signature (\bsample(s)?\b, case-insensitive) is decisive on its own.
// Absent that, a file with a MediaExtensions extension under 50 MiB is
// also flagged: no research note gives an authoritative size threshold
// (Radarr/Sonarr's real signal is a MediaInfo duration probe, which
// fsops deliberately does not depend on), so this is a stated, generous,
// dependency-free heuristic whose false positives are cheap: amendment
// §A1.5 requires the scanner to surface every file for review rather than
// act on a guess, and pkg/mediainfo.Probe downstream makes the real call.
func IsSample(path string, size int64) bool {
	base := filepath.Base(path)
	if sampleRE.MatchString(base) {
		return true
	}
	return MediaExtensions[strings.ToLower(filepath.Ext(base))] && size > 0 && size < sampleMaxBytes
}
