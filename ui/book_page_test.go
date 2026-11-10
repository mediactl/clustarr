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

package ui_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

// bookXCardRef is a library card's reference.
func bookXCardRef(ns, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: ns, Name: name}
}

// A book's page (2026-11-10 design §9.3) is the movie page for one book:
// the hero with the cover, title and subtitle, the author link and reading
// order, the release date, pages, publisher and logos, the facts, the
// action bar, the editions, the Files table in the book columns and the
// neighbouring books.
func TestBookPageIsTheMoviePageForABook(t *testing.T) {
	srv, _ := bookFixture(t)
	rec := getPath(t, srv, "/library/default/book/dispossessed", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"), "a row's title preloads the page")
	page := rec.Body.String()
	require.Contains(t, page, "<html")
	require.Contains(t, page, "<title>Ursula K. Le Guin - The Dispossessed: An Ambiguous Utopia")

	requireTag(t, page, `data-book-page="dispossessed"`, `data-hero`, `data-kind="book"`, `data-standalone="false"`,
		`data-monitored="true"`, `data-hasfile="true"`, `data-status="file"`, `data-phase="CutoffUnmet"`,
		`data-profile="ebook"`, `data-neighbours="release"`)
	require.Contains(t, page, ">The Dispossessed</h1>")
	requireTag(t, page, `data-subtitle`)
	require.Contains(t, page, ">An Ambiguous Utopia</p>")
	requireTag(t, page, `data-author-link`, `href="/library/default/author/le-guin"`)
	require.Contains(t, page, ">Ursula K. Le Guin</a>")
	require.Contains(t, page, `<time datetime="1974-05-01" title="1974-05-01" data-released>Released May 1 1974</time>`)
	requireTag(t, page, `data-pages="341"`)
	require.Contains(t, page, ">341 pages<")
	requireTag(t, page, `data-publisher`)
	require.Contains(t, page, ">HarperCollins</span>")
	require.Contains(t, page, "Shevek, a physicist, travels from Anarres to Urras.")

	// The logos: the Open Library work from the spec, Goodreads built from
	// the external id; no Hardcover without a recorded page.
	requireTag(t, page, `data-provider-link="openlibrary"`, `href="https://openlibrary.org/works/OL-dispossessed"`)
	requireTag(t, page, `data-provider-link="goodreads"`, `href="https://www.goodreads.com/book/show/13651"`)
	require.NotContains(t, page, `data-provider-link="hardcover"`)

	// The facts: status from the cell with the catalog's reason, the
	// quality (amber below the cutoff), the author's profile the book
	// inherits, the path, monitored, size, ISBN, languages as names, genres.
	require.Contains(t, page, `data-status="cutoff-unmet"`)
	require.Contains(t, page, "Cutoff Unmet")
	require.Regexp(t, regexp.MustCompile(`data-fact="quality".*?bg-amber-500/20[^>]*>EPUB<`), bookXOneLine(page))
	requireTag(t, page, `data-fact="profile"`)
	require.Contains(t, page, ">ebook<")
	require.Contains(t, page, "/data/books/Ursula K. Le Guin/The Dispossessed (1974)<")
	requireTag(t, page, `data-fact="monitored"`)
	require.Contains(t, page, ">Monitored</dd>")
	require.Contains(t, page, "2.00 MiB")
	requireTag(t, page, `data-fact="isbn"`)
	require.Contains(t, page, ">9780060125639<")
	requireTag(t, page, `data-fact="languages"`)
	require.Contains(t, page, "English, German")
	require.Contains(t, page, "Science fiction")

	// The action bar: Refresh (no folder of the book's own), Search Book
	// and the interactive search into #book-modal; Rename needs a folder,
	// Edit and Delete a kind the dialogs take, so none is drawn.
	refresh := requireTag(t, page, `data-action="refresh-metadata"`, `form="refresh-dispossessed"`)
	require.NotContains(t, refresh, "Scan", "a book has no folder of its own to scan")
	require.NotContains(t, page, `name="scan" value="true"`)
	requireTag(t, page, `data-action="search-now"`, `title="Search Book"`)
	requireTag(t, page, `data-action="book-interactive-search"`, `form="interactive-search-dispossessed"`)
	requireTag(t, page, `id="interactive-search-dispossessed"`, `hx-post="/library/default/book/dispossessed/search/interactive"`, `hx-target="#book-modal"`)
	require.NotContains(t, page, `data-action="rename-apply"`)
	require.NotContains(t, page, `data-edit-dialog=`)
	require.NotContains(t, page, `data-action="delete-confirm"`)

	// The bookmark flips the book's monitoring and returns here.
	requireTag(t, page, `action="/library/default/book/dispossessed/monitor"`)
	require.Contains(t, page, `name="return" value="/library/default/book/dispossessed"`)

	// The editions: a closed collapsible, the pinned edition first and
	// marked, the unmonitored hardcover dimmed, each cell filled.
	requireTag(t, page, `data-section="editions"`)
	requireTag(t, page, `data-editions="2"`, `data-slot="collapsible"`, `data-closed`)
	require.Contains(t, page, "Show 2 editions")
	pinned := requireTag(t, page, `data-edition="OL24364628M"`, `data-edition-pinned="true"`)
	require.NotContains(t, pinned, "text-muted-foreground", "the pinned ebook is monitored")
	requireTag(t, page, `data-pinned="OL24364628M"`)
	hardcover := requireTag(t, page, `data-edition="OL7353617M"`, `data-edition-pinned="false"`, `text-muted-foreground`)
	require.NotEmpty(t, hardcover)
	require.Less(t, indexOf(page, `data-edition="OL24364628M"`), indexOf(page, `data-edition="OL7353617M"`), "the pinned edition first")
	requireTag(t, page, `href="https://openlibrary.org/books/OL7353617M"`)
	for _, cell := range []string{">Hardcover<", ">EPUB<", `data-ebook>ebook<`, ">German<", ">English<", ">Heyne<", ">341<", `<time datetime="1974-05-01">1974-05-01</time>`, "Planet der Habenichtse"} {
		require.Contains(t, page, cell)
	}

	// The Files table in the book columns: format, pages, size; never the
	// video columns.
	files := requireTag(t, page, `data-section="files"`)
	require.NotEmpty(t, files)
	requireTag(t, page, `data-file="dispossessed-file"`)
	requireTag(t, page, `data-cell="format"`)
	requireTag(t, page, `data-cell="pages"`)
	require.Contains(t, page, "The Dispossessed.epub")
	require.Contains(t, page, ">Pages</th>")
	require.NotContains(t, page, ">Video Codec</th>")
	require.NotContains(t, page, `data-section="extras"`, "a book has no sidecars to manage")

	// The neighbours: no reading order shared with another book, so the
	// author's books by date -- The Lathe of Heaven before, Lavinia after.
	requireTag(t, page, `data-nav="prev"`, `href="/library/default/book/lathe-of-heaven"`)
	requireTag(t, page, `data-nav="next"`, `href="/library/default/book/lavinia"`)

	require.Equal(t, 1, strings.Count(page, `id="book-modal"`))
	require.Equal(t, 1, strings.Count(page, `id="action-status"`))
}

// A book in a reading order two or more of its author's books share takes
// its neighbours from that order by position, not by date: Firelight
// (Earthsea 0.5, 2018) comes before A Wizard of Earthsea (1, 1968), The
// Tombs of Atuan (2, 1971) after it; The Tombs of Atuan ends the order.
func TestBookPageNeighboursFollowTheReadingOrder(t *testing.T) {
	srv, _ := bookFixture(t)
	for name, want := range map[string]struct{ prev, next string }{
		"wizard-of-earthsea": {"/library/default/book/firelight", "/library/default/book/tombs-of-atuan"},
		"firelight":          {"", "/library/default/book/wizard-of-earthsea"},
		"tombs-of-atuan":     {"/library/default/book/wizard-of-earthsea", ""},
	} {
		rec := getPath(t, srv, "/library/default/book/"+name, false)
		require.Equal(t, http.StatusOK, rec.Code, name)
		page := rec.Body.String()
		requireTag(t, page, `data-book-page="`+name+`"`, `data-neighbours="series"`)
		require.Contains(t, page, `data-book-series="Earthsea #`)
		if want.prev != "" {
			requireTag(t, page, `data-nav="prev"`, `href="`+want.prev+`"`)
		} else {
			require.NotContains(t, page, `data-nav="prev"`, name)
		}
		if want.next != "" {
			requireTag(t, page, `data-nav="next"`, `href="`+want.next+`"`)
		} else {
			require.NotContains(t, page, `data-nav="next"`, name)
		}
	}
}

// A standalone book (no authorRef) renders the same page from its own
// card: no author link, its own profile, the tab's neighbours, a trail
// without an author. A book whose author the library does not hold, or no
// book, is not found.
func TestStandaloneBookPageNamesNoAuthor(t *testing.T) {
	srv, _ := bookFixture(t)
	rec := getPath(t, srv, "/library/default/book/standalone", false)
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	require.Contains(t, page, "<title>A Standalone Book")
	requireTag(t, page, `data-book-page="standalone"`, `data-standalone="true"`, `data-profile="ebook-standalone"`, `data-neighbours=""`, `data-status="missing"`)
	require.Contains(t, page, ">A Standalone Book</h1>")
	require.NotContains(t, page, `data-author-link`)
	require.NotContains(t, page, `href="/library/default/author/le-guin">Ursula`, "no author in the trail or the hero")
	requireTag(t, page, `data-nav="prev"`, `href="/library/default/author/le-guin"`)
	require.NotContains(t, page, `data-nav="next"`, "the tab's neighbours: the author's card before it, nothing after")
	require.Contains(t, page, "Missing")
	requireTag(t, page, `data-fact="monitored"`)
	require.NotContains(t, page, `data-fact="quality"`, "no file, no quality")
	require.Contains(t, page, "No file on disk.")
	require.NotContains(t, page, `data-section="editions"`, "no editions known")
	requireTag(t, page, `data-action="book-interactive-search"`, `form="interactive-search-standalone"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/book/nope", false).Code)
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/book/neuromancer", false).Code,
		"a book whose author the library does not hold")
}

// bookXOneLine joins a page's lines, so a regexp can span tags.
func bookXOneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }
