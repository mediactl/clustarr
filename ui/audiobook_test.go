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

// audiobookFixture is an audiobook on disk below the cutoff in three
// parts, named out of play order by its status, with chapters that align
// with the parts, an Author it names and the Book it is a recording of;
// a second audiobook with no metadata, no file, no author and no book;
// and a completed interactive Search for the first. The fake client is
// the reader and the writer.
func audiobookFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	released := metav1.NewTime(time.Date(2010, time.January, 5, 0, 0, 0, 0, time.UTC))
	part := func(n int32, minutes int64, bitrate int32) *catalogv1.MediaFile {
		return &catalogv1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "earthsea-ab-" + string(rune('0'+n)), Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				Path:      "/data/audiobooks/Ursula K. Le Guin/A Wizard of Earthsea/A Wizard of Earthsea - 0" + string(rune('0'+n)) + ".mp3",
				SizeBytes: minutes << 20, PartIndex: n, Quality: commonv1.Quality{Name: "MP3"},
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "earthsea-ab"},
			},
			Status: catalogv1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
				Container: "mp3", RuntimeMillis: minutes * 60 * 1000,
				Audio: []commonv1.AudioStream{{Codec: "mp3", BitrateKbps: bitrate, SampleRateHz: 44100, Channels: 2}},
			}},
		}
	}
	earthsea := &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "earthsea-ab", Namespace: "default"},
		Spec: catalogv1.AudiobookSpec{
			ASIN: "B00ABC1234", Region: catalogv1.AudiobookRegionUK, BookRef: new("wizard-of-earthsea"), AuthorRef: new("le-guin"),
			Monitored: new(true), QualityProfileRef: "audiobook", RootFolderRef: "audiobooks",
		},
		Status: catalogv1.AudiobookStatus{
			Metadata: &catalogv1.AudiobookMetadata{
				Title: "A Wizard of Earthsea", Subtitle: "The Earthsea Cycle, Book 1",
				Authors:   []catalogv1.NamedRef{{Name: "Ursula K. Le Guin", ASIN: "B000AP9A6K"}},
				Narrators: []string{"Rob Inglis"},
				Series:    &catalogv1.SeriesLink{Series: "Earthsea", Position: "1", Primary: true},
				Publisher: "Recorded Books", ReleaseDate: &released, Language: "en",
				Overview: "Ged was the greatest sorcerer in Earthsea.", Genres: []string{"Fantasy"},
				Chapters: []catalogv1.Chapter{
					{Title: "Warriors in the Mist", StartMs: 0},
					{Title: "The Shadow", StartMs: 3600000},
					{Title: "The School for Wizards", StartMs: 11100000},
				},
			},
			Phase: catalogv1.AudiobookPhaseCutoffUnmet, CutoffReason: "CutoffUnmet",
			Path:    "/data/audiobooks/Ursula K. Le Guin/A Wizard of Earthsea",
			HasFile: true, FileRefs: []string{"earthsea-ab-3", "earthsea-ab-1", "earthsea-ab-2"}, PartCount: 3,
			FileQuality: &commonv1.Quality{Name: "MP3"},
		},
	}
	leftHand := &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "left-hand-ab", Namespace: "default"},
		Spec:       catalogv1.AudiobookSpec{ASIN: "B00XYZ9876", Monitored: new(true), QualityProfileRef: "audiobook", RootFolderRef: "audiobooks"},
		Status:     catalogv1.AudiobookStatus{Phase: catalogv1.AudiobookPhaseWanted},
	}
	published := metav1.NewTime(time.Now().Add(-72 * time.Hour))
	search := &catalogv1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "earthsea-ab-abc12", Namespace: "default"},
		Spec:       catalogv1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "earthsea-ab"}},
		Status: catalogv1.SearchStatus{
			Phase: catalogv1.SearchPhaseCompleted,
			Results: []commonv1.ReleaseDecision{
				{ReleaseInfo: commonv1.ReleaseInfo{
					GUID: "m4b-1", Title: "A Wizard of Earthsea (Unabridged) M4B", IndexerName: "nzbgeek", Protocol: commonv1.ProtocolUsenet,
					SizeBytes: 300 << 20, PublishedAt: &published, Quality: commonv1.Quality{Name: "M4B"}, FormatScore: 10,
				}, Approved: true, Rank: 1},
				{ReleaseInfo: commonv1.ReleaseInfo{
					GUID: "mp3-2", Title: "A Wizard of Earthsea MP3", IndexerName: "tracker", Protocol: commonv1.ProtocolTorrent,
					SizeBytes: 100 << 20, PublishedAt: &published, Quality: commonv1.Quality{Name: "MP3"}, Seeders: new(int32(12)), Leechers: new(int32(3)),
				}, Approved: true, Rank: 2},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(
		&catalogv1.Author{ObjectMeta: metav1.ObjectMeta{Name: "le-guin", Namespace: "default"}, Spec: catalogv1.AuthorSpec{OpenLibraryID: "OL1A", QualityProfileRef: "ebook", RootFolderRef: "books"}},
		&catalogv1.Book{ObjectMeta: metav1.ObjectMeta{Name: "wizard-of-earthsea", Namespace: "default"}, Spec: catalogv1.BookSpec{WorkID: "OL45804W", AuthorRef: new("le-guin")}},
		earthsea, leftHand, search,
		part(1, 60, 128), part(2, 125, 128), part(3, 30, 96),
	).Build()
	items := []projection.LibraryItem{
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "earthsea-ab"}, Kind: commonv1.MediaKindAudiobook,
			Tab: projection.TabAudiobooks, Title: "A Wizard of Earthsea", Year: 2010, QualityProfileRef: "audiobook",
			Monitored: true, Phase: "CutoffUnmet", HasFile: true,
		},
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "left-hand-ab"}, Kind: commonv1.MediaKindAudiobook,
			Tab: projection.TabAudiobooks, Title: "The Left Hand of Darkness", QualityProfileRef: "audiobook", Monitored: true, Phase: "Wanted",
		},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Actions: actions.New(c),
		Library: func(context.Context) []projection.LibraryItem { return items },
	})
	return srv, c
}

// The audiobook page is the movie page for one recording (2026-11-10
// design §9.5): a hero with the cover, title, subtitle, authors linked to
// the Author's page, narrators, series, runtime from the parts, release
// date, abridgement, language and the three provider logos; the facts
// (Status with the cutoff reason, Quality, Profile, Path, Parts,
// Publisher); the action bar without Images; the Parts table in play
// order titled by the aligned chapters; the Chapters collapsible; the
// Files section under the audio columns; and the modal region.
func TestAudiobookPageIsTheMoviePageForARecording(t *testing.T) {
	srv, _ := audiobookFixture(t)
	rec := getPath(t, srv, "/library/default/audiobook/earthsea-ab", false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<html")
	requireTag(t, body, `data-audiobook-page="earthsea-ab"`, `data-kind="audiobook"`, `data-status="file"`, `data-hasfile="true"`,
		`data-profile="audiobook"`, `data-monitored="true"`, `data-year="2010"`)

	// The hero.
	require.Contains(t, body, `<p data-subtitle class="mt-1 text-2xl text-muted-foreground">The Earthsea Cycle, Book 1</p>`)
	requireTag(t, body, `data-author-link`, `href="/library/default/author/le-guin"`)
	require.Contains(t, body, `data-author-link class="font-medium hover:underline">Ursula K. Le Guin</a>`)
	require.Regexp(t, `data-narrators>.*narrated by .*Rob Inglis`, body)
	require.Contains(t, body, `data-series="Earthsea #1"`)
	require.Contains(t, body, `<span data-runtime>3h 35m</span>`, "the parts' durations, the metadata naming none")
	require.Contains(t, body, `<time datetime="2010-01-05" title="2010-01-05" data-released>Released Jan 5 2010</time>`)
	requireTag(t, body, `data-abridged="false"`)
	require.Contains(t, body, ">Unabridged<")
	for provider, href := range map[string]string{
		"audible":     "https://www.audible.co.uk/pd/B00ABC1234",
		"audnexus":    "https://api.audnex.us/books/B00ABC1234?region=uk",
		"openlibrary": "https://openlibrary.org/works/OL45804W",
	} {
		requireTag(t, body, `data-provider-link="`+provider+`"`, `href="`+href+`"`)
	}

	// The facts.
	require.Regexp(t, `data-fact="status">.*Cutoff Unmet`, oneLine(body))
	require.Regexp(t, `data-fact="quality">.*>MP3<`, oneLine(body))
	require.Regexp(t, `data-fact="profile">.*>audiobook<`, oneLine(body))
	require.Regexp(t, `data-fact="path">.*Ursula K. Le Guin/A Wizard of Earthsea<`, oneLine(body))
	require.Regexp(t, `data-fact="parts">.*3 of 3`, oneLine(body))
	require.Regexp(t, `data-fact="publisher">.*Recorded Books`, oneLine(body))
	require.Regexp(t, `data-fact="language">.*English`, oneLine(body))
	require.Regexp(t, `data-fact="genres">.*Fantasy`, oneLine(body))
	require.Contains(t, body, `data-overview class="mt-6 max-w-5xl text-base leading-relaxed">Ged was the greatest sorcerer in Earthsea.</p>`)
	require.NotContains(t, body, `data-audio=`, "an audiobook has no dub state, so no audio note")

	// The action bar: Refresh & Scan, Search, Interactive, Rename, Edit,
	// Delete; no Images.
	requireTag(t, body, `data-action="refresh-metadata"`, `title="Refresh &amp; Scan"`)
	requireTag(t, body, `data-action="search-now"`, `title="Search Audiobook"`)
	requireTag(t, body, `hx-post="/library/default/audiobook/earthsea-ab/search/interactive"`, `hx-target="#audiobook-modal"`)
	require.Contains(t, body, `data-action="audiobook-interactive-search"`)
	require.Contains(t, body, `data-action="rename-dry-run"`)
	require.Contains(t, body, `data-action="rename-apply"`)
	require.Contains(t, body, `data-edit-dialog="earthsea-ab"`)
	require.Contains(t, body, `data-delete-folder>/data/audiobooks/Ursula K. Le Guin/A Wizard of Earthsea<`)
	require.NotContains(t, body, `imageset-earthsea-ab`, "an audiobook has no image sets")

	// The Parts table: play order, the aligned chapters' titles, the
	// probe's duration, codec and bitrate.
	require.Equal(t, 1, strings.Count(body, `data-parts-table`))
	require.Less(t, indexOf(body, `data-part="1"`), indexOf(body, `data-part="2"`), "parts in play order, not the status's")
	require.Less(t, indexOf(body, `data-part="2"`), indexOf(body, `data-part="3"`))
	requireTag(t, body, `data-part="1"`, `data-part-file="earthsea-ab-1"`)
	first := body[indexOf(body, `data-part="1"`):indexOf(body, `data-part="2"`)]
	require.Contains(t, first, ">Warriors in the Mist<", "the chapter's title when parts and chapters align")
	require.Contains(t, first, ">1:00:00<")
	require.Contains(t, first, ">MP3<")
	require.Contains(t, first, ">128 kbps<")
	require.Contains(t, first, ">60.00 MiB<")
	third := body[indexOf(body, `data-part="3"`):indexOf(body, `data-section="chapters"`)]
	require.Contains(t, third, ">The School for Wizards<")
	require.Contains(t, third, ">30:00<")

	// The Chapters collapsible, closed.
	requireTag(t, body, `data-chapters="3"`, `data-closed`)
	require.Contains(t, body, ">Show 3 chapters<")
	chapter := body[indexOf(body, `data-chapter="3"`):indexOf(body, `data-section="files"`)]
	require.Contains(t, chapter, ">The School for Wizards<")
	require.Contains(t, chapter, ">3:05:00<")

	// The Files section lists every part under the audio columns.
	files := body[indexOf(body, `data-section="files"`):]
	require.Equal(t, 3, strings.Count(files, `data-file="earthsea-ab-`))
	require.Contains(t, files, ">Codec<")
	require.Contains(t, files, ">Sample Rate<")
	require.Contains(t, files, `data-cell="sampleRate">44.1 kHz<`)

	require.Equal(t, 1, strings.Count(body, `id="audiobook-modal"`))
	require.Equal(t, 1, strings.Count(body, `id="action-status"`))

	// An audiobook with no metadata and no file: missing, no parts fact,
	// no abridgement, Audible in the default marketplace, no Open Library.
	rec = getPath(t, srv, "/library/default/audiobook/left-hand-ab", false)
	require.Equal(t, http.StatusOK, rec.Code)
	bare := rec.Body.String()
	requireTag(t, bare, `data-audiobook-page="left-hand-ab"`, `data-status="missing"`, `data-hasfile="false"`)
	require.Regexp(t, `data-fact="status">.*Missing`, oneLine(bare))
	require.NotContains(t, bare, `data-fact="parts"`)
	require.NotContains(t, bare, `data-fact="quality"`)
	require.NotContains(t, bare, `data-abridged`)
	require.NotContains(t, bare, `data-author-link`)
	requireTag(t, bare, `data-provider-link="audible"`, `href="https://www.audible.com/pd/B00XYZ9876"`)
	requireTag(t, bare, `data-provider-link="audnexus"`, `href="https://api.audnex.us/books/B00XYZ9876"`)
	require.NotContains(t, bare, `data-provider-link="openlibrary"`)
	require.Contains(t, bare, "No parts on disk.")
	require.NotContains(t, bare, `data-section="chapters"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/audiobook/nope", false).Code)
}

// The details route serves the audiobook's modal to htmx -- "Author -
// Title: Subtitle", the facts line, the overview, the genres and the
// parts on the Details tab, the two searches on the Search tab -- and the
// page to anyone else.
func TestAudiobookDetailsServesTheModalToHTMXAndThePageOtherwise(t *testing.T) {
	srv, _ := audiobookFixture(t)
	rec := getPath(t, srv, "/library/default/audiobook/earthsea-ab/details", true)
	require.Equal(t, http.StatusOK, rec.Code)
	modal := rec.Body.String()
	require.NotContains(t, modal, "<html")
	require.Contains(t, modal, `data-audiobook-modal="earthsea-ab"`)
	require.Contains(t, modal, "Ursula K. Le Guin - A Wizard of Earthsea: The Earthsea Cycle, Book 1")
	require.Contains(t, modal, "Narrated by Rob Inglis · Released Jan 5 2010 · Earthsea #1 · 3h 35m · MP3")
	require.Contains(t, modal, "Ged was the greatest sorcerer in Earthsea.")
	require.Contains(t, modal, ">Fantasy<")
	require.Contains(t, modal, `data-parts-table`)
	require.Contains(t, modal, `data-part="2"`)
	requireTag(t, modal, `data-tab="details"`, `aria-selected="true"`)
	require.Contains(t, modal, `data-audiobook-search="earthsea-ab"`)
	require.Contains(t, modal, "Automatic Search")
	require.Contains(t, modal, "Interactive Search")
	require.Contains(t, modal, `hx-post="/library/default/audiobook/earthsea-ab/search" hx-target="#action-status"`)
	require.Contains(t, modal, `hx-select="#audiobook-search-body"`)

	searchTab := getPath(t, srv, "/library/default/audiobook/earthsea-ab/details?tab=search", true).Body.String()
	requireTag(t, searchTab, `data-tab="search"`, `aria-selected="true"`)

	page := getPath(t, srv, "/library/default/audiobook/earthsea-ab/details", false)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), `data-audiobook-page="earthsea-ab"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/audiobook/nope/details", true).Code)
}

// The bar's Interactive Search creates a Search for the audiobook that
// grabs nothing and answers the modal on its Search tab, the results panel
// polling the shared /searches route. A form post lands on the panel's
// page; an unknown audiobook creates nothing.
func TestAudiobookInteractiveSearchOpensTheModalOnTheSharedPanel(t *testing.T) {
	srv, c := audiobookFixture(t)
	rec := postForm(t, srv, "/library/default/audiobook/earthsea-ab/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 2, "the fixture's and the one just created")
	var created catalogv1.Search
	for _, s := range list.Items {
		if s.Name != "earthsea-ab-abc12" {
			created = s
		}
	}
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "earthsea-ab"}, created.Spec.MediaRef)
	require.False(t, created.Spec.GrabBest, "an interactive search grabs only what a person picks")

	require.Contains(t, body, `data-audiobook-modal="earthsea-ab"`)
	requireTag(t, body, `data-tab="search"`, `aria-selected="true"`)
	requireTag(t, body, `data-search-panel="`+created.Name+`"`,
		`hx-get="/searches/default/`+created.Name+`"`, `hx-trigger="load delay:2s"`, `hx-swap="outerHTML"`)
	require.Contains(t, body, "Searching indexers")
	requireTag(t, body, `id="audiobook-search-body"`, `data-audiobook-search="earthsea-ab"`)

	rec = postForm(t, srv, "/library/default/audiobook/earthsea-ab/search/interactive", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/searches/default/earthsea-ab-"), rec.Header().Get("Location"))

	rec = postForm(t, srv, "/library/default/audiobook/nope/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 3, "only the form post's Search; none for an unknown audiobook")
}

// The shared /searches route answers an audiobook's Search under the
// audiobook's columns -- Format, Bitrate (estimated from the size over the
// audiobook's runtime, read from the Audiobook since the Search carries
// none), Size, Age, Indexer, Peers, Score -- with the download button
// posting to the shared grab route, which answers the same columns with the
// grab requested; anyone but htmx gets the panel's page, a Search that is
// gone says so, and the audiobook's own panel and grab routes are gone.
func TestTheSharedPanelDrawsAnAudiobooksColumnsThroughPollAndGrab(t *testing.T) {
	srv, c := audiobookFixture(t)
	panelURL := "/searches/default/earthsea-ab-abc12"
	rec := getPath(t, srv, panelURL, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html")
	requireTag(t, body, `data-search-panel="earthsea-ab-abc12"`, `data-search-phase="Completed"`)
	require.NotContains(t, body, `hx-trigger="load delay:2s"`, "a completed search with nothing requested stops polling")
	require.Contains(t, body, "2 results from 0 indexers")
	requireTag(t, body, `data-release-table`, `data-release-columns="audiobook"`)

	heads := body[indexOf(body, `data-release-table`):indexOf(body, `data-release="m4b-1"`)]
	for _, label := range []string{">Format<", ">Bitrate<", ">Size<", ">Age<", ">Indexer<", ">Peers<", ">Score<"} {
		require.Contains(t, heads, label)
	}
	require.Less(t, indexOf(heads, ">Format<"), indexOf(heads, ">Bitrate<"))
	require.Less(t, indexOf(heads, ">Bitrate<"), indexOf(heads, ">Size<"))
	require.Less(t, indexOf(heads, ">Size<"), indexOf(heads, ">Age<"))
	require.Less(t, indexOf(heads, ">Age<"), indexOf(heads, ">Indexer<"))
	require.Less(t, indexOf(heads, ">Indexer<"), indexOf(heads, ">Peers<"))
	require.Less(t, indexOf(heads, ">Peers<"), indexOf(heads, ">Score<"))
	require.NotContains(t, heads, ">Languages<", "Sonarr's columns are not the audiobook's")

	m4b := body[indexOf(body, `data-release="m4b-1"`):indexOf(body, `data-release="mp3-2"`)]
	require.Regexp(t, `data-cell="format">.*>M4B<`, oneLine(m4b))
	require.Regexp(t, `data-cell="bitrate"[^>]*>195 kbps<`, m4b, "300 MiB over 3h 35m, the parts' durations")
	require.Contains(t, m4b, ">300.00 MiB<")
	require.Contains(t, m4b, ">3 days<")
	require.Contains(t, m4b, ">nzbgeek<")
	requireTag(t, m4b, `hx-post="`+panelURL+`/grab"`, `hx-target="closest [data-search-panel]"`)
	mp3 := body[indexOf(body, `data-release="mp3-2"`):]
	require.Contains(t, mp3, ">12/3<", "a torrent's seeders/leechers")

	rec = postForm(t, srv, panelURL+"/grab", url.Values{"guid": {"m4b-1"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	after := rec.Body.String()
	requireTag(t, after, `data-release="m4b-1"`, `data-grab="requested"`)
	require.Contains(t, after, `data-release-columns="audiobook"`, "the grab answers the audiobook's columns")
	require.Regexp(t, `data-cell="bitrate"[^>]*>195 kbps<`, after, "and the runtime it estimates from")
	requireTag(t, after, `data-search-panel="earthsea-ab-abc12"`, `hx-get="`+panelURL+`"`, `hx-trigger="load delay:2s"`) // a requested grab keeps the panel polling
	var search catalogv1.Search
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "earthsea-ab-abc12"}, &search))
	require.Equal(t, []string{"m4b-1"}, search.Spec.Grab)

	rec = postForm(t, srv, panelURL+"/grab", url.Values{"guid": {"nope"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error=`, "an unknown release is a failure in the panel")

	page := getPath(t, srv, panelURL, false)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), `data-release-columns="audiobook"`, "the page draws the same columns")

	gone := getPath(t, srv, "/searches/default/earthsea-ab-gone", true)
	require.Equal(t, http.StatusOK, gone.Code)
	require.Contains(t, gone.Body.String(), "This search has expired")
	require.NotContains(t, gone.Body.String(), `hx-trigger="load delay:2s"`)

	// The audiobook's own panel and grab routes are gone (C1, 2026-11-10).
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/audiobook/earthsea-ab/search/panel/earthsea-ab-abc12", true).Code)
	require.Equal(t, http.StatusNotFound, postForm(t, srv, "/library/default/audiobook/earthsea-ab/search/panel/earthsea-ab-abc12/grab", url.Values{"guid": {"m4b-1"}}, true).Code)
}

// oneLine folds a body onto one line so a regexp with "." can span the
// markup between an attribute and its content.
func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }
