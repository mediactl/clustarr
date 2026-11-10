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

package ui

import (
	"context"
	"net/http"
	"sort"

	"github.com/a-h/templ"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// The children fragment (spec 2026-09-23-library-page-design): an artist's
// albums as item rows, an author's books as the season table (book.go),
// monitored through the existing item action. Until the 2026-11-10 design
// (§9.2, §9.3) it was the lazily loaded body of an artist's and an
// author's page (renderParentPage, views.ParentDetail), which the artist
// page (ui/artist.go) and the author page (ui/author.go) replaced with
// their inline tables; the route alone stays, answering htmx the fragment
// and anyone else a page, since a bookmarked link still reaches it. Reads
// go through Options.Reader at request time; nothing here writes.

// childrenLabel names a parent kind's children, and is false for a kind
// that has no children fragment.
func childrenLabel(kind commonv1.MediaKind) (string, bool) {
	switch kind {
	case commonv1.MediaKindArtist:
		return "Albums", true
	case commonv1.MediaKindAuthor:
		return "Books", true
	default:
		return "", false
	}
}

// handleChildren serves GET /library/{namespace}/{kind}/{name}/children for
// an artist or an author: the children component to htmx, a page to anyone
// else. Any other kind, or a parent the projection does not list, is not
// found.
func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	ns, kind, name := r.PathValue("namespace"), commonv1.MediaKind(r.PathValue("kind")), r.PathValue("name")
	label, ok := childrenLabel(kind)
	if !ok {
		http.NotFound(w, r)
		return
	}
	item, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(kind), name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var children templ.Component
	var err error
	if kind == commonv1.MediaKindAuthor {
		var books []views.BookRow
		books, err = s.bookRows(r.Context(), item)
		children = views.BookRows(books)
	} else {
		var rows []views.ChildRow
		rows, err = s.childRows(r.Context(), item)
		children = views.ChildRows(rows)
	}
	if err != nil {
		logging.FromContext(r.Context()).Error("list children", "parent", item.Ref.String(), "error", err)
		http.Error(w, "could not list children", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isHTMX(r) {
		if err := children.Render(r.Context(), w); err != nil {
			logging.FromContext(r.Context()).Error("render children component", "error", err)
		}
		return
	}
	if err := views.ChildrenPage(s.itemDetail(r.Context(), item), label, children).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render children page", "error", err)
	}
}

// childRows lists an artist's Albums through the reader, by
// spec.artistRef, oldest first, then by title. An author's Books are
// bookRows.
func (s *Server) childRows(ctx context.Context, item projection.LibraryItem) ([]views.ChildRow, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var list catalogv1.AlbumList
	if err := s.opts.Reader.List(ctx, &list, client.InNamespace(item.Ref.Namespace)); err != nil {
		return nil, err
	}
	var rows []views.ChildRow
	for i := range list.Items {
		if list.Items[i].Spec.ArtistRef == item.Ref.Name {
			rows = append(rows, albumRow(&list.Items[i]))
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Year != rows[j].Year {
			return rows[i].Year < rows[j].Year
		}
		return rows[i].Title < rows[j].Title
	})
	return rows, nil
}

func albumRow(a *catalogv1.Album) views.ChildRow {
	row := views.ChildRow{
		Namespace: a.Namespace, Name: a.Name, Kind: string(commonv1.MediaKindAlbum), Title: a.Name,
		Monitored: monitoredOrDefault(a.Spec.Monitored), HasFile: a.Status.TrackFileCount > 0, Phase: string(a.Status.Phase),
	}
	if md := a.Status.Metadata; md != nil {
		row.Title = md.Title
		if md.ReleaseDate != nil {
			row.Year = int32(md.ReleaseDate.UTC().Year()) //nolint:gosec // a year
		}
	}
	if a.Status.FileQuality != nil {
		row.Quality = a.Status.FileQuality.Name
	}
	return row
}
