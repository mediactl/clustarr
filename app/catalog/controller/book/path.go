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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// Path resolves the folder a Book is stored under. BookSpec, unlike
// MovieSpec/SeriesSpec/AuthorSpec, has no Folder override field at all
// (book_types.go -- there is nothing to override): per pkg/naming/book.go's
// own doc comment, "no separate book folder" is the Readarr convention this
// project follows -- a book's files live directly under its author's
// folder, so the folder this returns is exactly the author folder, "{Author
// Name}" rendered from authorName.
//
// For a standalone Book (no spec.authorRef), authorName is whatever the
// reconciler's resolveContext could determine -- "" when nothing is known,
// which naming.Engine.Render resolves to an empty token rather than an
// error (pkg/naming/render.go: an empty-valued token wrapper renders as "").
// path.Join then collapses that empty folder segment away, so a standalone
// Book with no known author name resolves to the RootFolder's own path
// directly, not a missing/error path -- a deliberate, graceful degradation
// rather than a guess at an author name that does not exist.
func Path(rootPath, authorName string, engine naming.Engine) (string, error) {
	folder, err := engine.BuildFolder(commonv1.MediaKindBook, naming.Context{AuthorName: authorName})
	if err != nil {
		return "", err
	}
	return path.Join(rootPath, folder), nil
}
