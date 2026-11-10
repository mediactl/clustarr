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
	"regexp"
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

// comicFixture is a comic with issues in every state the status cell
// distinguishes -- on disk below the cutoff, downloading, pending, missing,
// unmonitored, not out yet, undated -- plus an issue numbered past the
// integers, one issue's file, another comic's issue, and a Search already
// answered for the missing issue. The fake client is the reader and the
// writer; the Issue index is the ui's own (ui.IssueComicRefField).
func comicFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	date := func(y int, m time.Month, d int) *metav1.Time {
		tm := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
		return &tm
	}
	future := metav1.NewTime(time.Now().AddDate(1, 0, 0))
	comic := &catalogv1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: "default", UID: "uid-saga"},
		Spec: catalogv1.ComicSpec{
			Source: catalogv1.ComicSourceComicVine, SourceID: "4050-56582", Kind: catalogv1.ComicKindComic,
			QualityProfileRef: "comic", RootFolderRef: "comics", Monitored: new(true),
		},
		Status: catalogv1.ComicStatus{
			Path: "/data/comics/Saga (2012)", IssueFileCount: 1,
			Metadata: &catalogv1.ComicMetadata{
				Title: "Saga", Publisher: "Image Comics", Year: 2012, VolumeNumber: 1, IssueCount: 72,
				Manga: catalogv1.MangaFlagNo, AgeRating: "Mature", Status: catalogv1.ComicRunStatusContinuing,
				Genres: []string{"Science Fiction", "Fantasy"}, OriginalLanguage: "en",
				Overview:    "Two soldiers from opposite sides of a galactic war fall in love.",
				ExternalIDs: map[string]string{"anilist": "98765", "metron": "1"},
				// Metron's series page, by the slug the metron client
				// records (the id alone addresses nothing on its site).
				Links: []catalogv1.Link{{Type: "metron", URL: "https://metron.cloud/series/saga-2012/"}},
			},
			Artwork: []catalogv1.ArtworkEntry{
				{Type: catalogv1.ImageTypePoster, Digest: "p0st3r"},
				{Type: catalogv1.ImageTypeFanart, Digest: "f4n4rt"},
			},
			// The volume search's state serves a person's planned volume
			// Search (C6), which the badge names.
			VolumeSearch: &catalogv1.SeasonSearchState{
				Request:   catalogv1.SeasonSearchRequest{Name: "saga-volume-x1", UID: "uid-search"},
				StartedAt: metav1.NewTime(time.Now().Add(-time.Minute)),
				Seasons:   []catalogv1.SeasonSearchSeason{{Number: 1, Phase: catalogv1.SeasonSearchSearching}},
			},
		},
	}
	issue := func(name, comicRef, number string, centis int32, title string, status catalogv1.IssueStatus) *catalogv1.Issue {
		status.Title = title
		return &catalogv1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.IssueSpec{ComicRef: comicRef, Number: number, CalculatedNumberCentis: centis, Monitored: new(true)},
			Status:     status,
		}
	}
	first := issue("saga-001.0", "saga", "1", 100, "Chapter One", catalogv1.IssueStatus{
		Date: date(2012, time.March, 14), StoreDate: date(2012, time.March, 14),
		HasFile: true, FileRef: new("saga-001.0-file"), FileFormat: "CBZ", FileQuality: &commonv1.Quality{Name: "CBZ"},
		Phase: catalogv1.IssuePhaseCutoffUnmet, CutoffReason: "CutoffUnmet",
		Overview: "Marko and Alana flee with their newborn daughter.",
		SourceID: "4000-12345",
		// ComicVine's image host is not one /art/search fetches from, so
		// the page falls back to the comic's poster.
		CoverURL: "https://comicvine.gamespot.com/a/uploads/scale_large/6/67663/2255420-saga1.jpg",
		Credits:  []catalogv1.Credit{{Name: "Brian K. Vaughan", Role: "writer"}, {Name: "Fiona Staples", Role: "artist"}},
	})
	unmonitored := issue("saga-005.0", "saga", "5", 500, "Chapter Five", catalogv1.IssueStatus{Date: date(2012, time.July, 18), Phase: catalogv1.IssuePhaseUnmonitored})
	unmonitored.Spec.Monitored = new(false)
	file := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "saga-001.0-file", Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			Path: "/data/comics/Saga (2012)/Saga 001 (2012).cbz", SizeBytes: 40 << 20,
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001.0"},
			Quality:  commonv1.Quality{Name: "CBZ"}, FormatScore: 10, ReleaseGroup: "Zone-Empire",
		},
		Status: catalogv1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{PageCount: 44}},
	}
	answered := &catalogv1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "saga-004.0-abc12", Namespace: "default"},
		Spec:       catalogv1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-004.0"}},
		Status: catalogv1.SearchStatus{
			Phase:           catalogv1.SearchPhaseCompleted,
			IndexerOutcomes: []catalogv1.IndexerOutcome{{Name: "nzbgeek", State: catalogv1.IndexerOutcomeOK, Count: 2}},
			Results: []commonv1.ReleaseDecision{
				comicRelease("guid-best", 1, "Saga 004 (2012) (Digital) (Zone-Empire).cbz", "CBZ"),
				comicRelease("guid-pdf", 2, "Saga 004 (2012) (Digital).pdf", "PDF",
					commonv1.Rejection{Reason: "Quality: PDF is not wanted in profile", Type: commonv1.RejectionPermanent}),
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(comic, file, answered, first, unmonitored,
		issue("saga-002.0", "saga", "2", 200, "Chapter Two", catalogv1.IssueStatus{Date: date(2012, time.April, 18), Phase: catalogv1.IssuePhaseDownloading, ActiveDownloadRef: new("dl")}),
		issue("saga-003.0", "saga", "3", 300, "Chapter Three", catalogv1.IssueStatus{Date: date(2012, time.May, 16), Phase: catalogv1.IssuePhaseDelayed}),
		issue("saga-004.0", "saga", "4", 400, "Chapter Four", catalogv1.IssueStatus{
			Date: date(2012, time.June, 20), Phase: catalogv1.IssuePhaseWanted, SourceIDs: map[string]string{"comicvine": "4000-23456"},
			// A cover on a host /art/search fetches from (the TMDB image
			// host stands in for one) is served through the proxy.
			CoverURL: "https://image.tmdb.org/t/p/original/saga4.jpg",
		}),
		issue("saga-006.0", "saga", "6", 600, "Chapter Six", catalogv1.IssueStatus{Date: &future, StoreDate: &future, Phase: catalogv1.IssuePhaseUnavailable}),
		issue("saga-007.0", "saga", "7", 700, "", catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted}),
		issue("saga-012.1-hu-0123456789", "saga", "12.HU", 1210, "Chapter Twelve, Hastings variant", catalogv1.IssueStatus{Date: date(2013, time.May, 1), Phase: catalogv1.IssuePhaseWanted}),
		issue("paper-girls-001.0", "paper-girls", "1", 100, "Paper Girls #1", catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted}),
	).WithIndex(&catalogv1.Issue{}, ui.IssueComicRefField, ui.IndexIssueByComicRef).Build()
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "saga"}, Kind: commonv1.MediaKindComic,
		Tab: projection.TabComics, Title: "Saga", Year: 2012, QualityProfileRef: "comic", Monitored: true, HasFile: true,
		Poster: "/art/comic/uid-saga/poster?v=p0st3r", ArtKey: "comic/uid-saga/poster",
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Actions: actions.New(c),
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})
	return srv, c
}

// comicRelease is one interactive search result for an issue: a comic
// archive of the quality the comic ladder names (CBZ, CBR, PDF).
func comicRelease(guid string, rank int32, title, format string, rejections ...commonv1.Rejection) commonv1.ReleaseDecision {
	published := metav1.NewTime(time.Now().Add(-50 * time.Hour))
	return commonv1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{
			GUID: guid, Title: title, IndexerName: "nzbgeek", Protocol: commonv1.ProtocolUsenet,
			SizeBytes: 40 << 20, PublishedAt: &published, Quality: commonv1.Quality{Name: format}, FormatScore: 10,
		},
		Approved: len(rejections) == 0, Rejections: rejections, Rank: rank,
	}
}

// A comic's page is the series page for a volume: the hero reads the
// Comic's metadata -- publisher, years, volume, issue count, kind, age
// rating, logos, genres -- the action bar has the volume search beside the
// shared actions, the issues table is Mylar's with the episode's status
// cell, in number order, and the volume search's badge reads
// status.volumeSearch. The page mounts the issue modal and the action
// status.
func TestComicPageIsMylarsIssuesTable(t *testing.T) {
	srv, _ := comicFixture(t)
	rec := getPath(t, srv, "/library/default/comic/saga", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"), "a card's link preloads the page")
	page := rec.Body.String()
	require.Contains(t, page, "<html")

	// The hero.
	requireTag(t, page, `data-ref="default/saga"`, `data-hero`, `data-kind="comic"`, `data-monitored="true"`,
		`data-hasfile="true"`, `data-year="2012"`, `data-profile="comic"`, `data-poster="art"`)
	require.Regexp(t, `<h1[^>]*>Saga</h1>`, page)
	require.Regexp(t, `data-backdrop src="/art/comic/uid-saga/fanart\?v=f4n4rt"`, page, "the background the provider has")
	require.Regexp(t, `data-age-rating="Mature"[^>]*>Mature<`, page)
	require.Regexp(t, `data-years[^>]*>2012–<`, page, "a run that continues")
	require.Regexp(t, `data-volume[^>]*>Vol\. 1<`, page)
	require.Regexp(t, `data-issue-count[^>]*>72 issues<`, page)
	require.Regexp(t, `data-comic-kind="comic"[^>]*data-manga="no"[^>]*>Comic<`, page)
	require.Contains(t, page, `href="https://comicvine.gamespot.com/volume/4050-56582/"`, "the source's own page")
	require.Contains(t, page, `<img src="/static/providers/comicvine.svg" alt="ComicVine"`)
	require.Contains(t, page, `href="https://anilist.co/manga/98765"`)
	require.Contains(t, page, `<img src="/static/providers/anilist.svg" alt="AniList"`)
	require.Contains(t, page, `href="https://metron.cloud/series/saga-2012/"`, "the metadata's links entry of type metron, the slug page the client recorded")
	require.Contains(t, page, `<img src="/static/providers/metron.svg" alt="Metron"`)
	require.NotContains(t, page, "metron.cloud/series/1", "the Metron id alone builds no page")
	require.Contains(t, page, "Two soldiers from opposite sides of a galactic war fall in love.")
	for fact, want := range map[string]string{
		"path":        "/data/comics/Saga (2012)",
		"status":      "Downloaded",
		"profile":     "comic",
		"publisher":   "Image Comics",
		"publication": "Continuing",
		"issues":      "1 / 72",
		"language":    "English",
		"genres":      "Science Fiction, Fantasy",
	} {
		require.Regexp(t, `data-fact="`+fact+`"[\s\S]*?`+regexp.QuoteMeta(want), page, fact)
	}

	// The action bar: the shared slots and the volume search between them.
	bar := page[strings.Index(page, `data-toolbar`):strings.Index(page, `data-hero`)]
	for _, a := range []string{`data-action="refresh-metadata"`, `data-action="search-now"`, `data-action="comic-volume-search"`,
		`data-action="rename-dry-run"`, `data-action="rename-apply"`} {
		require.Contains(t, bar, a, "the actions live in the bar")
	}
	require.NotContains(t, bar, `data-action="comic-interactive-search"`, "a comic has no interactive search of itself as an item")
	require.Contains(t, bar, `data-action="comic-volume-interactive-search"`, "the volume's interactive search, a season header's person")
	require.Less(t, strings.Index(bar, `data-action="search-now"`), strings.Index(bar, `data-action="comic-volume-search"`))
	require.Less(t, strings.Index(bar, `data-action="comic-volume-search"`), strings.Index(bar, `data-action="comic-volume-interactive-search"`))
	require.Less(t, strings.Index(bar, `data-action="comic-volume-interactive-search"`), strings.Index(bar, `data-action="rename-dry-run"`))
	requireTag(t, page, `id="volume-search-saga"`, `action="/library/default/comic/saga/volume/search"`,
		`hx-post="/library/default/comic/saga/volume/search"`, `hx-target="#action-status"`)
	requireTag(t, page, `id="volume-interactive-search-saga"`, `action="/library/default/comic/saga/volume/search"`,
		`hx-post="/library/default/comic/saga/volume/search"`, `hx-target="#issue-modal"`)
	require.Regexp(t, `id="volume-interactive-search-saga"[^>]*>[\s\S]*?name="interactive" value="true"[\s\S]*?</form>`, page,
		"the interactive volume search is the one route's form with the interactive field")
	require.Contains(t, page, "Search Monitored Issues")

	// The issues table and its volume badge.
	requireTag(t, page, `data-volume-search="Searching"`, `title="The volume is being searched for a pack (the search saga-volume-x1)"`)
	require.Equal(t, 1, strings.Count(page, `data-issue-table`), "the issues are one table")
	for name, status := range map[string]string{
		"saga-001.0": "file", "saga-002.0": "downloading", "saga-003.0": "pending", "saga-004.0": "missing",
		"saga-005.0": "unmonitored", "saga-006.0": "unreleased", "saga-007.0": "tba", "saga-012.1-hu-0123456789": "missing",
	} {
		requireTag(t, page, `data-issue="`+name+`"`, `data-slot="table-row"`, `data-status="`+status+`"`)
	}
	requireTag(t, page, `data-issue="saga-001.0"`, `data-number="1"`, `data-quality="CBZ"`, `data-phase="CutoffUnmet"`)
	require.Regexp(t, `class="[^"]*bg-amber-500/20[^"]*"[^>]*title="Quality cutoff has not been met"[^>]*>CBZ<`, page,
		"a file below the cutoff is an amber format badge")
	require.NotContains(t, page, "Paper Girls", "another comic's issue is never listed")
	for _, pair := range [][2]string{
		{"saga-001.0", "saga-002.0"}, {"saga-006.0", "saga-007.0"}, {"saga-007.0", "saga-012.1-hu-0123456789"},
	} {
		require.Less(t, indexOf(page, `data-issue="`+pair[0]+`"`), indexOf(page, `data-issue="`+pair[1]+`"`), "number order")
	}
	require.Regexp(t, `href="/library/default/issue/saga-001.0"[^>]*preload="mouseover"[^>]*data-issue-title[^>]*>\s*Chapter One\s*<`, page,
		"the title opens the issue's page and preloads it")
	require.Regexp(t, `data-issue-title[^>]*>\s*#7\s*<`, page, "an untitled issue is its number")
	require.Contains(t, page, `<time datetime="2012-03-14" title="2012-03-14" data-cover-date>Mar 14 2012</time>`)
	require.Contains(t, page, `<time datetime="2012-03-14" title="2012-03-14" data-store-date>Mar 14 2012</time>`)
	require.Contains(t, page, `hx-post="/library/default/issue/saga-004.0/search" hx-target="#action-status"`)
	require.Contains(t, page, `hx-post="/library/default/issue/saga-004.0/search/interactive" hx-target="#issue-modal"`)
	require.Contains(t, page, `hx-post="/library/default/issue/saga-004.0/monitor" hx-target="closest tr"`)
	require.Contains(t, page, `name="return" value="/library/default/comic/saga"`)
	require.Equal(t, 1, strings.Count(page, `id="issue-modal"`))
	require.Equal(t, 1, strings.Count(page, `id="action-status"`))

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/comic/nope", false).Code)
}

// The action bar's "Search Volume" plans the volume: a Search naming the
// Comic with spec.season its volume (actions.VolumeNumber) and grabBest,
// which the Comic key's volume planner serves as one pack search (design
// §9.4, D10; the W4b.3 leftover), never a Search that fans out per issue.
// It answers in place for htmx and redirects a form post.
func TestComicVolumeSearchPlansTheVolume(t *testing.T) {
	srv, c := comicFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/library/default/comic/saga/volume/search", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "action-status")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-status="ok"`)
	require.Contains(t, rec.Body.String(), "Searching the volume")

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default"), client.MatchingLabels{actions.LabelOrigin: actions.OriginUI}))
	require.Len(t, list.Items, 1)
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"}, list.Items[0].Spec.MediaRef)
	require.True(t, list.Items[0].Spec.GrabBest, "the volume search grabs its best")
	require.NotNil(t, list.Items[0].Spec.Season)
	require.Equal(t, actions.VolumeNumber, *list.Items[0].Spec.Season, "the Comic's one season, its volume")
	require.True(t, list.Items[0].Spec.PlannedBySeries(), "the Comic key plans it")
	require.False(t, list.Items[0].Spec.FansOut(), "it never fans out per issue")

	rec = postForm(t, srv, "/library/default/comic/saga/volume/search", url.Values{"return": {"/library/default/comic/saga"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/default/comic/saga", rec.Header().Get("Location"))

	require.Equal(t, http.StatusNotFound, postForm(t, srv, "/library/default/comic/nope/volume/search", url.Values{}, true).Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default"), client.MatchingLabels{actions.LabelOrigin: actions.OriginUI}))
	require.Len(t, list.Items, 2, "nothing is created for an unknown comic")
}

// The action bar's "Interactive" volume search -- the one route with the
// form's "interactive" field -- creates the same Search without grabBest
// (actions.InteractiveVolumeSearch) and opens the volume's modal in
// #issue-modal for htmx, its panel polling the shared /searches route with
// every release listed; a form post redirects to the panel. The badge
// names the planned Search its state serves.
func TestComicInteractiveVolumeSearchOpensTheVolumeModal(t *testing.T) {
	srv, c := comicFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/library/default/comic/saga/volume/search",
		strings.NewReader(url.Values{"interactive": {"true"}, "return": {"/library/default/comic/saga"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "issue-modal")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `data-volume-search-modal="saga"`)
	require.Contains(t, body, "Saga - Volume")
	require.NotContains(t, body, `data-season-search-modal`, "the volume's own modal, not a season's")

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default"), client.MatchingLabels{actions.LabelOrigin: actions.OriginUI}))
	require.Len(t, list.Items, 1)
	s := list.Items[0]
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"}, s.Spec.MediaRef)
	require.False(t, s.Spec.GrabBest, "the person picks")
	require.Equal(t, actions.VolumeNumber, *s.Spec.Season)
	require.False(t, s.Spec.PlannedBySeries(), "an interactive volume search is the Search controller's own")
	require.Contains(t, body, `hx-get="/searches/default/`+s.Name+`?filter=all"`, "the panel polls with every release listed")

	rec = postForm(t, srv, "/library/default/comic/saga/volume/search", url.Values{"interactive": {"true"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default"), client.MatchingLabels{actions.LabelOrigin: actions.OriginUI}))
	require.Len(t, list.Items, 2)
	require.Regexp(t, `^/searches/default/saga-[a-z0-9]+\?filter=all$`, rec.Header().Get("Location"))

	require.Equal(t, http.StatusNotFound, postForm(t, srv, "/library/default/comic/nope/volume/search", url.Values{"interactive": {"true"}}, true).Code)
}
