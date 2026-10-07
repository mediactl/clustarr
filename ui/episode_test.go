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
	"fmt"
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

// episodeFixture is a series with season 1's episodes in every state the
// status cell distinguishes -- on disk below the cutoff, downloading,
// missing, unmonitored, not aired, no air date -- one episode's file, and
// two Searches: one answered with ranked results and a grab already
// reported, one still running. The fake client is the reader and the
// writer.
func episodeFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	aired := metav1.NewTime(time.Date(2022, time.September, 21, 0, 0, 0, 0, time.UTC))
	future := metav1.NewTime(time.Now().AddDate(1, 0, 0))
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default"},
		Spec:       catalogv1.SeriesSpec{TvdbID: 393189, QualityProfileRef: "web-1080p", RootFolderRef: "tv"},
		Status: catalogv1.SeriesStatus{
			Path:    "/data/tv/Andor (2022)",
			Seasons: []catalogv1.SeasonStatus{{Number: 1, Monitored: true, EpisodeCount: 6, EpisodeFileCount: 1, SizeBytes: 3 << 30}},
		},
	}
	episode := func(number int32, title string, status catalogv1.EpisodeStatus) *catalogv1.Episode {
		status.Title = title
		return &catalogv1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("andor-s01e%02d", number), Namespace: "default"},
			Spec:       catalogv1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: number, Monitored: new(true)},
			Status:     status,
		}
	}
	unmonitored := episode(4, "Aldhani", catalogv1.EpisodeStatus{AirDate: &aired, Phase: catalogv1.EpisodePhaseUnmonitored})
	unmonitored.Spec.Monitored = new(false)
	file := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e01-file", Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			Path: "/data/tv/Andor (2022)/Season 01/Andor - S01E01 - Kassa.mkv", SizeBytes: 2 << 30,
			Quality: commonv1.Quality{Name: "WEBDL-1080p"}, FormatScore: 25, MatchedFormats: []string{"AMZN"},
		},
	}
	answered := &catalogv1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e02-abc12", Namespace: "default"},
		Spec: catalogv1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e02"},
			Grab:     []string{"guid-grabbed"},
		},
		Status: catalogv1.SearchStatus{
			Phase: catalogv1.SearchPhaseCompleted,
			IndexerOutcomes: []catalogv1.IndexerOutcome{
				{Name: "nzbgeek", State: catalogv1.IndexerOutcomeOK, Count: 3},
				{Name: "tracker", State: catalogv1.IndexerOutcomeTimeout},
			},
			Results: []commonv1.ReleaseDecision{
				release("guid-rejected", 3, "Andor.S01E02.720p.HDTV-LOL", commonv1.Rejection{Reason: "Quality: HDTV-720p is not wanted in profile", Type: commonv1.RejectionPermanent}),
				release("guid-best", 1, "Andor.S01E02.1080p.AMZN.WEB-DL-NTb"),
				release("guid-grabbed", 2, "Andor.S01E02.1080p.WEB.h264-GRP"),
				release("guid-queued", 4, "Andor.S01E02.2160p.WEB-DL-FLUX", commonv1.Rejection{Reason: "Queue: an equal release is already queued", Type: commonv1.RejectionTemporary}),
			},
			Grabbed: []catalogv1.GrabResult{{GUID: "guid-grabbed", DownloadRef: "andor-s01e02-dl"}},
		},
	}
	running := &catalogv1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e03-def34", Namespace: "default"},
		Spec:       catalogv1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e03"}},
		Status:     catalogv1.SearchStatus{Phase: catalogv1.SearchPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(series, file, answered, running,
		episode(1, "Kassa", catalogv1.EpisodeStatus{
			AirDate: &aired, HasFile: true, FileRef: new("andor-s01e01-file"),
			FileQuality: &commonv1.Quality{Name: "WEBDL-1080p"}, Phase: catalogv1.EpisodePhaseCutoffUnmet,
			Overview: "Cassian Andor searches for his sister.", RuntimeMinutes: 39,
		}),
		episode(2, "That Would Be Me", catalogv1.EpisodeStatus{AirDate: &aired, Phase: catalogv1.EpisodePhaseDownloading, ActiveDownloadRef: new("dl"), FinaleType: "midseason"}),
		episode(3, "Reckoning", catalogv1.EpisodeStatus{AirDate: &aired, Phase: catalogv1.EpisodePhaseWanted}),
		unmonitored,
		episode(5, "The Axe Forgets", catalogv1.EpisodeStatus{AirDate: &future, Phase: catalogv1.EpisodePhaseUnaired}),
		episode(6, "", catalogv1.EpisodeStatus{}),
	).WithIndex(&catalogv1.Episode{}, ui.EpisodeSeriesRefField, ui.IndexEpisodeBySeriesRef).Build()
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "andor"}, Kind: commonv1.MediaKindSeries,
		Tab: projection.TabTV, Title: "Andor", Year: 2022, QualityProfileRef: "web-1080p", Monitored: true,
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Actions: actions.New(c),
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})
	return srv, c
}

func release(guid string, rank int32, title string, rejections ...commonv1.Rejection) commonv1.ReleaseDecision {
	published := metav1.NewTime(time.Now().Add(-50 * time.Hour))
	return commonv1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{
			GUID: guid, Title: title, IndexerName: "nzbgeek", Protocol: commonv1.ProtocolUsenet,
			SizeBytes: 1 << 30, PublishedAt: &published, Quality: commonv1.Quality{Name: "WEBDL-1080p"},
			Languages: []string{"en"}, FormatScore: 25,
		},
		Approved: len(rejections) == 0, Rejections: rejections, Rank: rank,
	}
}

func getPath(t *testing.T, srv *ui.Server, path string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// Each row is Sonarr's: the status cell says, in Sonarr's order, what the
// episode is waiting on, and every row has the automatic and interactive
// searches and a title that opens the episode's details.
func TestEpisodeRowsAreSonarrs(t *testing.T) {
	srv, _ := episodeFixture(t)
	rec := getPath(t, srv, "/library/default/series/andor/seasons/1", true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	for number, status := range map[string]string{
		"1": "file", "2": "downloading", "3": "missing", "4": "unmonitored", "5": "unaired", "6": "tba",
	} {
		requireTag(t, body, `data-episode="`+number+`"`, `data-status="`+status+`"`)
	}
	require.Regexp(t, `class="[^"]*bg-amber-500/20[^"]*"[^>]*title="Quality cutoff has not been met"[^>]*>WEBDL-1080p<`, body,
		"a file below the cutoff is an amber quality badge")
	require.Contains(t, body, `data-finale="midseason"`)
	require.Contains(t, body, ">Midseason Finale<")
	require.Contains(t, body, `<time datetime="2022-09-21" title="2022-09-21">Sep 21 2022</time>`)
	require.Contains(t, body, `hx-post="/library/default/episode/andor-s01e03/search" hx-target="#action-status"`)
	require.Contains(t, body, `hx-post="/library/default/episode/andor-s01e03/search/interactive" hx-target="#episode-modal"`)
	require.Contains(t, body, `hx-get="/library/default/episode/andor-s01e03/details" hx-target="#episode-modal"`)
	require.Contains(t, body, `name="return" value="/library/default/series/andor/seasons/1"`)
	require.Regexp(t, `data-episode-title[^>]*>\s*TBA`, body, "an untitled episode reads TBA")

	page := getPath(t, srv, "/library/default/series/andor", false).Body.String()
	require.Contains(t, page, `id="episode-modal"`)
	require.Contains(t, page, `id="action-status"`)
	requireTag(t, page, `data-season-count="1 / 6"`, `bg-red-500/20`)
	require.Contains(t, page, "3.00 GiB")
}

// An episode's automatic search answers the page's #action-status in
// place, as a card's does.
func TestEpisodeAutomaticSearchAnswersInPlace(t *testing.T) {
	srv, c := episodeFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/library/default/episode/andor-s01e03/search", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "action-status")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-status="ok"`)

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	var got []catalogv1.Search
	for _, s := range list.Items {
		if s.Spec.MediaRef.Name == "andor-s01e03" && s.Spec.GrabBest {
			got = append(got, s)
		}
	}
	require.Len(t, got, 1, "one automatic search, grabbing its best")
}

// The title opens Sonarr's details modal for htmx and a page for anyone
// else: the heading, the overview and the file, Details open.
func TestEpisodeDetailsServesTheModalToHTMXAndAPageOtherwise(t *testing.T) {
	srv, _ := episodeFixture(t)
	rec := getPath(t, srv, "/library/default/episode/andor-s01e01/details", true)
	require.Equal(t, http.StatusOK, rec.Code)
	modal := rec.Body.String()
	require.NotContains(t, modal, "<html")
	require.Contains(t, modal, `data-episode-modal="andor-s01e01"`)
	require.Contains(t, modal, "Andor - 1x01 - Kassa")
	require.Contains(t, modal, "Cassian Andor searches for his sister.")
	require.Contains(t, modal, "39m")
	requireTag(t, modal, `data-episode-file="andor-s01e01-file"`)
	require.Contains(t, modal, "Season 01/Andor - S01E01 - Kassa.mkv", "the path relative to the series' folder")
	require.Contains(t, modal, "2.00 GiB")
	require.Contains(t, modal, ">+25<")
	requireTag(t, modal, `data-tab="details"`, `aria-selected="true"`)
	require.Contains(t, modal, "Automatic Search")
	require.Contains(t, modal, "Interactive Search")

	searchTab := getPath(t, srv, "/library/default/episode/andor-s01e03/details?tab=search", true).Body.String()
	requireTag(t, searchTab, `data-tab="search"`, `aria-selected="true"`)
	require.Contains(t, searchTab, `data-episode-file=""`, "no file on disk")

	page := getPath(t, srv, "/library/default/episode/andor-s01e01/details", false)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), `data-episode-page="andor-s01e01"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/episode/nope/details", true).Code)
}

// The row's interactive search creates a Search that grabs nothing and
// answers the modal on its Search tab, the results panel polling. A form
// post lands on the panel's page; an unknown episode creates nothing.
func TestInteractiveSearchOpensTheModalOnAPollingPanel(t *testing.T) {
	srv, c := episodeFixture(t)
	rec := postForm(t, srv, "/library/default/episode/andor-s01e03/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	var created *catalogv1.Search
	for i, s := range list.Items {
		if s.Labels[actions.LabelOrigin] == actions.OriginUI {
			require.Nil(t, created, "one Search")
			created = &list.Items[i]
		}
	}
	require.NotNil(t, created)
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e03"}, created.Spec.MediaRef)
	require.False(t, created.Spec.GrabBest, "an interactive search grabs only what a person picks")

	require.Contains(t, body, `data-episode-modal="andor-s01e03"`)
	requireTag(t, body, `data-tab="search"`, `aria-selected="true"`)
	requireTag(t, body, `data-search-panel="`+created.Name+`"`,
		`hx-get="/searches/default/`+created.Name+`"`, `hx-trigger="load delay:2s"`)
	require.Contains(t, body, "Searching indexers")

	rec = postForm(t, srv, "/library/default/episode/andor-s01e03/search/interactive", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/searches/default/andor-s01e03-"), rec.Header().Get("Location"))

	before := len(list.Items)
	rec = postForm(t, srv, "/library/default/episode/nope/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, before+1, "only the form post's Search; none for an unknown episode")
}

// An answered search's panel lists the releases in rank order with
// Sonarr's columns and verdicts, says how the indexers fared, shows the
// reported grab, asks before a permanently rejected one, and stops
// polling. A running search polls; an expired one says so and stops.
func TestSearchPanelListsTheRankedResultsAndStopsPollingOnceSettled(t *testing.T) {
	srv, _ := episodeFixture(t)
	rec := getPath(t, srv, "/searches/default/andor-s01e02-abc12", true)
	require.Equal(t, http.StatusOK, rec.Code)
	panel := rec.Body.String()
	require.NotContains(t, panel, "<html")
	require.NotContains(t, panel, "hx-trigger", "a settled search stops polling")
	require.Contains(t, panel, "4 results from 2 indexers (1 failed or timed out)")

	at := func(guid string) int { return strings.Index(panel, `data-release="`+guid+`"`) }
	require.Less(t, at("guid-best"), at("guid-grabbed"))
	require.Less(t, at("guid-grabbed"), at("guid-rejected"))
	require.Less(t, at("guid-rejected"), at("guid-queued"))

	requireTag(t, panel, `data-release="guid-best"`, `data-approved="true"`, `data-grab=""`)
	requireTag(t, panel, `data-release="guid-grabbed"`, `data-grab="grabbed"`)
	requireTag(t, panel, `data-release="guid-rejected"`, `data-approved="false"`)
	require.Contains(t, panel, `title="Quality: HDTV-720p is not wanted in profile"`)
	require.Contains(t, panel, `title="Queue: an equal release is already queued (temporary)"`)
	require.Contains(t, panel, "hx-confirm=\"This release was rejected:\nQuality: HDTV-720p is not wanted in profile")
	require.Equal(t, 1, strings.Count(panel, "hx-confirm="), "only the permanent rejection asks first")
	require.Contains(t, panel, "2 days")
	require.Contains(t, panel, "1.00 GiB")

	running := getPath(t, srv, "/searches/default/andor-s01e03-def34", true).Body.String()
	requireTag(t, running, `data-search-panel="andor-s01e03-def34"`, `hx-trigger="load delay:2s"`)

	gone := getPath(t, srv, "/searches/default/expired", true).Body.String()
	require.Contains(t, gone, "This search has expired")
	require.NotContains(t, gone, "hx-trigger")

	page := getPath(t, srv, "/searches/default/andor-s01e02-abc12", false)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), "Interactive search: andor-s01e02")
}

// The download button adds the release to spec.grab -- with
// spec.override for a permanently rejected one -- and the panel comes
// back with the grab requested, polling for its outcome. A guid the
// results do not hold is refused in the panel and writes nothing.
func TestGrabReleaseRequestsTheGrabAndPollsForItsOutcome(t *testing.T) {
	srv, c := episodeFixture(t)
	key := types.NamespacedName{Namespace: "default", Name: "andor-s01e02-abc12"}

	rec := postForm(t, srv, "/searches/default/andor-s01e02-abc12/grab", url.Values{"guid": {"guid-best"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	panel := rec.Body.String()
	requireTag(t, panel, `data-release="guid-best"`, `data-grab="requested"`)
	requireTag(t, panel, `data-search-panel="andor-s01e02-abc12"`, `hx-trigger="load delay:2s"`)
	var got catalogv1.Search
	require.NoError(t, c.Get(t.Context(), key, &got))
	require.Equal(t, []string{"guid-grabbed", "guid-best"}, got.Spec.Grab)
	require.False(t, got.Spec.Override, "an approved release needs no override")

	rec = postForm(t, srv, "/searches/default/andor-s01e02-abc12/grab", url.Values{"guid": {"guid-rejected"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, c.Get(t.Context(), key, &got))
	require.Equal(t, []string{"guid-grabbed", "guid-best", "guid-rejected"}, got.Spec.Grab)
	require.True(t, got.Spec.Override, "a permanently rejected release is grabbed with the override")

	rec = postForm(t, srv, "/searches/default/andor-s01e02-abc12/grab", url.Values{"guid": {"guid-unknown"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="invalid"`)
	require.NoError(t, c.Get(t.Context(), key, &got))
	require.Len(t, got.Spec.Grab, 3)

	rec = postForm(t, srv, "/searches/default/andor-s01e02-abc12/grab", url.Values{"guid": {"guid-queued"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/searches/default/andor-s01e02-abc12", rec.Header().Get("Location"))
}
