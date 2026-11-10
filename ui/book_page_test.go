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
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
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
	// Delete a kind the dialog takes and Edit a standalone book (design
	// §9.3: an author's book takes its author's profile and root), so none
	// of the three is drawn.
	refresh := requireTag(t, page, `data-action="refresh-metadata"`, `form="refresh-dispossessed"`)
	require.NotContains(t, refresh, "Scan", "a book has no folder of its own to scan")
	require.NotContains(t, page, `name="scan" value="true"`)
	requireTag(t, page, `data-action="search-now"`, `title="Search Book"`)
	requireTag(t, page, `data-action="book-interactive-search"`, `form="interactive-search-dispossessed"`)
	requireTag(t, page, `id="interactive-search-dispossessed"`, `hx-post="/library/default/book/dispossessed/search/interactive"`, `hx-target="#book-modal"`)
	require.NotContains(t, page, `data-action="rename-apply"`)
	require.NotContains(t, page, `data-edit-dialog="dispossessed"`, "an author's book is not editable (design §9.3)")
	require.NotContains(t, page, `data-action="delete"`, "an author's book is deleted with its author (design §9.3)")
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
// without an author -- and, since it has a folder of its own under its
// own root (design §9.3), its bar renders Refresh & Scan, Rename and Edit
// where an author's book's renders Refresh alone. A book whose author the
// library does not hold, or no book, is not found.
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
	requireTag(t, page, `data-action="refresh-metadata"`, `title="Refresh &amp; Scan"`)
	requireTag(t, page, `id="refresh-standalone"`, `action="/library/default/book/standalone/refresh"`)
	require.Contains(t, page, `name="scan" value="true"`)
	requireTag(t, page, `data-action="rename-dry-run"`, `form="rename-dry-run-standalone"`)
	requireTag(t, page, `data-action="rename-apply"`, `form="rename-standalone"`)
	requireTag(t, page, `data-edit-dialog="standalone"`)
	// Delete too (design §9.3; catalogv1.DeletableKinds since 2026-11-10):
	// the dialog posts to the book's delete route, names its folder, and
	// offers the import list exclusion, keyed by the book's Open Library
	// work (catalogv1.ExclusionKindFor, since 2026-11-10).
	requireTag(t, page, `data-action="delete"`)
	require.Contains(t, page, "Delete A Standalone Book")
	requireTag(t, page, `action="/library/default/book/standalone/delete"`)
	requireTag(t, page, `name="files"`, `value="true"`)
	requireTag(t, page, `name="exclude"`, `value="true"`)
	require.Contains(t, page, "Add import list exclusion")

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/book/nope", false).Code)
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/book/neuromancer", false).Code,
		"a book whose author the library does not hold")
}

// A standalone book's Refresh & Scan and Rename work on its own folder
// under its own root (design §9.3; itemFolder's Book case): each creates a
// LibraryScan restricted to it, as a movie's do. The Book reconciler
// writes a standalone book's status.path as the root itself (book.Path
// renders no author folder), so the folder is the one the import placed
// its file in, <root>/{Book Title} (catalogctx.BookFilePath): a standalone
// book without a file has no folder on disk yet, so its refresh scans
// nothing and its rename is refused. An author's book has no folder of its
// own -- one whose spec.rootFolderRef overrides its author's root
// included, since its status.path is then the author's whole folder -- so
// its page renders Refresh alone, no Rename and no Edit dialog, its
// refresh scans nothing and its rename is refused.
func TestStandaloneBookScansAndRenamesItsOwnFolder(t *testing.T) {
	srv, c := bookFixture(t)
	rec := postForm(t, srv, "/library/default/book/standalone-on-disk/refresh",
		url.Values{"return": {"/library/default/book/standalone-on-disk"}, "scan": {"true"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var scans catalogv1.LibraryScanList
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 1)
	require.Equal(t, "books", scans.Items[0].Spec.RootFolderRef)
	require.Equal(t, "A Standalone Book on Disk", scans.Items[0].Spec.Subpath, "the scan is restricted to the folder the import placed the book's file in")

	rec = postForm(t, srv, "/library/default/book/standalone-on-disk/rename",
		url.Values{"return": {"/library/default/book/standalone-on-disk"}, "rename": {"dryRun"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 2)
	var renames int
	for _, sc := range scans.Items {
		if sc.Spec.Rename == catalogv1.ScanRenameDryRun {
			renames++
			require.Equal(t, "A Standalone Book on Disk", sc.Spec.Subpath)
		}
	}
	require.Equal(t, 1, renames)

	// A standalone book with no file yet: nothing on disk to scan or
	// rename, and never the whole root.
	rec = postForm(t, srv, "/library/default/book/standalone/refresh",
		url.Values{"return": {"/library/default/book/standalone"}, "scan": {"true"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 2, "a standalone book without a file has no folder on disk yet")
	rec = postForm(t, srv, "/library/default/book/standalone/rename",
		url.Values{"return": {"/library/default/book/standalone"}, "rename": {"dryRun"}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// An author's book: the refresh alone, and no rename.
	rec = postForm(t, srv, "/library/default/book/dispossessed/refresh",
		url.Values{"return": {"/library/default/book/dispossessed"}, "scan": {"true"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 2, "an author's book has no folder of its own to scan")
	rec = postForm(t, srv, "/library/default/book/dispossessed/rename",
		url.Values{"return": {"/library/default/book/dispossessed"}, "rename": {"apply"}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 2)

	// An author's book whose root overrides the author's: still the
	// author's, so no Edit dialog, no Rename, Refresh without a scan, and
	// never a scan or a rename of the author's whole folder.
	page := getPath(t, srv, "/library/default/book/lavinia", false)
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	requireTag(t, body, `data-book-page="lavinia"`, `data-standalone="false"`)
	require.NotContains(t, body, `data-edit-dialog`, "an author's book is not editable (design §9.3)")
	require.NotContains(t, body, `data-action="rename-dry-run"`)
	requireTag(t, body, `data-action="refresh-metadata"`, `form="refresh-lavinia"`)
	require.NotContains(t, body, "Refresh &amp; Scan", "Refresh alone: no folder to scan")
	require.NotContains(t, body, `name="scan"`)
	rec = postForm(t, srv, "/library/default/book/lavinia/refresh",
		url.Values{"return": {"/library/default/book/lavinia"}, "scan": {"true"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 2, "an author's book with a root of its own still has no folder of its own")
	rec = postForm(t, srv, "/library/default/book/lavinia/rename",
		url.Values{"return": {"/library/default/book/lavinia"}, "rename": {"apply"}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// bookXOneLine joins a page's lines, so a regexp can span tags.
func bookXOneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }

// While import carries a standalone book's delete out the page hides the
// Delete button and the status fact reads Deleting with a Cancel; a refused
// one reads why and offers Retry beside it -- the book's own card carries
// the delete annotations, as the projection reads them.
func TestStandaloneBookPageShowsAPendingDeleteAndHidesTheButton(t *testing.T) {
	_, c := bookFixture(t)
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "standalone"}, Kind: commonv1.MediaKindBook,
		Tab: projection.TabBooks, Title: "A Standalone Book", Monitored: true, DeleteMode: catalogv1.DeleteRecords,
	}
	serve := func(li projection.LibraryItem) string {
		srv := ui.NewServer(t.Context(), ui.Options{
			Reader:  c,
			Actions: actions.New(c),
			Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{li} },
		})
		return getPath(t, srv, "/library/default/book/standalone", false).Body.String()
	}
	page := serve(item)
	require.NotContains(t, page, `data-action="delete"`, "no second request while one is pending")
	status := section(t, page, `data-fact="status"`)
	require.Contains(t, status, "Deleting")
	requireTag(t, status, `data-action="delete-cancel"`)
	requireTag(t, status, `action="/library/default/book/standalone/delete/cancel"`)
	require.NotContains(t, status, `data-action="delete-retry"`)

	item.DeleteError = "the item's root folder is unknown"
	page = serve(item)
	status = section(t, page, `data-fact="status"`)
	require.Contains(t, status, "Delete failed: the item&#39;s root folder is unknown")
	requireTag(t, status, `data-action="delete-retry"`)
	requireTag(t, status, `action="/library/default/book/standalone/delete"`)
	require.NotContains(t, status, `name="files"`, "retried as asked: records")
}
