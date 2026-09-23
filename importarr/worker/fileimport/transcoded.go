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

package fileimport

import (
	"fmt"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// userChosen reports whether a Download's files are a person's choice rather
// than an automatic grab's: an interactive grab (spec.grabbedBy interactive,
// a release someone picked from a search's results) or a manual import
// (spec.manual, or the import-override annotation -- manual). Every other
// grab source -- rss, search, redownload, push, or none recorded -- is
// automatic.
func userChosen(dl *downloadv1alpha1.Download, manual bool) bool {
	return manual || dl.Spec.GrabbedBy == downloadv1alpha1.GrabSourceInteractive
}

// transcodedRejection is the rejection for a file that would replace
// existing when existing is transcoded and the Download is an automatic
// grab, or "" when the import may go ahead.
//
// A transcoded file is final (CLAUDE.md, "Transcoding"; the predicate is
// catalogv1alpha1.(*MediaFile).Transcoded, the one catalogarr reads): the
// decision engine already refuses every automatic grab against one
// (decision.ReasonTranscodedFinal), but a grab that was in flight when the
// transcode landed still completes, and without this gate its import would
// recycle the transcoded file and put the source-quality release back. The
// person's own choice is still honoured, as Radarr and Sonarr let a
// user-chosen release replace any file: an interactive grab or a manual
// import replaces a transcoded file like any other.
//
// It is a per-file rejection on status.import, never a silent skip, so the
// Download reads Blocked with the reason when it was the only file, and the
// reason names what lifts it.
func transcodedRejection(rel string, existing *catalogv1alpha1.MediaFile, dl *downloadv1alpha1.Download, manual bool) string {
	if !existing.Transcoded() || userChosen(dl, manual) {
		return ""
	}
	source := string(dl.Spec.GrabbedBy)
	if source == "" {
		source = "unrecorded"
	}
	return fmt.Sprintf("%s: %s %s's existing file (MediaFile %s) is transcoded, and a transcoded file is final: "+
		"an automatic grab (grabbedBy %s) never replaces it; only an interactive grab or a manual import "+
		"(spec.manual, or %s=true) does",
		rel, existing.Spec.MediaRef.Kind, existing.Spec.MediaRef.Name, existing.Name, source, AnnotationImportOverride)
}
