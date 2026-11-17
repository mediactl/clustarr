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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
)

// rowsBulkFixture is a series with three episodes and an episode of another
// series, over a fake client the actions write through.
func rowsBulkFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	objs := []client.Object{
		&catalogv1.Series{ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default"}, Spec: catalogv1.SeriesSpec{TvdbID: 393189, QualityProfileRef: "web-1080p", RootFolderRef: "tv"}},
	}
	for i := 1; i <= 3; i++ {
		objs = append(objs, &catalogv1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("andor-s01e%02d", i), Namespace: "default"},
			Spec:       catalogv1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: int32(i), Monitored: new(true)},
		})
	}
	objs = append(objs, &catalogv1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "other-s01e01", Namespace: "default"},
		Spec:       catalogv1.EpisodeSpec{SeriesRef: "other", SeasonNumber: 1, EpisodeNumber: 1, Monitored: new(true)},
	})
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(objs...).Build()
	return ui.NewServer(t.Context(), ui.Options{Reader: c, Actions: actions.New(c)}), c
}

// postRowsBulk posts the selection bar's request: htmx, aimed at
// #action-status.
func postRowsBulk(t *testing.T, srv *ui.Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "action-status")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func episodeMonitored(t *testing.T, c client.Client, name string) bool {
	t.Helper()
	var ep catalogv1.Episode
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: name}, &ep))
	return ep.Spec.Monitored == nil || *ep.Spec.Monitored
}

// A season's selected episodes are written through the row's own action,
// and the answer is the toast, each written row out of band from the object
// its write returned, and the event that clears the page's selection.
func TestRowsBulkWritesTheSelectionAndAnswersTheRowsOutOfBand(t *testing.T) {
	srv, c := rowsBulkFixture(t)
	rec := postRowsBulk(t, srv, "/library/default/series/andor/rows/bulk", url.Values{
		"action": {"unmonitor"},
		"item":   {"default/episode/andor-s01e01", "default/episode/andor-s01e03"},
	})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "rows-bulk-done", rec.Header().Get("HX-Trigger-After-Settle"))
	body := rec.Body.String()
	require.Contains(t, body, "2 episodes unmonitored")
	for _, name := range []string{"andor-s01e01", "andor-s01e03"} {
		ref := "default/episode/" + name
		row := requireTag(t, body, `data-row-ref="`+ref+`"`, `data-monitored="false"`)
		require.Contains(t, row, `hx-swap-oob="outerHTML:[data-row-ref=&#39;`+ref+`&#39;]"`, "the row replaces itself wherever it is")
		require.False(t, episodeMonitored(t, c, name), name)
	}
	require.Equal(t, 2, strings.Count(body, "<template>"), "a bare <tr> would be dropped by the parser")
	require.NotContains(t, body, `data-row-ref="default/episode/andor-s01e02"`)
	require.True(t, episodeMonitored(t, c, "andor-s01e02"), "an unselected row is untouched")
}

// Every item is checked against the container before any write: a row of
// another series, another kind or another namespace refuses the whole
// request, and nothing is written.
func TestRowsBulkRefusesARowThatIsNotTheContainersBeforeAnyWrite(t *testing.T) {
	for name, form := range map[string]url.Values{
		"another series' episode": {"action": {"unmonitor"}, "item": {"default/episode/andor-s01e01", "default/episode/other-s01e01"}},
		"another kind":            {"action": {"unmonitor"}, "item": {"default/episode/andor-s01e01", "default/movie/andor-s01e02"}},
		"another namespace":       {"action": {"unmonitor"}, "item": {"default/episode/andor-s01e01", "media/episode/andor-s01e02"}},
		"an unknown action":       {"action": {"delete"}, "item": {"default/episode/andor-s01e01"}},
		"nothing selected":        {"action": {"unmonitor"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv, c := rowsBulkFixture(t)
			rec := postRowsBulk(t, srv, "/library/default/series/andor/rows/bulk", form)
			require.Equal(t, http.StatusOK, rec.Code, "an htmx action is always answered 200")
			require.Empty(t, rec.Header().Get("HX-Trigger-After-Settle"), "a refused request keeps the selection")
			require.Contains(t, rec.Body.String(), `data-action-error="invalid"`)
			for _, ep := range []string{"andor-s01e01", "andor-s01e02", "andor-s01e03", "other-s01e01"} {
				require.True(t, episodeMonitored(t, c, ep), ep)
			}
		})
	}
}

// Only a container whose page lists its children in a table has the route.
func TestRowsBulkIsOnlyForContainers(t *testing.T) {
	srv, _ := rowsBulkFixture(t)
	rec := postRowsBulk(t, srv, "/library/default/movie/heat/rows/bulk", url.Values{"action": {"monitor"}, "item": {"default/episode/andor-s01e01"}})
	require.Equal(t, http.StatusNotFound, rec.Code)
}
