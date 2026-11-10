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
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/quality"
)

// FileState derives status.hasFile, fileRef, fileQuality, fileFormatScore,
// fileFormat and cutoffMet from the MediaFile currently backing a Book,
// ranked against profile: rollup.FileState, the same function Movie,
// Episode and Issue use, which calls profile.CutoffMet(mf.Spec.Quality);
// profile is nil when the owning QualityProfile could not be resolved, and
// cutoffMet is then conservatively false, never a guess.
//
// fileQuality and fileFormatScore are the file's frozen spec.quality and
// spec.formatScore (2026-11-10 design D5: one file vocabulary on every leaf,
// frozen at import as Issue's are), and fileFormat is the quality's name as
// the book ladder names it (pkg/quality/definition.go's
// nonVideoDefinitions["book"]: PDF, MOBI, EPUB, AZW3 -- the strings the
// import freezes into spec.quality.name). A file whose quality the import
// left unset has no format: until 2026-11-10 the path's extension stood in
// for it, from the days before the import froze a quality onto a book
// MediaFile, which it now does (app/import/importplan's nonVideoKind path),
// so the fallback invented nothing the import did not say and is gone.
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileQuality *commonv1.Quality, fileFormatScore int32, fileFormat string, cutoffMet bool) {
	hasFile, fileRef, fileQuality, fileFormatScore, cutoffMet = rollup.FileState(mf, profile)
	if !hasFile {
		return false, nil, nil, 0, "", false
	}
	if fileQuality != nil {
		fileFormat = fileQuality.Name
	}
	return hasFile, fileRef, fileQuality, fileFormatScore, fileFormat, cutoffMet
}
