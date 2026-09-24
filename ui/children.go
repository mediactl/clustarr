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

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// An artist's and an author's pages are the series page's shape with albums
// and books as the children (spec 2026-09-23-library-page-design): one
// component that loads them lazily, monitored through the existing item
// action. Reads go through Options.Reader at request time, like the season
// route; nothing here writes.

// childrenLabel names a parent kind's children, and is false for a kind
// that has no children page.
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

// renderParentPage renders an artist's or an author's page for
// handleLibraryItem.
func (s *Server) renderParentPage(w http.ResponseWriter, r *http.Request, item projection.LibraryItem, label string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ParentDetail(s.itemDetail(r.Context(), item), label).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render parent page", "error", err)
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
	rows, err := s.childRows(r.Context(), item)
	if err != nil {
		logging.FromContext(r.Context()).Error("list children", "parent", item.Ref.String(), "error", err)
		http.Error(w, "could not list children", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isHTMX(r) {
		if err := views.ChildRows(rows).Render(r.Context(), w); err != nil {
			logging.FromContext(r.Context()).Error("render children component", "error", err)
		}
		return
	}
	if err := views.ChildrenPage(s.itemDetail(r.Context(), item), label, rows).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render children page", "error", err)
	}
}

// childRows lists a parent's children through the reader -- an artist's
// Albums by spec.artistRef, an author's Books by spec.authorRef -- oldest
// first, then by title.
func (s *Server) childRows(ctx context.Context, item projection.LibraryItem) ([]views.ChildRow, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var rows []views.ChildRow
	switch item.Kind {
	case commonv1.MediaKindArtist:
		var list catalogv1.AlbumList
		if err := s.opts.Reader.List(ctx, &list, client.InNamespace(item.Ref.Namespace)); err != nil {
			return nil, err
		}
		for i := range list.Items {
			if list.Items[i].Spec.ArtistRef == item.Ref.Name {
				rows = append(rows, albumRow(&list.Items[i]))
			}
		}
	case commonv1.MediaKindAuthor:
		var list catalogv1.BookList
		if err := s.opts.Reader.List(ctx, &list, client.InNamespace(item.Ref.Namespace)); err != nil {
			return nil, err
		}
		for i := range list.Items {
			if ref := list.Items[i].Spec.AuthorRef; ref != nil && *ref == item.Ref.Name {
				rows = append(rows, bookRow(&list.Items[i]))
			}
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
	if a.Status.Quality != nil {
		row.Quality = a.Status.Quality.Name
	}
	return row
}

func bookRow(b *catalogv1.Book) views.ChildRow {
	row := views.ChildRow{
		Namespace: b.Namespace, Name: b.Name, Kind: string(commonv1.MediaKindBook), Title: b.Name,
		Monitored: monitoredOrDefault(b.Spec.Monitored), HasFile: b.Status.HasFile,
		Quality: b.Status.FileFormat, Phase: string(b.Status.Phase),
	}
	if md := b.Status.Metadata; md != nil {
		row.Title = md.Title
		if md.ReleaseDate != nil {
			row.Year = int32(md.ReleaseDate.UTC().Year()) //nolint:gosec // a year
		}
	}
	return row
}

// replyChildRow answers an htmx album or book toggle with the row
// re-rendered, replyEpisodeRow's counterpart.
func (s *Server) replyChildRow(w http.ResponseWriter, r *http.Request, ns string, kind commonv1.MediaKind, name string, patched client.Object, err error) {
	var row views.ChildRow
	switch {
	case err == nil && kind == commonv1.MediaKindAlbum:
		if a, ok := patched.(*catalogv1.Album); ok {
			row = albumRow(a)
		}
	case err == nil && kind == commonv1.MediaKindBook:
		if b, ok := patched.(*catalogv1.Book); ok {
			row = bookRow(b)
		}
	}
	if row.Name == "" {
		row = s.currentChildRow(r.Context(), ns, kind, name)
	}
	if err != nil {
		row.Error = failureOf(r, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rerr := views.ChildRowView(row).Render(r.Context(), w); rerr != nil {
		logging.FromContext(r.Context()).Error("render child row", "error", rerr)
	}
}

// currentChildRow reads a child as it stands, for a failed toggle's reply;
// a bare row when it cannot be read.
func (s *Server) currentChildRow(ctx context.Context, ns string, kind commonv1.MediaKind, name string) views.ChildRow {
	key := types.NamespacedName{Namespace: ns, Name: name}
	if s.opts.Reader != nil {
		switch kind {
		case commonv1.MediaKindAlbum:
			var a catalogv1.Album
			if err := s.opts.Reader.Get(ctx, key, &a); err == nil {
				return albumRow(&a)
			}
		case commonv1.MediaKindBook:
			var b catalogv1.Book
			if err := s.opts.Reader.Get(ctx, key, &b); err == nil {
				return bookRow(&b)
			}
		}
	}
	return views.ChildRow{Namespace: ns, Name: name, Kind: string(kind), Title: name, Monitored: true}
}
