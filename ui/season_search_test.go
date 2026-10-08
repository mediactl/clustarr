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

// seasonSearchFixture is a series with four seasons in each state of the
// Series' season search -- answered, searching, queued, skipped -- and
// three answered Searches: a season search with packs, singles and other
// titles; a season search with no pack; and an episode search with other
// titles. The fake client is the reader and the writer.
func seasonSearchFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	answeredAt := metav1.NewTime(time.Now().Add(-2*time.Hour - time.Minute))
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default"},
		Spec:       catalogv1.SeriesSpec{TvdbID: 393189, QualityProfileRef: "web-1080p", RootFolderRef: "tv"},
		Status: catalogv1.SeriesStatus{
			Seasons: []catalogv1.SeasonStatus{
				{Number: 1, Monitored: true, EpisodeCount: 12},
				{Number: 2, Monitored: true, EpisodeCount: 12},
				{Number: 3, Monitored: true, EpisodeCount: 12},
				{Number: 4, Monitored: false, EpisodeCount: 12},
			},
			SeasonSearch: &catalogv1.SeasonSearchState{
				Request:   catalogv1.SeasonSearchRequest{Name: "andor-x1", UID: "x1"},
				StartedAt: metav1.NewTime(time.Now().Add(-3 * time.Hour)),
				Seasons: []catalogv1.SeasonSearchSeason{
					{Number: 1, Phase: catalogv1.SeasonSearchAnswered, AnsweredAt: &answeredAt, Candidates: 12, Approved: 3},
					{Number: 2, Phase: catalogv1.SeasonSearchSearching},
					{Number: 3, Phase: catalogv1.SeasonSearchQueued},
					{Number: 4, Phase: catalogv1.SeasonSearchSkipped, Reason: catalogv1.SeasonSearchNotMonitored},
				},
			},
		},
	}
	season := func(name string, n int32, otherTitles int32, results ...commonv1.ReleaseDecision) *catalogv1.Search {
		return &catalogv1.Search{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "andor"}, Season: &n},
			Status: catalogv1.SearchStatus{
				Phase: catalogv1.SearchPhaseCompleted, OtherTitles: otherTitles, Results: results,
				IndexerOutcomes: []catalogv1.IndexerOutcome{{Name: "nzbgeek", State: catalogv1.IndexerOutcomeOK, Count: int32(len(results))}},
			},
		}
	}
	pack := func(r commonv1.ReleaseDecision) commonv1.ReleaseDecision { r.CoversSeason = true; return r }
	typedPack := func(r commonv1.ReleaseDecision) commonv1.ReleaseDecision {
		r.ReleaseType = commonv1.ReleaseTypeSeasonPack
		return r
	}
	withPacks := season("andor-s1-abc12", 1, 1,
		pack(release("guid-pack", 1, "Andor.S01.1080p.WEB-DL-NTb")),
		release("guid-single", 2, "Andor.S01E03.1080p.WEB-DL-NTb"),
		typedPack(release("guid-rejected-pack", 3, "Andor.S01.720p.HDTV-LOL",
			commonv1.Rejection{Reason: "Quality: HDTV-720p is not wanted in profile", Type: commonv1.RejectionPermanent})),
		pack(release("guid-other-pack", 4, "Andor.and.Friends.S01.1080p.WEB-DL")),
	)
	noPacks := season("andor-s2-def34", 2, 0,
		release("guid-e1", 1, "Andor.S02E01.1080p.WEB-DL-NTb"),
		release("guid-e2", 2, "Andor.S02E02.1080p.WEB-DL-NTb"),
	)
	episode := &catalogv1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e02-ghi56", Namespace: "default"},
		Spec:       catalogv1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e02"}},
		Status: catalogv1.SearchStatus{
			Phase: catalogv1.SearchPhaseCompleted, OtherTitles: 2,
			IndexerOutcomes: []catalogv1.IndexerOutcome{{Name: "nzbgeek", State: catalogv1.IndexerOutcomeOK, Count: 4}},
			Results: []commonv1.ReleaseDecision{
				release("guid-other-1", 3, "Andorra.S01E02.1080p.WEB-DL"),
				release("guid-mine", 1, "Andor.S01E02.1080p.WEB-DL-NTb"),
				release("guid-mine-rejected", 2, "Andor.S01E02.720p.HDTV-LOL",
					commonv1.Rejection{Reason: "Quality: HDTV-720p is not wanted in profile", Type: commonv1.RejectionPermanent}),
				release("guid-other-2", 4, "The.Andor.Files.S01E02.1080p.WEB-DL"),
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(series, withPacks, noPacks, episode).
		WithIndex(&catalogv1.Episode{}, ui.EpisodeSeriesRefField, ui.IndexEpisodeBySeriesRef).Build()
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

// uiSeasonSearches lists the Searches the ui created for the series.
func uiSeasonSearches(t *testing.T, c client.Client) []catalogv1.Search {
	t.Helper()
	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default"),
		client.MatchingLabels{actions.LabelOrigin: actions.OriginUI}))
	return list.Items
}

// TestTheSeasonButtonsPostToTheirRoutes: each season header carries
// Sonarr's two season searches (S10). The magnifier posts to
// .../seasons/{n}/search and answers #action-status in place, creating a
// Search with grabBest and spec.season; the person posts to
// .../search/interactive and answers the season's modal on a polling
// panel listing season packs, its Search grabbing nothing -- a form post
// lands on the panel's page. A season that is not a number, and a series
// the library does not hold, are not found and create nothing.
func TestTheSeasonButtonsPostToTheirRoutes(t *testing.T) {
	srv, c := seasonSearchFixture(t)
	page := getPath(t, srv, "/library/default/series/andor", false).Body.String()
	requireTag(t, page, `data-action="season-search"`)
	require.Contains(t, page, `hx-post="/library/default/series/andor/seasons/1/search" hx-target="#action-status"`)
	require.Contains(t, page, `hx-post="/library/default/series/andor/seasons/1/search/interactive" hx-target="#episode-modal"`)
	requireTag(t, page, `data-action="season-interactive-search"`)

	req := httptest.NewRequest(http.MethodPost, "/library/default/series/andor/seasons/1/search", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "action-status")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-status="ok"`)
	require.Contains(t, rec.Body.String(), "Searching season 1")
	got := uiSeasonSearches(t, c)
	require.Len(t, got, 1)
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "andor"}, got[0].Spec.MediaRef)
	require.NotNil(t, got[0].Spec.Season)
	require.Equal(t, int32(1), *got[0].Spec.Season)
	require.True(t, got[0].Spec.GrabBest, "the magnifier grabs the best for each wanted episode")

	rec = postForm(t, srv, "/library/default/series/andor/seasons/2/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	modal := rec.Body.String()
	var interactive *catalogv1.Search
	searches := uiSeasonSearches(t, c)
	for i := range searches {
		if !searches[i].Spec.GrabBest {
			interactive = &searches[i]
		}
	}
	require.NotNil(t, interactive)
	require.Equal(t, int32(2), *interactive.Spec.Season)
	require.Contains(t, modal, `data-season-search-modal="2"`)
	requireTag(t, modal, `data-search-panel="`+interactive.Name+`"`, `data-search-filter="packs"`,
		`hx-get="/searches/default/`+interactive.Name+`?filter=packs"`, `hx-trigger="load delay:2s"`)

	rec = postForm(t, srv, "/library/default/series/andor/seasons/2/search/interactive", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	loc := rec.Header().Get("Location")
	require.True(t, strings.HasPrefix(loc, "/searches/default/andor-") && strings.HasSuffix(loc, "?filter=packs"), loc)

	before := len(uiSeasonSearches(t, c))
	for _, path := range []string{
		"/library/default/series/andor/seasons/x/search",
		"/library/default/series/andor/seasons/-1/search",
		"/library/default/series/andor/seasons/x/search/interactive",
		"/library/default/series/nope/seasons/1/search/interactive",
	} {
		require.Equal(t, http.StatusNotFound, postForm(t, srv, path, url.Values{}, true).Code, path)
	}
	require.Len(t, uiSeasonSearches(t, c), before, "nothing is created for a bad season or an unknown series")
}

// TestThePanelCollapsesOtherTitles: every results panel lists the item's
// approved and rejected releases, then the last status.otherTitles
// results -- releases for other titles -- collapsed under "N releases for
// other titles", each keeping its download button (S10, §3.4).
func TestThePanelCollapsesOtherTitles(t *testing.T) {
	srv, _ := seasonSearchFixture(t)
	panel := getPath(t, srv, "/searches/default/andor-s01e02-ghi56", true).Body.String()
	require.Contains(t, panel, "2 results (and 2 for other titles) from 1 indexers")

	band := strings.Index(panel, `data-other-titles="2"`)
	require.Positive(t, band, "the other titles are collapsed in one band")
	require.Contains(t, panel[band:], "2 releases for other titles")
	at := func(guid string) int { return strings.Index(panel, `data-release="`+guid+`"`) }
	for _, guid := range []string{"guid-mine", "guid-mine-rejected"} {
		require.Less(t, at(guid), band, "%s is the item's own, listed above the band", guid)
		requireTag(t, panel, `data-release="`+guid+`"`, `data-other-title="false"`)
	}
	for _, guid := range []string{"guid-other-1", "guid-other-2"} {
		require.Greater(t, at(guid), band, "%s is another title's, inside the band", guid)
		requireTag(t, panel, `data-release="`+guid+`"`, `data-other-title="true"`)
	}
	require.Less(t, at("guid-mine"), at("guid-mine-rejected"), "approved, then rejected, by rank")
	require.Less(t, at("guid-other-1"), at("guid-other-2"))
	require.Equal(t, 4, strings.Count(panel, `name="guid"`), "every row keeps its download button")
}

// TestTheSeasonPanelDefaultsToPacks: a season search's panel lists season
// packs -- coversSeason, or a seasonPack release type -- by default, with a
// filter to show all; under all, every release of the item is listed and
// the other titles are collapsed below (S10).
func TestTheSeasonPanelDefaultsToPacks(t *testing.T) {
	srv, _ := seasonSearchFixture(t)
	panel := getPath(t, srv, "/searches/default/andor-s1-abc12", true).Body.String()
	requireTag(t, panel, `data-search-panel="andor-s1-abc12"`, `data-search-filter="packs"`)
	require.Contains(t, panel, `data-season-filter="packs"`)
	require.Contains(t, panel, "Season packs (2)", "the pack and the rejected pack; the other title's pack is not the item's")
	require.Contains(t, panel, "All (4)")
	require.Contains(t, panel, `data-release="guid-pack"`)
	require.Contains(t, panel, `data-release="guid-rejected-pack"`, "a seasonPack release type is a pack")
	require.NotContains(t, panel, `data-release="guid-single"`, "a single episode is not a pack")
	require.NotContains(t, panel, `data-other-titles=`, "the packs view lists the item's packs alone")
	require.Contains(t, panel, `hx-get="/searches/default/andor-s1-abc12?filter=all"`)

	all := getPath(t, srv, "/searches/default/andor-s1-abc12?filter=all", true).Body.String()
	requireTag(t, all, `data-search-panel="andor-s1-abc12"`, `data-search-filter="all"`)
	for _, guid := range []string{"guid-pack", "guid-single", "guid-rejected-pack"} {
		require.Contains(t, all, `data-release="`+guid+`"`)
	}
	band := strings.Index(all, `data-other-titles="1"`)
	require.Positive(t, band)
	require.Greater(t, strings.Index(all, `data-release="guid-other-pack"`), band)

	episode := getPath(t, srv, "/searches/default/andor-s01e02-ghi56?filter=packs", true).Body.String()
	requireTag(t, episode, `data-search-panel="andor-s01e02-ghi56"`, `data-search-filter=""`)
	require.Contains(t, episode, `data-release="guid-mine"`, "an episode search has no packs filter")
}

// TestTheSeasonPanelOffersAllWhenNoPackExists: a season search with no
// pack says so under the packs filter and links to all its releases.
func TestTheSeasonPanelOffersAllWhenNoPackExists(t *testing.T) {
	srv, _ := seasonSearchFixture(t)
	panel := getPath(t, srv, "/searches/default/andor-s2-def34", true).Body.String()
	requireTag(t, panel, `data-search-panel="andor-s2-def34"`, `data-search-filter="packs"`)
	none := panel[strings.Index(panel, "data-search-no-packs"):]
	require.Contains(t, none, "No season packs.")
	require.Contains(t, none, "Show all 2 releases")
	requireTag(t, none, `data-action="search-show-all"`, `hx-get="/searches/default/andor-s2-def34?filter=all"`)
	require.NotContains(t, panel, `data-release="guid-e1"`)

	all := getPath(t, srv, "/searches/default/andor-s2-def34?filter=all", true).Body.String()
	require.NotContains(t, all, "No season packs.")
	require.Contains(t, all, `data-release="guid-e1"`)
	require.Contains(t, all, `data-release="guid-e2"`)
}

// TestTheSeasonBadgeReadsTheSeriesState: each season header shows the
// Series' season search (status.seasonSearch) as a badge -- Queued,
// Searching, or "Searched: N approved" with its candidates and age -- and
// none for a season the search skipped (S10).
func TestTheSeasonBadgeReadsTheSeriesState(t *testing.T) {
	srv, _ := seasonSearchFixture(t)
	page := getPath(t, srv, "/library/default/series/andor", false).Body.String()
	header := func(n string) string {
		t.Helper()
		at := strings.Index(page, `data-season="`+n+`"`)
		require.GreaterOrEqual(t, at, 0, "no header for season %s", n)
		rest := page[at+1:]
		if end := strings.Index(rest, `data-season="`); end >= 0 {
			rest = rest[:end]
		}
		return rest
	}
	answered := header("1")
	requireTag(t, answered, `data-season-search="Answered"`, `title="12 releases, 3 approved, 2 hours ago"`)
	require.Contains(t, answered, "Searched: 3 approved")
	requireTag(t, header("2"), `data-season-search="Searching"`)
	require.Contains(t, header("2"), ">Searching<")
	requireTag(t, header("3"), `data-season-search="Queued"`)
	require.Contains(t, header("3"), ">Queued<")
	require.NotContains(t, header("4"), "data-season-search=", "a skipped season shows no badge")
}
