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

package author

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// Path resolves the folder an Author is stored under, which is the folder
// the import places its books' files in (catalogctx.ItemFolder, the rule
// catalogctx.BookFilePath joins a book's two segments onto when the
// Author's status.path is not yet written, and holds status.path to once it
// is; 2026-11-10 non-video design §5.5): folderOverride (spec.folder) as
// written under the root when it pins one (a folder on disk, never
// sanitized), otherwise the author preset,
// "{Author Name}" (Readarr's convention: no separate book folder,
// pkg/naming/book.go), rendered from c -- sanitized as the import sanitizes
// a component and held under the root. An Author whose name has not
// arrived renders no folder and reads the root until it has one. A known
// name the rule cannot render is an error the reconciler reports as
// catalogctx.ReasonFolderUnrenderable on Ready.
func Path(root *catalogv1alpha1.RootFolder, folderOverride *string, c naming.Context) (string, error) {
	return catalogctx.ItemFolder(root, commonv1.MediaKindAuthor, folderOverride, c)
}
