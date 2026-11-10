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
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// An author's page (2026-11-10 design §9.3) is the series page's shape
// with the books inline: a hero with the author's dates, genres, provider
// logos and the Status fact read from the container rollup, the action bar
// without an interactive search, and the books table whose titles open the
// Book page, preloaded.
func TestAuthorPageIsTheSeriesPageWithItsBooksInline(t *testing.T) {
	srv, _ := bookFixture(t)
	rec := getPath(t, srv, "/library/default/author/le-guin", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"), "a book row's title preloads its page; the author's card preloads this one")
	page := rec.Body.String()
	require.Contains(t, page, "<html")

	hero := requireTag(t, page, `data-author-page="le-guin"`, `data-hero`, `data-kind="author"`, `data-monitored="true"`, `data-profile="ebook"`)
	require.Contains(t, hero, `data-ref="default/le-guin"`)
	require.Contains(t, page, ">Ursula K. Le Guin</h1>")
	requireTag(t, page, `data-born="1929-10-21"`)
	require.Contains(t, page, ">Born 1929-10-21<")
	requireTag(t, page, `data-died="2018-01-22"`)
	require.Contains(t, page, ">Died 2018-01-22<")
	require.Contains(t, page, "Science fiction, Fantasy")
	require.Contains(t, page, "Ursula Kroeber Le Guin was an American author.")
	require.Contains(t, page, "/data/books/Ursula K. Le Guin<")

	// The logos: Open Library from the spec's id, Goodreads and Wikipedia
	// from the metadata's links; Wikidata has no logo and Hardcover no URL.
	requireTag(t, page, `data-provider-link="openlibrary"`, `href="https://openlibrary.org/authors/OL1A"`)
	requireTag(t, page, `data-provider-link="goodreads"`, `href="https://www.goodreads.com/author/show/874602"`)
	requireTag(t, page, `data-provider-link="wikipedia"`, `href="https://en.wikipedia.org/wiki/Ursula_K._Le_Guin"`)
	require.NotContains(t, page, "wikidata.org")
	require.NotContains(t, page, `data-provider-link="hardcover"`)

	// The facts: the Status from the rollup (nothing missing or in flight
	// on the card, one file: downloaded), the profile, the book counts.
	requireTag(t, page, `data-fact="status"`)
	require.Contains(t, page, `data-status="downloaded"`)
	requireTag(t, page, `data-book-count="1 / 9"`)
	require.Contains(t, page, ">1 / 9<")

	// The action bar: Refresh & Scan, Search Monitored Books, Rename, Edit
	// and Delete; an author has no interactive search of its own.
	requireTag(t, page, `data-action="refresh-metadata"`, `title="Refresh &amp; Scan"`)
	requireTag(t, page, `data-action="search-now"`, `title="Search Monitored Books"`)
	require.Contains(t, page, `data-action="rename-dry-run"`)
	require.Contains(t, page, `data-action="rename-apply"`)
	require.Contains(t, page, `data-edit-dialog="le-guin"`)
	require.Contains(t, page, `data-action="delete-confirm"`)
	require.NotContains(t, page, `author-interactive-search`)

	// The books, inline and in the table's order, each title the book's
	// page; the modal region and the action status once.
	require.Equal(t, 1, strings.Count(page, `data-book-table`), "the books are one table, inline")
	requireTag(t, page, `data-author-books="9"`)
	for _, name := range []string{"dispossessed", "wizard-of-earthsea", "tombs-of-atuan", "firelight", "lathe-of-heaven", "left-hand", "lavinia", "forthcoming", "poems"} {
		requireTag(t, page, `data-book="`+name+`"`, `data-slot="table-row"`)
	}
	require.NotContains(t, page, "Neuromancer", "another author's book is never listed")
	require.NotContains(t, page, `data-book="standalone"`, "a standalone book is no author's")
	require.Less(t, indexOf(page, `data-book="poems"`), indexOf(page, `data-book="wizard-of-earthsea"`), "undated first")
	require.Less(t, indexOf(page, `data-book="tombs-of-atuan"`), indexOf(page, `data-book="lathe-of-heaven"`), "then oldest first")
	requireTag(t, page, `href="/library/default/book/dispossessed"`, `preload="mouseover"`, `data-book-title`)
	require.NotContains(t, page, `hx-trigger="collapsible-open-change[detail.open] once"`, "nothing loads lazily")
	require.Equal(t, 1, strings.Count(page, `id="book-modal"`))
	require.Equal(t, 1, strings.Count(page, `id="action-status"`))

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/author/nope", false).Code)
}

// With no cluster the author's page still renders from the card alone:
// no books, no dates, no counts.
func TestAuthorPageRendersFromTheCardWithoutAReader(t *testing.T) {
	items := []projection.LibraryItem{{
		Ref: bookXCardRef("default", "le-guin"), Kind: "author", Tab: projection.TabBooks, Title: "Ursula K. Le Guin", Monitored: true,
	}}
	srv := ui.NewServer(t.Context(), ui.Options{Library: func(context.Context) []projection.LibraryItem { return items }})
	rec := getPath(t, srv, "/library/default/author/le-guin", false)
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	requireTag(t, page, `data-author-page="le-guin"`)
	require.NotContains(t, page, `data-born=`)
	require.NotContains(t, page, `data-fact="books"`)
	requireTag(t, page, `data-author-books="0"`)
	require.Contains(t, page, "Nothing here yet.")
}
