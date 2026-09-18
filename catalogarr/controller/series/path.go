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

package series

import (
	"path"

	"github.com/mediactl/clustarr/pkg/naming"
)

// Path resolves the folder a Series is stored under: folderOverride when it
// is set and non-empty (spec.folder), otherwise the naming engine's own
// dialect preset. path.Join is POSIX-only, which is fine -- Clustarr runs
// in-cluster on Linux only, so rootPath is always a POSIX path.
func Path(rootPath string, folderOverride *string, engine naming.Engine, ctx naming.Context) (string, error) {
	if folderOverride != nil && *folderOverride != "" {
		return path.Join(rootPath, *folderOverride), nil
	}
	folder, err := engine.SeriesFolder(ctx)
	if err != nil {
		return "", err
	}
	return path.Join(rootPath, folder), nil
}
