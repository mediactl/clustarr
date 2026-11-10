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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// An author's page (2026-11-10 design §9.3; plan W4b.2) is the series
// page's shape with the author's books as the one table: the hero with the
// author's photo, name, born and died dates, genres and provider logos, the
// Status fact read from the container rollup, the action bar (Refresh &
// Scan, Search, Rename, Edit, Delete -- no interactive search of its own)
// and the books table inline, each title opening its Book page. The books
// are listed through the reader's spec.authorRef index (ui/reader.go),
// never a namespace List filtered in memory. Nothing here writes.

// handleAuthorXPage serves GET /library/{namespace}/author/{name}, the
// author's page (a literal route, so it wins over the generic item route).
// An author Add New just created is in the cluster before the library
// view's next rebuild, and is told to wait rather than 404.
func (s *Server) handleAuthorXPage(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	item, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(commonv1.MediaKindAuthor), name)
	if !ok {
		s.notFoundUnlessJustAdded(w, r, ns, string(commonv1.MediaKindAuthor), name)
		return
	}
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AuthorXPage(s.authorXPage(r.Context(), item)).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render author page", "error", err)
	}
}

// authorXPage builds the author's page: the item page's details (the
// photo, path, overview, genres, neighbours and the Edit dialog's profiles,
// itemDetail's), the Author's own dates, links and counts, and its books.
func (s *Server) authorXPage(ctx context.Context, item projection.LibraryItem) views.AuthorXView {
	v := views.AuthorXView{Detail: s.itemDetail(ctx, item)}
	var a catalogv1.Author
	if s.getObject(ctx, item.Ref, &a) {
		v.Links = authorXLinks(&a)
		v.BookCount, v.BookFileCount = a.Status.BookCount, a.Status.BookFileCount
		if md := a.Status.Metadata; md != nil {
			v.Born, v.Died = md.Born, md.Died
		}
	}
	books, err := s.bookXRows(ctx, item.Ref.Namespace, item.Ref.Name)
	if err != nil {
		logging.FromContext(ctx).Error("list an author's books", "author", item.Ref.String(), "error", err)
	}
	v.Books = books
	return v
}

// authorXLinks are the author's provider pages, each with a logo the ui
// holds (views.Providers): Open Library from spec.openLibraryID, then the
// Goodreads, Wikipedia and Hardcover pages the metadata's links name. A
// Hardcover page is linked only when the metadata carries its URL:
// spec.hardcoverID is Hardcover's numeric id, and its site addresses an
// author by slug, which the gateway does not record.
func authorXLinks(a *catalogv1.Author) []views.Link {
	var links []views.Link
	if a.Spec.OpenLibraryID != "" {
		links = append(links, views.Link{Label: "Open Library", Href: "https://openlibrary.org/authors/" + a.Spec.OpenLibraryID, Provider: views.ProviderOpenLibrary})
	}
	if md := a.Status.Metadata; md != nil {
		links = append(links, providerLinksOf(md.Links)...)
	}
	return links
}

// providerLinksOf turns the metadata's links into the hero's logos, for the
// providers the ui has a logo for and in a fixed order: Hardcover,
// Goodreads, Wikipedia. The rest (Wikidata, VIAF, LibraryThing, an
// author's homepage) have no logo and are not shown.
func providerLinksOf(links []catalogv1.Link) []views.Link {
	var out []views.Link
	for _, want := range []struct{ typ, label, provider string }{
		{"hardcover", "Hardcover", views.ProviderHardcover},
		{"goodreads", "Goodreads", views.ProviderGoodreads},
		{"wikipedia", "Wikipedia", views.ProviderWikipedia},
	} {
		for _, l := range links {
			if l.Type == want.typ && l.URL != "" {
				out = append(out, views.Link{Label: want.label, Href: l.URL, Provider: want.provider})
				break
			}
		}
	}
	return out
}

// bookXRows lists an author's Books through the reader's spec.authorRef
// index (ui.BookAuthorRefField) and orders them as the books table does:
// an undated book first, then oldest first, then by title.
func (s *Server) bookXRows(ctx context.Context, namespace, author string) ([]views.BookRow, error) {
	books, err := s.bookXList(ctx, namespace, author)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rows := make([]views.BookRow, 0, len(books))
	for i := range books {
		rows = append(rows, bookRow(&books[i], now))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ReleaseDate != rows[j].ReleaseDate {
			return rows[i].ReleaseDate < rows[j].ReleaseDate
		}
		return rows[i].DisplayTitle() < rows[j].DisplayTitle()
	})
	return rows, nil
}

// bookXList is an author's Books as the reader holds them, by the
// spec.authorRef index; nil with no reader.
func (s *Server) bookXList(ctx context.Context, namespace, author string) ([]catalogv1.Book, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var list catalogv1.BookList
	if err := s.opts.Reader.List(ctx, &list, client.InNamespace(namespace),
		client.MatchingFields{BookAuthorRefField: author}); err != nil {
		return nil, err
	}
	return list.Items, nil
}
