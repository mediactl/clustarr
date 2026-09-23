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

package rollup

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// ReasonTranscoded is the CutoffMet condition's reason, and the phase, of an
// item whose file is transcoded: the condition is True, because a transcoded
// file is final and so counts as meeting any cutoff.
const ReasonTranscoded = "Transcoded"

// Transcoded reports whether mf is a transcoded file. This is the one place
// the rule lives; the Movie and Episode phases, the cutoff, the search
// worker's and the RSS matcher's decision input all read it from here.
//
// The owner's rule (CLAUDE.md, "Transcoding"): a transcoded media file is the
// final destination. Its item reads Transcoded, never CutoffUnmet; it counts
// as meeting the cutoff; the wanted sweep never selects it; and the decision
// engine rejects every automatic upgrade of it (decision.ReasonTranscodedFinal),
// leaving only a user's interactive grab.
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
// A nil mf is not transcoded. Only video kinds are ever transcoded (squasharr
// selects movie and episode MediaFiles), so for every other kind this is
// false in practice; it does not test the kind, because a tag on a file is
// evidence whatever kind it backs.
func Transcoded(mf *catalogv1alpha1.MediaFile) bool {
	if mf == nil {
		return false
	}
	if mf.Spec.Original != nil && !*mf.Spec.Original {
		return true
	}
	return mf.Status.MediaInfo != nil && mf.Status.MediaInfo.TranscodeProfile != ""
}

// TranscodedObject is [Transcoded] over a client.Object, for a watch
// predicate (k8s.StatusFieldChanged) on MediaFile: false for any other type.
// The Movie and Episode controllers wake on its change, because the probe
// sets status.mediaInfo.transcodeProfile in a status write that bumps no
// generation, and a GenerationChanged-only watch would leave a rescanned,
// already-transcoded file's item at CutoffUnmet until something else woke it.
func TranscodedObject(o client.Object) bool {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	return ok && Transcoded(mf)
}
