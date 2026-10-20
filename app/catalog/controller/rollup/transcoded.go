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

// ReasonMissingAudio is the CutoffMet condition's reason for an item whose
// file lacks a language a non-grafting audio policy wants
// (decision.IncompleteAudio): False, transcoded or not, until a release
// carrying every wanted language replaces the file.
const ReasonMissingAudio = "MissingAudio"

// Transcoded reports whether mf is a transcoded file: catalog's name for
// catalogv1alpha1.(*MediaFile).Transcoded, where the rule itself lives, beside
// the type, so import's completed-download import reads the same predicate
// without importing catalog. The Movie and Episode phases, the cutoff, the
// search worker's and the RSS matcher's decision input all read it through
// here; that method's doc comment is the rule (CLAUDE.md, "Transcoding"):
// spec.original false, or the probe's status.mediaInfo.transcodeProfile, and
// never status.transcode.profileTag. A nil mf is not transcoded.
func Transcoded(mf *catalogv1alpha1.MediaFile) bool {
	return mf.Transcoded()
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
