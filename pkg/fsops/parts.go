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

// TranscodePart is a transcode dispatch's in-progress output,
// <Stem>.part-<FileUID8>-<Seq><Ext>, as app/transcode/worker's
// uniquePartPath names it beside the output path <Stem><Ext> (loop spec
// §4.12: keyed by the MediaFile and the dispatch's sequence).
type TranscodePart struct {
	// Stem is the output path without its extension, directory included.
	Stem string
	// FileUID8 is the first eight characters of the MediaFile's UID.
	FileUID8 string
	// Seq is the dispatch sequence that wrote it.
	Seq int64
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
	// ".part-<uid8>-<seq>": the regexp fixed both fields' shapes.
	uid8 := infix[len(".part-") : len(".part-")+8]
	seq, err := strconv.ParseInt(infix[len(".part-")+9:], 10, 64)
	if err != nil {
		return TranscodePart{}, false
	}
	return TranscodePart{Stem: strings.TrimSuffix(withPart, infix), FileUID8: uid8, Seq: seq, Ext: ext}, true
}

// ParseTranscodePartCopy is [ParseTranscodePart] for a dead attempt's
// sweeps: it also reads a copy of a part still in flight -- the part's name
// with ".partial" after it, the temporary file an atomic copy lands it
// through (copyFile; MoveAtomic nested a second one before 2026-10-16) --
// as that part. A pod killed mid copy-back leaves one beside the output,
// and it is the dead attempt's leftover as much as the part is (Beetlejuice
// Beetlejuice, 2026-10-16: two, 8.7 GB, that no sweep recognised).
func ParseTranscodePartCopy(path string) (TranscodePart, bool) {
	return ParseTranscodePart(trimPartial(path))
}

// trimPartial is path without its trailing ".partial" suffixes.
func trimPartial(path string) string {
	for strings.HasSuffix(path, partialSuffix) {
		path = strings.TrimSuffix(path, partialSuffix)
	}
	return path
}

// partialSuffix is what an atomic copy or write appends to its target's
// name while it is in flight (copyFile, AtomicWrite).
const partialSuffix = ".partial"
