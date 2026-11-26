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

package v1alpha1

import "slices"

// MaxFormerProbeHashes bounds status.formerProbeHashes: the last renames or
// moves a file's history is carried across. It mirrors the field's
// +kubebuilder:validation:MaxItems.
const MaxFormerProbeHashes = 4

// SameBytes reports whether hash names the bytes s describes: its probe
// hash, or one those same bytes had at a path the file was renamed or moved
// from (status.formerProbeHashes). The probe hash hashes the path, so a
// path-only change moves it; what a stage, a record or a cache keyed by the
// old hash holds is still about this file.
func (s *MediaFileStatus) SameBytes(hash string) bool {
	if s == nil || hash == "" {
		return false
	}
	return hash == s.ProbeHash || slices.Contains(s.FormerProbeHashes, hash)
}

// WithFormerProbeHash is former after a path-only change from the hash was
// to the hash is: was is appended, is is dropped (a file renamed back to a
// name it had), and the list keeps its newest MaxFormerProbeHashes.
func WithFormerProbeHash(former []string, was, is string) []string {
	out := make([]string, 0, len(former)+1)
	for _, h := range former {
		if h != "" && h != is && h != was {
			out = append(out, h)
		}
	}
	if was != "" && was != is {
		out = append(out, was)
	}
	if len(out) > MaxFormerProbeHashes {
		out = out[len(out)-MaxFormerProbeHashes:]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CarryBytes re-points the blocks of s that are keyed by hash and at rest
// -- the transcode and graft verdicts, the subtitle history, the markers and
// the segment analysis -- to s's probe hash, when the hash a block records
// is one these same bytes had at an earlier path (SameBytes). It reports
// whether it changed anything.
//
// A block is what a stage knows about some bytes; the probe hash it records
// says which. A rename or a move changes the hash and not the bytes, and
// read as a new file it costs every language's search history, a fresh
// fetch and analysis of the markers, and a verdict the bytes had earned
// (owner, 2026-11-26: "carry the history across a path-only move"; the
// anime library design, 2026-11-25, §8a). A block with work in flight is
// left as it is: its task names the old path and is the stage's to
// withdraw or let fail, as before.
func (s *MediaFileStatus) CarryBytes() bool {
	if s == nil {
		return false
	}
	return s.CarryBytesTo(s.ProbeHash, s.FormerProbeHashes)
}

// CarryBytesTo is CarryBytes for a probe hash and former hashes s does not
// hold yet: the file loop carries the blocks of the status it read in the
// pass whose probe planner took the path-only change, so they are stored
// with the new hash in one write.
func (s *MediaFileStatus) CarryBytesTo(hash string, formerHashes []string) bool {
	if s == nil || hash == "" || len(formerHashes) == 0 {
		return false
	}
	former := func(h string) bool { return h != hash && slices.Contains(formerHashes, h) }
	changed := false
	if t := s.Transcode; t != nil && former(t.ProbeHash) {
		switch t.Phase {
		case TranscodePhaseQueued, TranscodePhaseRunning, TranscodePhaseSwapping:
		default:
			t.ProbeHash, changed = hash, true
		}
	}
	if g := s.Graft; g != nil && former(g.ProbeHash) {
		switch g.Phase {
		case GraftPhaseQueued, GraftPhaseRunning, GraftPhaseSwapping:
		default:
			g.ProbeHash, changed = hash, true
		}
	}
	if b := s.Subtitles; b != nil && former(b.ProbeHash) {
		inFlight := slices.ContainsFunc(b.Items, func(it SubtitleItemStatus) bool { return it.Dispatch.InFlight() })
		if !inFlight {
			b.ProbeHash, changed = hash, true
		}
	}
	if m := s.Markers; m != nil {
		if former(m.ForProbeHash) {
			m.ForProbeHash, changed = hash, true
		}
		if a := m.Analysis; a != nil && former(a.ForProbeHash) {
			a.ForProbeHash, changed = hash, true
		}
	}
	return changed
}
