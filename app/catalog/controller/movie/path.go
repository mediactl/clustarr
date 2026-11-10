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

package movie

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// Path resolves the folder a Movie is stored under, which is the folder the
// import places its file in (catalogctx.ItemFolder, the rule
// catalogctx.MovieFilePath joins the file onto; 2026-11-10 non-video design
// §5.5): folderOverride (spec.folder) under the root when it is set and
// non-empty, otherwise the root's dialect preset rendered from c -- every
// component sanitized as the import sanitizes it and the result held under
// the root, so a title with a "?", a trailing dot or 390 bytes names the
// folder on disk, not the title as written. Until 2026-11-10 this joined
// the rendered folder as written (path.Join), so "What If...?" read
// status.path "<root>/What If...? (2024) [tmdbid-...]" while the file lay
// under "What If... (2024) [tmdbid-...]", and Refresh & Scan, Rename and the
// library delete named a folder that was not there. A known title the rule
// cannot render (only dots, or nothing once sanitized) is an error the
// reconciler reports as catalogctx.ReasonFolderUnrenderable on Ready.
func Path(root *catalogv1alpha1.RootFolder, folderOverride *string, c naming.Context) (string, error) {
	return catalogctx.ItemFolder(root, commonv1.MediaKindMovie, folderOverride, c)
}
