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
	"net/url"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// An author's books are the season table's shape (2026-10-07): each row
// has the monitor bookmark, the title opening the book's page (book_page.go,
// since 2026-11-10, design §9.3), the release date, the status cell and the
// automatic and interactive searches, the episode's own routes and
// actions for kind book. Interactive search and its grab are the
// episode's (startInteractiveSearch, the /searches routes); the book's
// modal (BookModal: Details and Search tabs) stays for the row's details
// and the interactive search's results.

// handleBookDetails serves GET /library/{namespace}/book/{name}/details:
// the book's modal to htmx, on the Details tab, or the Search tab with
// ?tab=search; the book's page to anyone else, as an episode's details do.
func (s *Server) handleBookDetails(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		s.renderBookXPage(w, r, false)
		return
	}
	d, ok := s.bookDetail(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("tab") == "search" {
		d.Tab = "search"
	}
	d.ReturnURL = bookModalReturnURL(r, d.Row)
	s.renderBookModal(w, r, d)
}

// bookModalReturnURL is where the modal's automatic search returns when it
// posts as a plain form: the book's own page when the modal was opened
// from it (the page's action bar opens it in #book-modal; htmx names the
// opener in HX-Current-URL, whose path is then the book's page or its
// details route, which answers the page to a browser without htmx), else
// the author's page the row's buttons return to (a standalone book's own
// page, BookRow.ReturnURL). Until 2026-11-10 every modal returned to the
// author's page, so a search from the book's page landed on the author's.
func bookModalReturnURL(r *http.Request, row views.BookRow) string {
	if current, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
		if p := current.Path; p == row.URL() || p == row.DetailsURL() {
			return row.URL()
		}
	}
	return row.ReturnURL()
}

// handleBookInteractiveSearch serves POST
// /library/{namespace}/book/{name}/search/interactive, the episode's
// interactive search for a book: htmx gets the book's modal on its Search
// tab, the results panel polling; a form post is sent to the panel's
// page. A book the cache does not hold is not found, and nothing is
// created for it.
func (s *Server) handleBookInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	d, ok := s.bookDetail(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	panel, ok := s.startInteractiveSearch(w, r, ns, commonv1.MediaKindBook, name)
	if !ok {
		return
	}
	d.Tab, d.Search, d.ReturnURL = "search", panel, bookModalReturnURL(r, d.Row)
	s.renderBookModal(w, r, d)
}

// renderBookModal answers the book's modal, into the page's #book-modal.
func (s *Server) renderBookModal(w http.ResponseWriter, r *http.Request, d views.BookDetail) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.BookModal(d).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render book details", "error", err)
	}
}

// bookDetail reads a book and builds the modal's view on its Details tab.
// False when the book, the library card it hangs from (its author's, or a
// standalone book's own) or the reader is missing.
func (s *Server) bookDetail(ctx context.Context, ns, name string) (views.BookDetail, bool) {
	var b catalogv1.Book
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b) {
		return views.BookDetail{}, false
	}
	row := bookRow(&b, time.Now())
	kind, parentName := commonv1.MediaKindBook, b.Name
	if row.Author != "" {
		kind, parentName = commonv1.MediaKindAuthor, row.Author
	}
	parent, ok := findLibraryItem(s.opts.Library(ctx), ns, string(kind), parentName)
	if !ok {
		return views.BookDetail{}, false
	}
	d := views.BookDetail{Row: row, Tab: "details"}
	if kind == commonv1.MediaKindAuthor {
		d.AuthorTitle = parent.Title
	}
	if md := b.Status.Metadata; md != nil {
		d.Overview, d.Genres = md.Overview, md.Genres
	}
	if b.Status.HasFile && b.Status.FileRef != nil {
		var mf catalogv1.MediaFile
		if s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: *b.Status.FileRef}, &mf) {
			rows, _ := fileRows(b.Status.Path, &mf)
			d.File = &rows[0]
		}
	}
	return d, true
}

// bookRow is one book's row in its author's books table.
func bookRow(b *catalogv1.Book, now time.Time) views.BookRow {
	row := views.BookRow{
		Namespace: b.Namespace, Name: b.Name,
		Monitored: monitoredOrDefault(b.Spec.Monitored), HasFile: b.Status.HasFile,
		Format: b.Status.FileFormat, Phase: string(b.Status.Phase),
	}
	if b.Spec.AuthorRef != nil {
		row.Author = *b.Spec.AuthorRef
	}
	if md := b.Status.Metadata; md != nil {
		row.Title, row.Subtitle, row.Series = md.Title, md.Subtitle, bookSeries(md.SeriesLinks)
		if md.ReleaseDate != nil {
			row.ReleaseDate = md.ReleaseDate.UTC().Format(time.DateOnly)
			row.ReleaseDateLabel = relativeDate(md.ReleaseDate.UTC(), now)
		}
	}
	row.Status = bookStatus(b, row.Monitored, now)
	return row
}

// bookStatus is the episode's status cell for a book, the first case that
// applies: a grab in flight, a grab waiting out its delay, the file's
// format (warned below the cutoff), not yet released, unmonitored, else
// missing. A book has no TBA: one without a release date is a work Open
// Library has not dated, not one still to come, so it reads missing.
func bookStatus(b *catalogv1.Book, monitored bool, now time.Time) views.RowStatus {
	st := b.Status
	switch {
	case st.Phase == catalogv1.BookPhaseDownloading || st.ActiveDownloadRef != nil:
		return views.RowStatus{Kind: "downloading", Label: "Downloading", Title: "Book is downloading"}
	case st.Phase == catalogv1.BookPhaseDelayed || st.PendingGrab != nil:
		return views.RowStatus{Kind: "pending", Label: "Pending", Title: "A release waits out its delay profile"}
	case st.HasFile:
		s := views.RowStatus{Kind: "file", Label: "Unknown", Title: "Book on disk"}
		if st.FileFormat != "" {
			s.Label = st.FileFormat
		}
		if st.Phase == catalogv1.BookPhaseCutoffUnmet {
			s.Warn, s.Title = true, "Quality cutoff has not been met"
		}
		return s
	case st.Metadata != nil && st.Metadata.ReleaseDate != nil && st.Metadata.ReleaseDate.After(now):
		return views.RowStatus{Kind: "unreleased", Label: "Unreleased", Title: "Book has not been released"}
	case !monitored:
		return views.RowStatus{Kind: "unmonitored", Label: "Unmonitored", Title: "Book is not monitored"}
	default:
		return views.RowStatus{Kind: "missing", Label: "Missing", Title: "Book missing from disk"}
	}
}

// bookSeries is the reading order a book is chiefly part of, as Readarr
// names it: "Earthsea #1", or the series alone without a position. The
// primary link wins, else the first.
func bookSeries(links []catalogv1.SeriesLink) string {
	if len(links) == 0 {
		return ""
	}
	link := links[0]
	for _, l := range links {
		if l.Primary {
			link = l
			break
		}
	}
	if link.Position == "" {
		return link.Series
	}
	return link.Series + " #" + link.Position
}

// replyBookRow answers an htmx book toggle with the row re-rendered,
// replyEpisodeRow's counterpart: from the Book the patch returned on
// success, or from the Book as it still stands with the failure inside it.
func (s *Server) replyBookRow(w http.ResponseWriter, r *http.Request, ns, name string, patched client.Object, err error) {
	var b *catalogv1.Book
	if got, ok := patched.(*catalogv1.Book); ok && err == nil {
		b = got
	} else {
		var current catalogv1.Book
		if s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &current) {
			b = &current
		}
	}
	if b == nil {
		b = &catalogv1.Book{}
		b.Namespace, b.Name = ns, name
	}
	row := bookRow(b, time.Now())
	if err != nil {
		row.Error = failureOf(r, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rerr := views.BookRowView(row).Render(r.Context(), w); rerr != nil {
		logging.FromContext(r.Context()).Error("render book row", "error", rerr)
	}
}
