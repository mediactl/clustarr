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

package views

import "fmt"

// BookRow is one book on an author's books table, the episode row's
// counterpart: the monitor bookmark, title, release date, status and the
// two searches.
type BookRow struct {
	Namespace string
	Name      string
	// Author is the author's name, for the author's books page a form
	// returns to; "" for a standalone book.
	Author   string
	Title    string
	Subtitle string
	// Series is the reading order the book is chiefly part of, "Earthsea
	// #1", or "".
	Series string
	// ReleaseDate is YYYY-MM-DD, or "" when unknown; ReleaseDateLabel is
	// how the table shows it (ui.relativeDate).
	ReleaseDate      string
	ReleaseDateLabel string
	Monitored        bool
	HasFile          bool
	// Format is the file's format (EPUB, AZW3), "" without a file.
	Format string
	Phase  string
	// Status is the status cell (ui.bookStatus).
	Status RowStatus
	// Error is set on a row rendered as the reply to a toggle that failed.
	Error ActionFailure
}

// DisplayTitle is the book's title, or its object name before its
// metadata has arrived.
func (r BookRow) DisplayTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

// URL is the book's base route.
func (r BookRow) URL() string { return fmt.Sprintf("/library/%s/book/%s", r.Namespace, r.Name) }

// MonitorURL is the book's monitor action, the existing per-item route.
func (r BookRow) MonitorURL() string { return r.URL() + "/monitor" }

// SearchURL is the book's automatic search, the per-item "search now".
func (r BookRow) SearchURL() string { return r.URL() + "/search" }

// InteractiveSearchURL starts an interactive search for the book.
func (r BookRow) InteractiveSearchURL() string { return r.URL() + "/search/interactive" }

// DetailsURL is the book's details: the modal to htmx, a page otherwise.
func (r BookRow) DetailsURL() string { return r.URL() + "/details" }

// ReturnURL is where a form post from the row returns: the author's books
// page, or a standalone book's own page.
func (r BookRow) ReturnURL() string {
	if r.Author == "" {
		return r.URL()
	}
	return ChildrenURL(r.Namespace, "author", r.Author)
}

// BookDetail is a book's details modal, the episode modal's counterpart:
// the book's summary and file on one tab, its searches on the other.
type BookDetail struct {
	Row BookRow
	// AuthorTitle is the author's name as the library shows it, "" for a
	// standalone book.
	AuthorTitle string
	Overview    string
	Genres      []string
	// File is the book's file, nil without one.
	File *FileRow
	// Tab is the open tab, "details" or "search".
	Tab string
	// Search is the interactive search shown on the search tab, nil until
	// one is run.
	Search *SearchPanel
}
