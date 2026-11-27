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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
)

func hubsServer(t *testing.T, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).
		WithStatusSubresource(&catalogv1.PlexCollection{}, &catalogv1.PlexHubLayout{}).Build()
	return ui.NewServer(t.Context(), ui.Options{Reader: c, Actions: actions.New(c), Namespace: pcolNS}), c
}

func moviesHubs() []catalogv1.PlexHubStatus {
	return []catalogv1.PlexHubStatus{
		{Identifier: "movie.recentlyreleased", Title: "Recently Released Movies", Kind: catalogv1.PlexHubBuiltin, LibraryRecommended: true, Home: true},
		{Identifier: "movie.genre", Title: "Top Movies in (Genre)", Kind: catalogv1.PlexHubBuiltin, LibraryRecommended: true},
		{Identifier: "custom.collection.1.77", Title: "UP", Kind: catalogv1.PlexHubCollection, Collection: "up", RatingKey: "77", LibraryRecommended: true},
		{Identifier: "custom.collection.1.78", Title: "A24", Kind: catalogv1.PlexHubCollection, RatingKey: "78", FriendsHome: true},
	}
}

func moviesLayout(spec []catalogv1.PlexHubEntry) *catalogv1.PlexHubLayout {
	return &catalogv1.PlexHubLayout{
		ObjectMeta: metav1.ObjectMeta{Namespace: pcolNS, Name: "clustarr-movies", Generation: 1},
		Spec:       catalogv1.PlexHubLayoutSpec{Library: "Clustarr Movies", Hubs: spec},
		Status: catalogv1.PlexHubLayoutStatus{ObservedGeneration: 1, Hubs: moviesHubs(), Conditions: []metav1.Condition{{
			Type: catalogv1.PlexHubLayoutConditionReady, Status: metav1.ConditionTrue, Reason: "Synced", LastTransitionTime: metav1.Now(),
		}}},
	}
}

func getHubLayout(t *testing.T, c client.Client, name string) catalogv1.PlexHubLayout {
	t.Helper()
	var l catalogv1.PlexHubLayout
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: name}, &l))
	return l
}

func hubKeys(hubs []catalogv1.PlexHubEntry) []string {
	out := make([]string, 0, len(hubs))
	for _, h := range hubs {
		out = append(out, actions.HubRefOf(h).Key())
	}
	return out
}

func TestHubsPageListsEachLibrarysHubsInOrder(t *testing.T) {
	layout := moviesLayout([]catalogv1.PlexHubEntry{{CollectionTitle: "A24", Home: true}})
	ghibli := plexCollection("ghibli", refGhibli())
	srv, _ := hubsServer(t, layout, plexCollection("up", refUP()), ghibli, plexCollection("netflix", refNetflix()))

	rec := get(t, srv, "/library/hubs")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	movies := body[strings.Index(body, `data-hub-library="Clustarr Movies"`):strings.Index(body, `data-hub-library="Clustarr TV"`)]
	order := []string{`data-hub="title:A24"`, `data-hub="id:movie.recentlyreleased"`, `data-hub="id:movie.genre"`, `data-hub="col:up"`}
	at := -1
	for _, want := range order {
		i := strings.Index(movies, want)
		require.Greater(t, i, at, "%s out of order", want)
		at = i
	}
	for _, want := range []string{
		"Recently Released Movies", "Top Movies in (Genre)", "Library Recommended", "Friends&#39; Home",
		"Reset customization", "Add collection", "Studio Ghibli Essentials", `hx-post="/library/hubs/clustarr-system/clustarr-movies/edit"`,
	} {
		assert.Contains(t, movies, want)
	}
	assert.Equal(t, 1, strings.Count(movies, `data-hub-listed="true"`), "A24 alone is listed")
	assert.NotContains(t, movies, `data-hub-not-ready`)

	// A library the Collections page knows and no layout holds.
	tv := body[strings.Index(body, `data-hub-library="Clustarr TV"`):]
	assert.Contains(t, tv, `data-hub-unmanaged`)
	assert.Contains(t, tv, `action="/library/hubs/clustarr-system/clustarr-tv/edit"`)
	assert.NotContains(t, tv, "Reset customization")

	// The Library sub-menu names the page.
	assert.Contains(t, body, `href="/library/hubs"`)
}

func TestHubsPageSaysWhyALayoutIsNotReady(t *testing.T) {
	layout := moviesLayout(nil)
	layout.Status.Conditions[0].Status = metav1.ConditionFalse
	layout.Status.Conditions[0].Message = `no Plex library is titled "Clustarr Movies"`
	layout.Annotations = map[string]string{catalogv1.AnnotationPlexHubsReset: "2026-11-16T20:00:00Z"}
	srv, _ := hubsServer(t, layout)
	body := get(t, srv, "/library/hubs").Body.String()
	assert.Contains(t, body, `data-hub-not-ready`)
	assert.Contains(t, body, "no Plex library is titled")
	assert.Contains(t, body, `data-hub-reset="pending"`)
	assert.Contains(t, body, `hx-get="/library/hubs/clustarr-system/clustarr-movies/section"`, "it asks again while the reset is pending")

	rec := get(t, srv, "/library/hubs/clustarr-system/clustarr-movies/section")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `data-hub-library="Clustarr Movies"`)
	assert.Equal(t, http.StatusNotFound, get(t, srv, "/library/hubs/clustarr-system/nope/section").Code)
}

func TestAHubSwitchWritesTheLayoutAndRedrawsTheRows(t *testing.T) {
	srv, c := hubsServer(t, moviesLayout(nil), plexCollection("up", refUP()))
	rec := postHTMX(t, srv, "/library/hubs/clustarr-system/clustarr-movies/edit", url.Values{
		"library": {"Clustarr Movies"}, "op": {"flag"}, "hub": {"id:movie.genre"}, "flag": {"home"}, "on": {"true"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, `data-action-status="ok"`)
	assert.Contains(t, body, `id="hub-rows-clustarr-system-clustarr-movies"`)
	assert.Contains(t, body, `hx-swap-oob="outerHTML"`)
	assert.Equal(t, 2, strings.Count(body, `data-hub-listed="true"`), "the answer shows the layout as written, not the cache")

	got := getHubLayout(t, c, "clustarr-movies")
	assert.Equal(t, []catalogv1.PlexHubEntry{
		{Identifier: "movie.recentlyreleased", LibraryRecommended: true, Home: true},
		{Identifier: "movie.genre", LibraryRecommended: true, Home: true},
	}, got.Spec.Hubs)
	assert.Equal(t, moviesHubs(), got.Status.Hubs, "status is never written")
}

func TestHubMoveAndRemove(t *testing.T) {
	srv, c := hubsServer(t, moviesLayout(nil))
	path := "/library/hubs/clustarr-system/clustarr-movies/edit"
	rec := postHTMX(t, srv, path, url.Values{"library": {"Clustarr Movies"}, "op": {"move"}, "hub": {"col:up"}, "to": {"0"}})
	require.Contains(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, []string{"col:up", "id:movie.recentlyreleased", "id:movie.genre"}, hubKeys(getHubLayout(t, c, "clustarr-movies").Spec.Hubs))

	rec = postHTMX(t, srv, path, url.Values{"library": {"Clustarr Movies"}, "op": {"remove"}, "hub": {"id:movie.genre"}})
	require.Contains(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, []string{"col:up", "id:movie.recentlyreleased"}, hubKeys(getHubLayout(t, c, "clustarr-movies").Spec.Hubs))

	for name, bad := range map[string]url.Values{
		"no op":        {"hub": {"col:up"}},
		"no hub":       {"op": {"move"}, "to": {"1"}},
		"no position":  {"op": {"move"}, "hub": {"col:up"}},
		"no on":        {"op": {"flag"}, "hub": {"col:up"}, "flag": {"home"}},
		"unknown hub":  {"op": {"remove"}, "hub": {"id:movie.nope"}},
		"unknown flag": {"op": {"flag"}, "hub": {"col:up"}, "flag": {"everywhere"}, "on": {"true"}},
	} {
		rec = postHTMX(t, srv, path, bad)
		assert.Contains(t, rec.Body.String(), `data-action-error="invalid"`, name)
	}
	assert.Equal(t, []string{"col:up", "id:movie.recentlyreleased"}, hubKeys(getHubLayout(t, c, "clustarr-movies").Spec.Hubs), "a refused edit writes nothing")
}

func TestAddingACollectionHubAndManagingALibraryAreFormPosts(t *testing.T) {
	srv, c := hubsServer(t, moviesLayout(nil), plexCollection("ghibli", refGhibli()), plexCollection("netflix", refNetflix()))
	rec := post(t, srv, "/library/hubs/clustarr-system/clustarr-movies/edit", url.Values{
		"library": {"Clustarr Movies"}, "op": {"add"}, "collection": {"ghibli"}, "return": {"/library/hubs"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, "/library/hubs", rec.Header().Get("Location"))
	hubs := getHubLayout(t, c, "clustarr-movies").Spec.Hubs
	require.Len(t, hubs, 5)
	assert.Equal(t, catalogv1.PlexHubEntry{Collection: "ghibli", LibraryRecommended: true}, hubs[4])

	// "Manage hubs" on a library no layout holds creates the empty layout.
	rec = post(t, srv, "/library/hubs/clustarr-system/clustarr-tv/edit", url.Values{
		"library": {"Clustarr TV"}, "op": {"ensure"}, "return": {"/library/hubs"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	tv := getHubLayout(t, c, "clustarr-tv")
	assert.Equal(t, catalogv1.PlexHubLayoutSpec{Library: "Clustarr TV"}, tv.Spec)

	body := get(t, srv, "/library/hubs").Body.String()
	assert.Contains(t, body, `data-hub-waiting`, "the new layout waits for cluster-plex's report")
}

func TestHubResetSetsTheAnnotationAndClearsTheHubs(t *testing.T) {
	srv, c := hubsServer(t, moviesLayout([]catalogv1.PlexHubEntry{{Identifier: "movie.genre"}}))
	at := time.Date(2026, 11, 16, 21, 0, 0, 0, time.UTC)
	ui.SetNowForTest(srv, func() time.Time { return at })
	rec := post(t, srv, "/library/hubs/clustarr-system/clustarr-movies/reset", url.Values{"library": {"Clustarr Movies"}, "return": {"/library/hubs"}})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	got := getHubLayout(t, c, "clustarr-movies")
	assert.Equal(t, "2026-11-16T21:00:00Z", got.Annotations[catalogv1.AnnotationPlexHubsReset])
	assert.Empty(t, got.Spec.Hubs)
}

func TestCollectionsListCarriesEachCollectionsHubSwitches(t *testing.T) {
	// UP is a hub Plex reports on Library Recommended; Netflix's library
	// has no layout, so its own spec.visibility shows.
	netflix := refNetflix()
	netflix.Visibility = &catalogv1.PlexVisibility{Home: true}
	srv, c := hubsServer(t, moviesLayout(nil), plexCollection("up", refUP()), plexCollection("netflix", netflix))

	body := get(t, srv, "/library/collections").Body.String()
	up := body[strings.Index(body, `id="collection-hub-clustarr-system-up"`):]
	up = up[:strings.Index(up, `</div>`)]
	assert.Contains(t, up, `hx-post="/library/hubs/clustarr-system/clustarr-movies/edit"`)
	assert.Equal(t, 1, strings.Count(up, `aria-checked="true"`), "Library Recommended, from Plex's report")
	nf := body[strings.Index(body, `id="collection-hub-clustarr-system-netflix"`):]
	nf = nf[:strings.Index(nf, `</div>`)]
	assert.Contains(t, nf, `hx-post="/library/hubs/clustarr-system/clustarr-tv/edit"`, "the layout its first write creates")
	assert.Equal(t, 1, strings.Count(nf, `aria-checked="true"`), "Home, from its spec.visibility")

	// A switch writes the library's layout and is answered with the
	// collection's switches as written.
	rec := postHTMX(t, srv, "/library/hubs/clustarr-system/clustarr-tv/edit", url.Values{
		"library": {"Clustarr TV"}, "op": {"flag"}, "hub": {"col:netflix"}, "flag": {"friendsHome"}, "on": {"true"}, "view": {"collection"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	answer := rec.Body.String()
	assert.Contains(t, answer, `data-action-status="ok"`)
	assert.Contains(t, answer, `id="collection-hub-clustarr-system-netflix"`)
	assert.Contains(t, answer, `hx-swap-oob="outerHTML"`)
	assert.NotContains(t, answer, `data-hub-rows`)
	assert.Equal(t, []catalogv1.PlexHubEntry{{Collection: "netflix", FriendsHome: true}}, getHubLayout(t, c, "clustarr-tv").Spec.Hubs)

	rec = postHTMX(t, srv, "/library/hubs/clustarr-system/clustarr-movies/edit", url.Values{
		"library": {"Clustarr Movies"}, "op": {"flag"}, "hub": {"col:up"}, "flag": {"home"}, "on": {"true"}, "view": {"collection"},
	})
	require.Contains(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, []catalogv1.PlexHubEntry{
		{Identifier: "movie.recentlyreleased", LibraryRecommended: true, Home: true},
		{Identifier: "movie.genre", LibraryRecommended: true},
		{Collection: "up", LibraryRecommended: true, Home: true},
	}, getHubLayout(t, c, "clustarr-movies").Spec.Hubs, "the hubs ahead of it keep their place and flags")
}

func TestTheCollectionEditorSaysTheHubSwitchesTakePrecedence(t *testing.T) {
	srv, _ := hubsServer(t, plexCollection("up", refUP()))
	body := get(t, srv, "/library/collections/clustarr-system/up/edit").Body.String()
	assert.Contains(t, body, `data-editor-visibility-note`)
}
