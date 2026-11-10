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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// standaloneFolderTemplate is the folder a standalone Book takes under its
// root: the title alone, the first segment of the two the import places
// its file at, "{Book Title}/{Book Title}.<ext>" (catalogctx.BookFilePath's
// standalone branch renders the same literal through the root's engine).
// It is no preset token of pkg/naming's, so a RootFolder's naming
// overrides cannot move it -- exactly as the import's rule.
const standaloneFolderTemplate = "{Book Title}"

// componentOptions sanitizes the rendered folder as catalogctx's joinUnder
// sanitizes one path component (its own componentOptions, unexported there):
// illegal characters replaced (a colon the book-title token leaves in
// place, since unlike a movie's it is not colon-sensitive, a dropped "?"),
// trailing dots and spaces trimmed, 255 bytes at most. TestPathAgreesWithTheImport
// holds the two copies together.
var componentOptions = naming.SanitizeOptions{
	ReplaceIllegal:    true,
	MaxComponentBytes: naming.DefaultMaxComponentBytes,
	MaxTotalBytes:     naming.DefaultMaxComponentBytes,
}

// Path resolves the folder a Book is stored under, which is the folder the
// import places its file in (catalogctx.BookFilePath's two rules, 2026-11-10
// non-video design §5.5, §9.3), so the folder Refresh & Scan, Rename and
// the library delete read off status.path is the one on disk:
//
//   - one of an Author's (author non-nil) lives directly under its
//     author's folder, "{Author Name}" from c.AuthorName (Readarr's
//     convention: no separate book folder, pkg/naming/book.go), "" while
//     the Author's name is not yet known, which path.Join collapses away,
//     so the path reads the root until the name arrives;
//   - a standalone one (author nil, as the import's nil author) has a
//     folder of its own under the root, "{Book Title}" rendered through
//     the root's naming engine and sanitized as the import sanitizes a
//     path component, and the root while the title is not yet known or
//     sanitizes to nothing.
//
// Until 2026-11-10 a standalone book rendered the author folder too, ""
// with no author, so its status.path was its root's own path while its
// file lay at <root>/{Book Title}/<file>; the ui read the folder off the
// file meanwhile (ui.itemFolder's belt, kept for a book written that way
// until its key's next pass writes this path).
func Path(rootPath string, author *catalogv1alpha1.Author, engine naming.Engine, c naming.Context) (string, error) {
	if author != nil {
		folder, err := engine.BuildFolder(commonv1.MediaKindBook, c)
		if err != nil {
			return "", err
		}
		return path.Join(rootPath, folder), nil
	}
	title, err := engine.Render(standaloneFolderTemplate, c)
	if err != nil {
		return "", err
	}
	return path.Join(rootPath, naming.SanitizePath(title, componentOptions)), nil
}
