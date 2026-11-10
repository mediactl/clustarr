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
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// Path resolves the folder a Comic is stored under, which is the folder
// the import places its issues' files in (catalogctx.ItemFolder, the rule
// catalogctx.IssueFilePath joins the issue's file onto when the Comic's
// status.path is not yet written, and holds status.path to once it is;
// 2026-11-10 non-video design §5.5): folderOverride (spec.folder) under
// the root when it is set and non-empty, otherwise the comic preset,
// "{Comic Series Title}" (pkg/naming/preset.go's BuildFolder dispatches
// MediaKindComic and MediaKindIssue to it), rendered from c -- sanitized as
// the import sanitizes a component and held under the root, so "What
// If...?" reads "<root>/What If", the folder on disk. A known title the
// rule cannot render (only dots, or nothing once sanitized) is an error the
// reconciler reports as catalogctx.ReasonFolderUnrenderable on Ready.
func Path(root *catalogv1alpha1.RootFolder, folderOverride *string, c naming.Context) (string, error) {
	return catalogctx.ItemFolder(root, commonv1.MediaKindComic, folderOverride, c)
}
