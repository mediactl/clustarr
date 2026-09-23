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

package comic

import (
	"path"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// Path resolves the folder a Comic is stored under: folderOverride when it
// is set and non-empty (spec.folder), otherwise the naming engine's own
// dialect preset. Unlike series.Path, which calls the kind-specific
// engine.SeriesFolder, this calls the generic engine.BuildFolder: Comic has
// no dedicated *Folder Engine method of its own (pkg/naming/preset.go's
// BuildFolder dispatches MediaKindComic and MediaKindIssue to the same
// "{Comic Series Title}" template, TokenComicFolder). path.Join is
// POSIX-only, which is fine -- Clustarr runs in-cluster on Linux only, so
// rootPath is always a POSIX path.
func Path(rootPath string, folderOverride *string, engine naming.Engine, ctx naming.Context) (string, error) {
	if folderOverride != nil && *folderOverride != "" {
		return path.Join(rootPath, *folderOverride), nil
	}
	folder, err := engine.BuildFolder(commonv1.MediaKindComic, ctx)
	if err != nil {
		return "", err
	}
	return path.Join(rootPath, folder), nil
}
