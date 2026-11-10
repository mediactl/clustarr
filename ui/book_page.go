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
	"regexp"
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// A book's page (2026-11-10 design §9.3; plan W4b.2) is the movie page for
// one book, filled from what the cluster already holds: the Book, its
// author's library card (or its own, standalone) and its MediaFile. The
// hero carries the cover, title and subtitle, the author, the reading
// order, the release date, pages, publisher and provider logos, the facts
// (status with the catalog's cutoff reason, quality, profile, path,
// monitored, ISBN, languages); then the editions, the Files table and the
// neighbouring books in series order. Nothing here fetches from a provider,
// and nothing here writes.

// handleBookXPage serves GET /library/{namespace}/book/{name}, the page a
// book's title opens (and preloads): an author's book's, or a standalone
// book's, which is the page its card opens.
func (s *Server) handleBookXPage(w http.ResponseWriter, r *http.Request) {
	s.renderBookXPage(w, r, true)
}

// renderBookXPage answers the book's page, preloadable when the route is
// one a link preloads; not found when the book, its author's card (or its
// own) or the reader is missing -- unless Add New just created a standalone
// Book the library view does not list yet, which is told to wait.
func (s *Server) renderBookXPage(w http.ResponseWriter, r *http.Request, preload bool) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	v, ok := s.bookXPage(r.Context(), ns, name)
	if !ok {
		if s.standaloneBookJustAdded(r.Context(), ns, name) {
			s.renderAddPending(w, r, name)
			return
		}
		http.NotFound(w, r)
		return
	}
	if preload {
		preloadable(w)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.BookXPage(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render book page", "error", err)
	}
}

// standaloneBookJustAdded reports a Book without an authorRef that is in
// the cluster but not yet in the library view: Add New's standalone Book,
// told to wait rather than 404. An author's book the view does not hold
// (its author's card missing) is still not found, as before.
func (s *Server) standaloneBookJustAdded(ctx context.Context, ns, name string) bool {
	var b catalogv1.Book
	return s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b) && b.Spec.AuthorRef == nil
}

// bookXPage builds a book's page. The book's card is its author's card
// with the book's own cover, title, year, monitoring and profile (the
// book's own, else the author's), or the standalone book's own card from
// the projection.
func (s *Server) bookXPage(ctx context.Context, ns, name string) (views.BookXView, bool) {
	var b catalogv1.Book
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b) {
		return views.BookXView{}, false
	}
	row := bookRow(&b, time.Now())
	library := s.opts.Library(ctx)
	v := views.BookXView{Row: row, Standalone: row.Author == "", CutoffReason: b.Status.CutoffReason}
	var parent projection.LibraryItem
	var ok bool
	if v.Standalone {
		parent, ok = findLibraryItem(library, ns, string(commonv1.MediaKindBook), b.Name)
	} else {
		parent, ok = findLibraryItem(library, ns, string(commonv1.MediaKindAuthor), row.Author)
	}
	if !ok {
		return views.BookXView{}, false
	}
	// A standalone book has a folder of its own (bookHasFolder), so its
	// bar renders Refresh & Scan, Rename and Edit (design §9.3); an
	// author's book takes its author's folder, profile and root, and its
	// bar renders Refresh alone and no Edit dialog.
	v.Detail = views.Detail{Item: bookXItem(&b, row, parent, v.Standalone), Path: b.Status.Path, Links: bookXLinks(&b), CanScan: bookHasFolder(&b)}
	if !v.Standalone {
		v.AuthorTitle, v.AuthorURL = parent.Title, views.DetailPath(parent)
	}
	if md := b.Status.Metadata; md != nil {
		v.Overview, v.Genres = md.Overview, md.Genres
		v.Pages, v.Publisher = md.PageCount, md.Publisher
		for _, l := range md.Languages {
			v.Languages = append(v.Languages, languageName(l))
		}
		v.ISBN = bookXISBN(&b)
		v.Editions = bookXEditions(&b)
	}
	v.Quality = b.Status.FileFormat
	if q := b.Status.FileQuality; q != nil && q.Name != "" {
		v.Quality = q.Name
	}
	if b.Status.HasFile && b.Status.FileRef != nil && *b.Status.FileRef != "" {
		var mf catalogv1.MediaFile
		if s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: *b.Status.FileRef}, &mf) {
			v.Files, _ = fileRows(b.Status.Path, &mf)
			for i := range v.Files {
				// The file is the book's whatever its mediaRef says, so
				// the Files table draws the book columns.
				if v.Files[i].Kind == "" {
					v.Files[i].Kind = commonv1.MediaKindBook
				}
			}
		}
	}
	if v.Standalone {
		v.Prev, v.Next = neighbours(library, v.Item)
	} else {
		v.Prev, v.Next, v.NeighbourOrder = s.bookXNeighbours(ctx, &b)
	}
	return v, true
}

// bookXItem is the book's library card as its page reads it: a standalone
// book's own card from the projection, else one built from the Book over
// its author's -- the author's tab, root folder and quality profile unless
// the book names its own -- with the book's cover from status.artwork
// (through /art, ADR-0011), its first publication year, its monitoring,
// phase and file.
func bookXItem(b *catalogv1.Book, row views.BookRow, parent projection.LibraryItem, standalone bool) projection.LibraryItem {
	item := parent
	if !standalone {
		item = projection.LibraryItem{
			Ref: types.NamespacedName{Namespace: b.Namespace, Name: b.Name}, Kind: commonv1.MediaKindBook,
			Title: row.DisplayTitle(), Tab: parent.Tab, RootFolder: parent.RootFolder,
			QualityProfileRef: parent.QualityProfileRef,
		}
		for _, a := range b.Status.Artwork {
			if a.Type == catalogv1.ImageTypePoster {
				item.Poster = projection.ArtURL(commonv1.MediaKindBook, b.GetUID(), catalogv1.ImageTypePoster, a.Digest)
			}
		}
		item.ArtKey = projection.ArtKey(commonv1.MediaKindBook, b.GetUID(), catalogv1.ImageTypePoster)
		if md := b.Status.Metadata; md != nil && md.ReleaseDate != nil {
			item.Year = int32(md.ReleaseDate.UTC().Year()) //nolint:gosec // a year
		}
	}
	if ref := b.Spec.QualityProfileRef; ref != nil && *ref != "" {
		item.QualityProfileRef = *ref
	}
	if ref := b.Spec.RootFolderRef; ref != nil && *ref != "" {
		item.RootFolder = *ref
	}
	// The page reads the live Book, not the card a projection tick ago.
	item.Monitored, item.Phase, item.HasFile = row.Monitored, row.Phase, row.HasFile
	return item
}

// bookXLinks are the book's provider pages with a logo: its Open Library
// work (spec.workID), then the Hardcover, Goodreads and Wikipedia pages the
// metadata's links name, and a Goodreads page built from the goodreads
// external id when the links name none. A Hardcover page is the metadata's
// links entry of type hardcover, which the Hardcover client records from
// the row's slug (or the gateway's PageProvider ask, for a work Open
// Library answered, by its Hardcover id or its first edition's ISBN-13);
// the numeric Hardcover id alone makes no link, since Hardcover's site
// addresses a book by slug.
func bookXLinks(b *catalogv1.Book) []views.Link {
	var links []views.Link
	if b.Spec.WorkID != "" {
		links = append(links, views.Link{Label: "Open Library", Href: "https://openlibrary.org/works/" + b.Spec.WorkID, Provider: views.ProviderOpenLibrary})
	}
	md := b.Status.Metadata
	if md == nil {
		return links
	}
	links = append(links, providerLinksOf(md.Links)...)
	if id := md.ExternalIDs["goodreads"]; id != "" && !hasProvider(links, views.ProviderGoodreads) {
		links = append(links, views.Link{Label: "Goodreads", Href: "https://www.goodreads.com/book/show/" + id, Provider: views.ProviderGoodreads})
	}
	return links
}

// hasProvider reports whether links already carry provider's page.
func hasProvider(links []views.Link, provider string) bool {
	for _, l := range links {
		if l.Provider == provider {
			return true
		}
	}
	return false
}

// bookXISBN is the ISBN the page shows: the work's isbn13 external id, else
// the pinned edition's, else the first edition's that has one.
func bookXISBN(b *catalogv1.Book) string {
	md := b.Status.Metadata
	if id := md.ExternalIDs["isbn13"]; id != "" {
		return id
	}
	if pinned := bookXPinned(b); pinned != "" {
		for _, e := range md.Editions {
			if e.ID == pinned && e.ISBN13 != "" {
				return e.ISBN13
			}
		}
	}
	for _, e := range md.Editions {
		if e.ISBN13 != "" {
			return e.ISBN13
		}
	}
	return ""
}

// bookXPinned is the edition spec.editionID pins, "" for none.
func bookXPinned(b *catalogv1.Book) string {
	if b.Spec.EditionID == nil {
		return ""
	}
	return *b.Spec.EditionID
}

// bookXEditions are the work's editions as the Editions table lists them:
// the pinned one first and marked, then the rest in the metadata's order;
// each with its monitoring override from spec.editions (true without one).
func bookXEditions(b *catalogv1.Book) []views.BookXEdition {
	md := b.Status.Metadata
	if md == nil || len(md.Editions) == 0 {
		return nil
	}
	pinned := bookXPinned(b)
	monitored := map[string]bool{}
	for _, e := range b.Spec.Editions {
		monitored[e.ID] = monitoredOrDefault(e.Monitored)
	}
	rows := make([]views.BookXEdition, 0, len(md.Editions))
	for _, e := range md.Editions {
		row := views.BookXEdition{
			ID: e.ID, Title: e.Title, Format: e.Format, Language: languageName(e.Language), Publisher: e.Publisher,
			ISBN13: e.ISBN13, Pages: e.PageCount, Ebook: e.IsEbook, Pinned: e.ID == pinned, Monitored: true,
		}
		if m, ok := monitored[e.ID]; ok {
			row.Monitored = m
		}
		if e.ReleaseDate != nil {
			row.Release = e.ReleaseDate.UTC().Format(time.DateOnly)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Pinned && !rows[j].Pinned })
	return rows
}

// bookXNeighbours are the pages of the books either side of b among its
// author's, in series order when b is in a reading order two or more of
// the author's books share (its primary series, by position), else by
// release date as the books table lists them. order says which ("series"
// or "release"), for the tests.
func (s *Server) bookXNeighbours(ctx context.Context, b *catalogv1.Book) (prev, next, order string) {
	if b.Spec.AuthorRef == nil {
		return "", "", ""
	}
	books, err := s.bookXList(ctx, b.Namespace, *b.Spec.AuthorRef)
	if err != nil {
		logging.FromContext(ctx).Error("list books for a book's neighbours", "error", err)
		return "", "", ""
	}
	now := time.Now()
	series, _ := bookXPrimary(b)
	var ordered []bookXOrdered
	if series != "" {
		for i := range books {
			if s, pos := bookXPrimary(&books[i]); s == series {
				ordered = append(ordered, bookXOrdered{bookRow(&books[i], now), pos})
			}
		}
	}
	if len(ordered) >= 2 {
		order = "series"
		sort.SliceStable(ordered, func(i, j int) bool { return bookXBefore(ordered[i], ordered[j]) })
	} else {
		order = "release"
		ordered = ordered[:0]
		for i := range books {
			ordered = append(ordered, bookXOrdered{row: bookRow(&books[i], now)})
		}
		sort.SliceStable(ordered, func(i, j int) bool { return bookXReleasedBefore(ordered[i].row, ordered[j].row) })
	}
	for i := range ordered {
		if ordered[i].row.Name != b.Name {
			continue
		}
		if i > 0 {
			prev = ordered[i-1].row.URL()
		}
		if i+1 < len(ordered) {
			next = ordered[i+1].row.URL()
		}
		break
	}
	return prev, next, order
}

// bookXOrdered is a book's row with its position in the reading order the
// neighbours are taken from.
type bookXOrdered struct {
	row      views.BookRow
	position string
}

// bookXReleasedBefore is the books table's order: an undated book first,
// then oldest first, then by title.
func bookXReleasedBefore(a, b views.BookRow) bool {
	if a.ReleaseDate != b.ReleaseDate {
		return a.ReleaseDate < b.ReleaseDate
	}
	return a.DisplayTitle() < b.DisplayTitle()
}

// bookXPrimary is the reading order a book is chiefly part of and its
// position in it: the primary series link, else the first; "" for none.
func bookXPrimary(b *catalogv1.Book) (series, position string) {
	md := b.Status.Metadata
	if md == nil || len(md.SeriesLinks) == 0 {
		return "", ""
	}
	link := md.SeriesLinks[0]
	for _, l := range md.SeriesLinks {
		if l.Primary {
			link = l
			break
		}
	}
	return link.Series, link.Position
}

// bookXBefore orders two books of one reading order: by position (as
// numbers when both read as one -- "1", "2.5", "10" -- else as text), then
// by release date, then by title.
func bookXBefore(a, b bookXOrdered) bool {
	na, oka := bookXNumber(a.position)
	nb, okb := bookXNumber(b.position)
	switch {
	case oka && okb && na != nb:
		return na < nb
	case (!oka || !okb) && a.position != b.position:
		return a.position < b.position
	default:
		return bookXReleasedBefore(a.row, b.row)
	}
}

// bookXNumberRE is the leading number of a position, "2.5" in "2.5" and
// "1" in "1-3".
var bookXNumberRE = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?`)

// bookXNumber reads a position as a number, false when it starts with
// none.
func bookXNumber(pos string) (float64, bool) {
	m := bookXNumberRE.FindString(pos)
	if m == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(m, 64)
	return n, err == nil
}
