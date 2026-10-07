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
	"fmt"
	"path/filepath"
	"strings"
)

// SidecarPath is the sidecar named suffix -- everything after the stem:
// "en.ass", "en.forced.srt", "ass" -- of the video at video. Beside a
// library file it is <stem>.<suffix>. Beside a transcode attempt's part,
// <stem>.part-<uid8>-<n>.<ext>, it is that attempt's part of the sidecar,
// <stem>.<suffix less its ext>.part-<uid8>-<n>.<suffix ext>, which
// ParseTranscodePart and IsPart read as a part (both sweeps and the
// rescan's orphan sweep remove it with its attempt) and
// subtitles.ParseSidecar reads as no sidecar of anything.
func SidecarPath(video, suffix string) string {
	// "ass" (no language) is all extension.
	name, ext := "", "."+suffix
	if i := strings.LastIndex(suffix, "."); i >= 0 {
		name, ext = suffix[:i], suffix[i:]
	}
	if p, ok := ParseTranscodePart(video); ok {
		stem := p.Stem
		if name != "" {
			stem += "." + name
		}
		return fmt.Sprintf("%s.part-%s-%d%s", stem, p.JobUID8, p.Attempt, ext)
	}
	return strings.TrimSuffix(video, filepath.Ext(video)) + "." + suffix
}

// SubtitleExt reports whether ext is a sidecar the transcode standard
// writes: ".ass" or ".srt".
func SubtitleExt(ext string) bool { return ext == ".ass" || ext == ".srt" }
