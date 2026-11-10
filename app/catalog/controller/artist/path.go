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

package artist

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// Path resolves the folder an Artist is stored under, which is the folder
// the import places its albums' tracks under (catalogctx.ItemFolder, the
// rule catalogctx.TrackFilePath and catalogctx.AlbumFolder join the album
// segment and the track onto when the Artist's status.path is not yet
// written, and hold status.path to once it is; 2026-11-10 non-video design
// §5.5): folderOverride (spec.folder) as written under the root when it
// pins one, otherwise the artist preset, "{Artist Name}", rendered from c
// -- sanitized as the import sanitizes a component and held under the
// root, so an artist named "Therapy?" reads "<root>/Therapy", the folder
// on disk. An Artist whose name has not arrived renders no folder and
// reads the root until it has one. A known name the rule cannot render
// (only dots, or nothing once sanitized) is an error the reconciler
// reports as catalogctx.ReasonFolderUnrenderable on Ready. Until the F1
// fix round (2026-11-10) this joined the rendered folder as written
// (path.Join), the rule series.Path had until task F1, while
// TrackFilePath placed the tracks under the sanitized folder.
func Path(root *catalogv1alpha1.RootFolder, folderOverride *string, c naming.Context) (string, error) {
	return catalogctx.ItemFolder(root, commonv1.MediaKindArtist, folderOverride, c)
}
