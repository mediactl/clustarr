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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// seriesFixture is one series with two seasons on the cluster -- season 2
// unmonitored by a spec override the status rollup has not caught up with
// -- plus season 1's two episodes and one of season 2's, and the library
// card the projection would build for it.
func seriesFixture(t *testing.T) (*ui.Server, projection.LibraryItem) {
	t.Helper()
	aired := metav1.NewTime(time.Date(2022, time.September, 21, 0, 0, 0, 0, time.UTC))
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default"},
		Spec: catalogv1.SeriesSpec{
			TvdbID: 393189, QualityProfileRef: "web-1080p", RootFolderRef: "tv",
			Seasons: []catalogv1.SeasonSpec{{Number: 2, Monitored: ptr.To(false)}},
		},
		Status: catalogv1.SeriesStatus{
			Metadata: &catalogv1.SeriesMetadata{Title: "Andor", Year: 2022},
			Seasons: []catalogv1.SeasonStatus{
				{Number: 1, Monitored: true, EpisodeCount: 12, EpisodeFileCount: 8},
				{Number: 2, Monitored: true, EpisodeCount: 12, EpisodeFileCount: 0, NextAiring: &aired},
			},
		},
	}
	episode := func(name string, season, number int32, title string, hasFile bool) *catalogv1.Episode {
		ep := &catalogv1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: season, EpisodeNumber: number},
			Status:     catalogv1.EpisodeStatus{Title: title, AirDate: &aired, HasFile: hasFile, Phase: "Imported"},
		}
		if hasFile {
			ep.Status.FileQuality = &commonv1.Quality{Name: "WEBDL-1080p"}
		}
		return ep
	}
	other := &catalogv1.Episode{ // another series' episode, never listed here
		ObjectMeta: metav1.ObjectMeta{Name: "silo-s01e01", Namespace: "default"},
		Spec:       catalogv1.EpisodeSpec{SeriesRef: "silo", SeasonNumber: 1, EpisodeNumber: 1},
	}
	reader := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(series,
		episode("andor-s01e02", 1, 2, "That Would Be Me", true),
		episode("andor-s01e01", 1, 1, "Kassa", true),
		episode("andor-s02e01", 2, 1, "One Year Later", false),
		other,
	).Build()
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "andor"}, Kind: commonv1.MediaKindSeries,
		Tab: projection.TabTV, Title: "Andor", Year: 2022, QualityProfileRef: "web-1080p", Monitored: true,
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  reader,
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})
	return srv, item
}

// A series' page lists one season component per season the rollup knows,
// with its counts and its effective monitored flag (a spec override wins
// over a rollup that has not caught up), each loading its episodes lazily
// on first expand and linking to its own page for a browser without
// JavaScript.
func TestSeriesPageListsSeasonsThatLoadTheirEpisodesLazily(t *testing.T) {
	srv, _ := seriesFixture(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/series/andor", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `data-profile="web-1080p"`)
	require.Contains(t, body, `data-season="1"`)
	require.Contains(t, body, `data-season="2"`)
	require.Contains(t, body, `data-season="1" data-monitored="true" data-episodes="12" data-files="8"`)
	require.Contains(t, body, `data-season="2" data-monitored="false" data-episodes="12" data-files="0"`,
		"the spec override wins over the rollup")
	require.Contains(t, body, `hx-get="/library/default/series/andor/seasons/1"`)
	require.Contains(t, body, `hx-trigger="toggle once"`)
	require.Contains(t, body, `href="/library/default/series/andor/seasons/2"`)
	require.NotContains(t, body, `data-episode=`, "episodes load lazily, not with the page")
}

// The season route serves the episodes component alone to htmx and a full
// page to anyone else, and lists only that season's episodes of that series,
// by number, each with its title, air date, monitored flag, file and
// quality.
func TestSeasonRouteServesAPartialToHTMXAndAPageOtherwise(t *testing.T) {
	srv, _ := seriesFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/library/default/series/andor/seasons/1", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	partial := rec.Body.String()
	require.NotContains(t, partial, "<html", "htmx gets the component, not a page")
	require.Contains(t, partial, `data-episode="1" data-monitored="true" data-hasfile="true" data-quality="WEBDL-1080p" data-phase="Imported"`)
	require.Contains(t, partial, `data-episode="2"`)
	require.Less(t, indexOf(partial, `data-episode="1"`), indexOf(partial, `data-episode="2"`), "episodes in number order")
	require.NotContains(t, partial, `One Year Later`, "season 2's episode is not in season 1")
	require.NotContains(t, partial, `silo`, "another series' episode is never listed")
	require.Contains(t, partial, "Kassa")
	require.Contains(t, partial, "2022-09-21")
	require.Contains(t, partial, `hx-post="/library/default/episode/andor-s01e01/monitor"`)

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/series/andor/seasons/2", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	require.Contains(t, page, "<html")
	require.Contains(t, page, `data-episode="1" data-monitored="true" data-hasfile="false"`)
	require.Contains(t, page, "One Year Later")
	require.NotContains(t, page, "Kassa")

	for _, path := range []string{
		"/library/default/series/nope/seasons/1",
		"/library/default/series/andor/seasons/x",
	} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
