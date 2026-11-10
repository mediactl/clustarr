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
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// Path resolves the folder a Book is stored under, which is the folder the
// import places its file in (catalogctx.BookFolder, the one rule
// catalogctx.BookFilePath joins the file onto; 2026-11-10 non-video design
// §5.5, §9.3), so the folder Refresh & Scan, Rename and the library delete
// read off status.path is the one on disk:
//
//   - one of an Author's (author non-nil) lives directly under its
//     author's folder -- the Author's spec.folder, else "{Author Name}"
//     from c.AuthorName (Readarr's convention: no separate book folder,
//     pkg/naming/book.go) -- the root until the Author's name arrives;
//   - a standalone one (author nil, as the import's nil author) has a
//     folder of its own under the root, "{Book Title}" rendered through
//     the root's naming engine, the root while the title is not yet known.
//
// Every component is sanitized as the import sanitizes it, through the
// same function: until 2026-11-10 (ba7c7256) this file carried a copy of
// catalogctx's component options for the standalone folder alone, and an
// author's book joined the author folder as written. A known title the
// rule cannot render (only dots, or nothing once sanitized) is an error the
// reconciler reports as catalogctx.ReasonFolderUnrenderable on Ready.
//
// Until 2026-11-10 a standalone book rendered the author folder too, ""
// with no author, so its status.path was its root's own path while its
// file lay at <root>/{Book Title}/<file>; the ui read the folder off the
// file meanwhile (ui.itemFolder's belt, kept for a book written that way
// until its key's next pass writes this path).
func Path(root *catalogv1alpha1.RootFolder, author *catalogv1alpha1.Author, c naming.Context) (string, error) {
	return catalogctx.BookFolder(root, author, c)
}
