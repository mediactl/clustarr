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
	"strconv"
	"strings"
)

// TranscodePart is a transcode attempt's in-progress output,
// <Stem>.part-<JobUID8>-<Attempt><Ext>, as app/squash/worker's
// uniquePartPath names it beside the output path <Stem><Ext>.
type TranscodePart struct {
	// Stem is the output path without its extension, directory included.
	Stem string
	// JobUID8 is the first eight characters of the TranscodeJob's UID.
	JobUID8 string
	// Attempt is the dispatch attempt that wrote it.
	Attempt int
	// Ext is the output container's extension, ".mkv" or ".mp4".
	Ext string
}

// ParseTranscodePart reads path as a [TranscodePart]: only the exact
// per-attempt form ([IsPart]'s partAttemptRE), never the anacrolix
// "<name>.part" a torrent is still downloading into, the generic
// "<stem>.part.<ext>", or a release whose name merely contains ".part-".
// It is what both sweeps of a dead attempt's output key on (the worker's at
// task start, the rescan's), so neither can ever read another kind of file
// as a transcode's leftover.
func ParseTranscodePart(path string) (TranscodePart, bool) {
	ext := filepath.Ext(path)
	if ext == "" {
		return TranscodePart{}, false
	}
	withPart := strings.TrimSuffix(path, ext)
	infix := filepath.Ext(withPart)
	if !partAttemptRE.MatchString(infix) {
		return TranscodePart{}, false
	}
	// ".part-<uid8>-<attempt>": the regexp fixed both fields' shapes.
	uid8 := infix[len(".part-") : len(".part-")+8]
	attempt, err := strconv.Atoi(infix[len(".part-")+9:])
	if err != nil {
		return TranscodePart{}, false
	}
	return TranscodePart{Stem: strings.TrimSuffix(withPart, infix), JobUID8: uid8, Attempt: attempt, Ext: ext}, true
}
