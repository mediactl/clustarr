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

// Transcoded reports whether mf is a transcoded file. This is the one place
// the rule lives, here beside the type so every service reads the same
// predicate without importing another's: catalogarr through
// catalogarr/controller/rollup.Transcoded (the Movie and Episode phases, the
// cutoff, the search worker's and the RSS matcher's decision input), and
// importarr's completed-download import, which refuses to let an automatic
// grab replace a transcoded file (spec §8.4).
//
// The owner's rule (CLAUDE.md, "Transcoding"): a transcoded media file is the
// final destination. Its item reads Transcoded, never CutoffUnmet; it counts
// as meeting the cutoff; the wanted sweep never selects it; the decision
// engine rejects every automatic upgrade of it; and the import never lets an
// automatic grab overwrite it -- leaving only a user's interactive grab or
// manual import.
//
// A file is transcoded when either holds:
//
//   - spec.original is false: catalogarr incorporated a transcode swap and
//     took the file over (spec §8.5). It stays false for good, so a re-mux
//     that later strips the tag does not make the file upgradeable again.
//   - status.mediaInfo.transcodeProfile is set: the file carries squasharr's
//     CLUSTARR_PROFILE container tag, read by the probe. This is what
//     recognises a library file an earlier install transcoded, found by a
//     rescan, whose MediaFile starts life with spec.original true.
//
// status.transcode.profileTag deliberately does not count. A
// replaceSource=false transcode records it on the SOURCE's MediaFile (so
// squasharr does not plan the same derived copy again) while this MediaFile's
// own bytes stay the untouched original, which is still upgradeable.
//
// A nil mf is not transcoded, and spec.original unset reads as its
// +kubebuilder:default, true. Only video kinds are ever transcoded (squasharr
// selects movie and episode MediaFiles), so for every other kind this is
// false in practice; it does not test the kind, because a tag on a file is
// evidence whatever kind it backs. It is a plain Go method and generates
// nothing.
func (mf *MediaFile) Transcoded() bool {
	if mf == nil {
		return false
	}
	if mf.Spec.Original != nil && !*mf.Spec.Original {
		return true
	}
	return mf.Status.MediaInfo != nil && mf.Status.MediaInfo.TranscodeProfile != ""
}
