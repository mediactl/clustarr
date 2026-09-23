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

package book

import (
	"path"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/quality"
)

// FileState derives status.hasFile/fileRef/fileFormat/cutoffMet from the
// MediaFile currently backing a Book, ranked against profile -- corrected
// from this package's original design, which wrongly believed no book
// quality ladder existed (see this package's doc.go). It delegates the
// actual ranking to rollup.FileState (the same function Movie/Episode/
// Audiobook use), which calls profile.CutoffMet(mf.Spec.Quality); profile
// is nil when the owning QualityProfile could not be resolved, and
// cutoffMet is then conservatively false, never a guess.
//
// BookStatus has no FileQuality/FileFormatScore fields (unlike Movie/
// Episode/Audiobook) -- only FileFormat (a string: "EPUB", "AZW3", ...).
// fileFormat is read from rollup.FileState's returned commonv1.Quality.Name
// when a MediaFile's spec.quality has been set (pkg/quality's book ladder
// -- pkg/quality/definition.go's nonVideoDefinitions["book"] -- names its
// tiers exactly "PDF"/"MOBI"/"EPUB"/"AZW3", the same strings a real import
// would freeze into spec.quality.name), falling back to the file's own path
// extension when spec.quality is still its zero value -- which it always is
// today, since the rescan/import worker that would freeze quality onto a
// book MediaFile at import (task G2-4, non-video root attribution) has not
// landed yet. The fallback keeps FileFormat populated and useful in that
// gap without inventing a quality verdict: it is purely a fact read off the
// file, and cutoffMet is computed from spec.quality (or its absence) alone,
// never from the fallback.
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileFormat string, cutoffMet bool) {
	hasFile, fileRef, q, _, cutoffMet := rollup.FileState(mf, profile)
	if !hasFile {
		return false, nil, "", false
	}
	fileFormat = ""
	if q != nil {
		fileFormat = q.Name
	}
	if fileFormat == "" && mf != nil {
		fileFormat = formatFromPath(mf.Spec.Path)
	}
	return hasFile, fileRef, fileFormat, cutoffMet
}

// formatFromPath returns the uppercased file extension without its leading
// dot ("EPUB" for "...books/hobbit.epub"), or "" when the path has none.
func formatFromPath(p string) string {
	ext := path.Ext(p)
	if ext == "" {
		return ""
	}
	return strings.ToUpper(strings.TrimPrefix(ext, "."))
}
