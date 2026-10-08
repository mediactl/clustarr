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
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// bookFixture is an author with books in every state the status cell
// distinguishes -- on disk below the cutoff, downloading, pending,
// missing, unmonitored, unreleased, undated -- one book's file, another
// author's book, and a standalone book. The fake client is the reader and
// the writer.
func bookFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	date := func(y int, m time.Month, d int) *metav1.Time {
		tm := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
		return &tm
	}
	future := metav1.NewTime(time.Now().AddDate(1, 0, 0))
	author := &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "le-guin", Namespace: "default"},
		Spec:       catalogv1.AuthorSpec{OpenLibraryID: "OL1A", QualityProfileRef: "ebook", RootFolderRef: "books"},
	}
	book := func(name, authorRef, title string, released *metav1.Time, status catalogv1.BookStatus) *catalogv1.Book {
		status.Metadata = &catalogv1.BookMetadata{Title: title, ReleaseDate: released}
		b := &catalogv1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.BookSpec{WorkID: "OL-" + name, Monitored: new(true)},
			Status:     status,
		}
		if authorRef != "" {
			b.Spec.AuthorRef = new(authorRef)
		}
		return b
	}
	dispossessed := book("dispossessed", "le-guin", "The Dispossessed", date(1974, time.May, 1), catalogv1.BookStatus{
		HasFile: true, FileRef: new("dispossessed-file"), FileFormat: "EPUB", Phase: catalogv1.BookPhaseCutoffUnmet,
		Path: "/data/books/Ursula K. Le Guin/The Dispossessed (1974)",
	})
	dispossessed.Status.Metadata.Subtitle = "An Ambiguous Utopia"
	dispossessed.Status.Metadata.Overview = "Shevek, a physicist, travels from Anarres to Urras."
	dispossessed.Status.Metadata.Genres = []string{"Science fiction"}
	earthsea := book("wizard-of-earthsea", "le-guin", "A Wizard of Earthsea", date(1968, time.November, 1), catalogv1.BookStatus{
		Phase: catalogv1.BookPhaseDownloading, ActiveDownloadRef: new("dl"),
	})
	earthsea.Status.Metadata.SeriesLinks = []catalogv1.SeriesLink{{Series: "Hainish"}, {Series: "Earthsea", Position: "1", Primary: true}}
	lathe := book("lathe-of-heaven", "le-guin", "The Lathe of Heaven", date(1971, time.March, 1), catalogv1.BookStatus{Phase: catalogv1.BookPhaseUnmonitored})
	lathe.Spec.Monitored = new(false)
	file := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "dispossessed-file", Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			Path:      "/data/books/Ursula K. Le Guin/The Dispossessed (1974)/The Dispossessed.epub",
			SizeBytes: 2 << 20, Quality: commonv1.Quality{Name: "EPUB"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(author, file, dispossessed, earthsea, lathe,
		book("left-hand", "le-guin", "The Left Hand of Darkness", date(1969, time.March, 1), catalogv1.BookStatus{Phase: catalogv1.BookPhaseWanted}),
		book("lavinia", "le-guin", "Lavinia", date(2008, time.April, 1), catalogv1.BookStatus{Phase: catalogv1.BookPhaseDelayed}),
		book("forthcoming", "le-guin", "Forthcoming", &future, catalogv1.BookStatus{Phase: catalogv1.BookPhaseWanted}),
		book("poems", "le-guin", "Collected Poems", nil, catalogv1.BookStatus{Phase: catalogv1.BookPhaseWanted}),
		book("neuromancer", "gibson", "Neuromancer", date(1984, time.July, 1), catalogv1.BookStatus{}),
		book("standalone", "", "A Standalone Book", date(2001, time.January, 1), catalogv1.BookStatus{Phase: catalogv1.BookPhaseWanted}),
	).Build()
	items := []projection.LibraryItem{
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "le-guin"}, Kind: commonv1.MediaKindAuthor,
			Tab: projection.TabBooks, Title: "Ursula K. Le Guin", QualityProfileRef: "ebook", Monitored: true,
		},
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "standalone"}, Kind: commonv1.MediaKindBook,
			Tab: projection.TabBooks, Title: "A Standalone Book", Monitored: true,
		},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Actions: actions.New(c),
		Library: func(context.Context) []projection.LibraryItem { return items },
	})
	return srv, c
}

// An author's books are the season table: the status cell says, in the
// episode's order, what each book is waiting on, and every row has the
// automatic and interactive searches and a title that opens the book's
// details. The author's pages mount the modal and the action status.
func TestBookRowsAreTheSeasonTables(t *testing.T) {
	srv, _ := bookFixture(t)
	rec := getPath(t, srv, "/library/default/author/le-guin/children", true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html")
	require.Equal(t, 1, strings.Count(body, `data-book-table`), "the books are one table")

	for name, status := range map[string]string{
		"dispossessed": "file", "wizard-of-earthsea": "downloading", "lavinia": "pending", "left-hand": "missing",
		"lathe-of-heaven": "unmonitored", "forthcoming": "unreleased", "poems": "missing",
	} {
		requireTag(t, body, `data-book="`+name+`"`, `data-slot="table-row"`, `data-status="`+status+`"`)
	}
	require.NotContains(t, body, "Neuromancer", "another author's book is never listed")
	require.NotContains(t, body, `data-book="standalone"`, "a standalone book is no author's")
	require.Less(t, indexOf(body, `data-book="poems"`), indexOf(body, `data-book="wizard-of-earthsea"`), "undated first")
	require.Less(t, indexOf(body, `data-book="wizard-of-earthsea"`), indexOf(body, `data-book="left-hand"`), "then oldest first")
	require.Less(t, indexOf(body, `data-book="dispossessed"`), indexOf(body, `data-book="forthcoming"`))

	require.Regexp(t, `class="[^"]*bg-amber-500/20[^"]*"[^>]*title="Quality cutoff has not been met"[^>]*>EPUB<`, body,
		"a file below the cutoff is an amber format badge")
	require.Contains(t, body, `data-book-series="Earthsea #1"`, "the primary reading order, with its position")
	require.Contains(t, body, `<time datetime="1974-05-01" title="1974-05-01">May 1 1974</time>`)
	require.Contains(t, body, `hx-post="/library/default/book/left-hand/search" hx-target="#action-status"`)
	require.Contains(t, body, `hx-post="/library/default/book/left-hand/search/interactive" hx-target="#book-modal"`)
	require.Contains(t, body, `hx-get="/library/default/book/left-hand/details" hx-target="#book-modal"`)
	require.Contains(t, body, `hx-post="/library/default/book/left-hand/monitor" hx-target="closest tr"`)
	require.Contains(t, body, `name="return" value="/library/default/author/le-guin/children"`)

	for _, path := range []string{"/library/default/author/le-guin", "/library/default/author/le-guin/children"} {
		page := getPath(t, srv, path, false).Body.String()
		require.Equal(t, 1, strings.Count(page, `id="book-modal"`), path)
		require.Equal(t, 1, strings.Count(page, `id="action-status"`), path)
	}
}

// A book's monitor bookmark swaps the row it sits in, re-read from the
// patched Book.
func TestBookToggleSwapsItsRow(t *testing.T) {
	srv, c := bookFixture(t)
	rec := postForm(t, srv, "/library/default/book/left-hand/monitor", url.Values{"monitored": {"false"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	row := rec.Body.String()
	require.NotContains(t, row, "<html")
	requireTag(t, row, `data-book="left-hand"`, `data-slot="table-row"`, `data-monitored="false"`, `data-status="unmonitored"`)

	var got catalogv1.Book
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "left-hand"}, &got))
	require.False(t, *got.Spec.Monitored)
}

// A book's automatic search answers the page's #action-status in place,
// as an episode's does.
func TestBookAutomaticSearchAnswersInPlace(t *testing.T) {
	srv, c := bookFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/library/default/book/left-hand/search", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "action-status")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-status="ok"`)

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 1)
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "left-hand"}, list.Items[0].Spec.MediaRef)
	require.True(t, list.Items[0].Spec.GrabBest, "an automatic search grabs its best")
}

// The title opens the book's details modal for htmx and a page for anyone
// else: the heading, the overview, the genres and the file, Details open.
// A standalone book's heading names no author.
func TestBookDetailsServesTheModalToHTMXAndAPageOtherwise(t *testing.T) {
	srv, _ := bookFixture(t)
	rec := getPath(t, srv, "/library/default/book/dispossessed/details", true)
	require.Equal(t, http.StatusOK, rec.Code)
	modal := rec.Body.String()
	require.NotContains(t, modal, "<html")
	require.Contains(t, modal, `data-book-modal="dispossessed"`)
	require.Contains(t, modal, "Ursula K. Le Guin - The Dispossessed: An Ambiguous Utopia")
	require.Contains(t, modal, "Released May 1 1974")
	require.Contains(t, modal, "Shevek, a physicist, travels from Anarres to Urras.")
	require.Contains(t, modal, ">Science fiction<")
	requireTag(t, modal, `data-book-file="dispossessed-file"`)
	require.Contains(t, modal, "The Dispossessed.epub", "the path relative to the book's folder")
	require.NotContains(t, modal, "Ursula K. Le Guin/The Dispossessed (1974)/The Dispossessed.epub")
	require.Contains(t, modal, "2.00 MiB")
	requireTag(t, modal, `data-tab="details"`, `aria-selected="true"`)
	require.Contains(t, modal, `data-book-search="dispossessed"`)
	require.Contains(t, modal, "Automatic Search")
	require.Contains(t, modal, "Interactive Search")

	searchTab := getPath(t, srv, "/library/default/book/left-hand/details?tab=search", true).Body.String()
	requireTag(t, searchTab, `data-tab="search"`, `aria-selected="true"`)
	require.Contains(t, searchTab, `data-book-file=""`, "no file on disk")
	require.Contains(t, searchTab, `hx-select="#book-search-body"`)

	page := getPath(t, srv, "/library/default/book/dispossessed/details", false)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), `data-book-page="dispossessed"`)

	standalone := getPath(t, srv, "/library/default/book/standalone/details", true)
	require.Equal(t, http.StatusOK, standalone.Code)
	require.Contains(t, standalone.Body.String(), ">A Standalone Book<")
	require.Contains(t, standalone.Body.String(), `name="return" value="/library/default/book/standalone"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/book/nope/details", true).Code)
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/book/neuromancer/details", true).Code,
		"a book whose author the library does not hold")
}

// The row's interactive search creates a Search for the book that grabs
// nothing and answers the modal on its Search tab, the results panel
// polling. A form post lands on the panel's page; an unknown book creates
// nothing.
func TestBookInteractiveSearchOpensTheModalOnAPollingPanel(t *testing.T) {
	srv, c := bookFixture(t)
	rec := postForm(t, srv, "/library/default/book/left-hand/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 1)
	created := list.Items[0]
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "left-hand"}, created.Spec.MediaRef)
	require.False(t, created.Spec.GrabBest, "an interactive search grabs only what a person picks")

	require.Contains(t, body, `data-book-modal="left-hand"`)
	requireTag(t, body, `data-tab="search"`, `aria-selected="true"`)
	requireTag(t, body, `data-search-panel="`+created.Name+`"`,
		`hx-get="/searches/default/`+created.Name+`"`, `hx-trigger="load delay:2s"`)
	require.Contains(t, body, "Searching indexers")

	rec = postForm(t, srv, "/library/default/book/left-hand/search/interactive", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/searches/default/left-hand-"), rec.Header().Get("Location"))

	rec = postForm(t, srv, "/library/default/book/nope/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 2, "only the form post's Search; none for an unknown book")
}
