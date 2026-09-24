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
	"path/filepath"

	"github.com/mediactl/clustarr/pkg/naming"
)

// destinationPath resolves the absolute library path a source file is
// imported to: the RootFolder's own folder-resolution rule (mirroring
// app/catalog/controller/movie.Path, which computes the same folder for
// Movie.status.path) joined with the rendered file name and the source
// file's own extension, then run through naming.SanitizePath -- the one
// general-purpose sanitiser pkg/naming exports, which no per-kind renderer
// calls on its own (see pkg/naming's doc comment). This worker is the first
// thing to actually write these paths to a filesystem, so it is the one
// that must make them filesystem-safe.
func destinationPath(
	rootPath string, folderOverride *string, srcPath string, eng naming.Engine, nctx naming.Context,
) (string, error) {
	var folder string
	if folderOverride != nil && *folderOverride != "" {
		folder = *folderOverride
	} else {
		f, err := eng.MovieFolder(nctx)
		if err != nil {
			return "", fmt.Errorf("fileimport: render movie folder: %w", err)
		}
		folder = f
	}

	file, err := eng.MovieFile(nctx)
	if err != nil {
		return "", fmt.Errorf("fileimport: render movie file: %w", err)
	}
	file += filepath.Ext(srcPath)

	full := filepath.Join(rootPath, folder, file)
	return naming.SanitizePath(full, naming.DefaultSanitizeOptions()), nil
}
