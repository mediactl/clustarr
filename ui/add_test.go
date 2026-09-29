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
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// addServer is a ui over a fake cluster whose library already holds Heat
// (TMDB 949) as media/heat-x.
func addServer(t *testing.T, search ui.MetadataSearch, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	return ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c), Namespace: "media", MetadataSearch: search,
		Library: func(context.Context) []projection.LibraryItem {
			return []projection.LibraryItem{{
				Ref: types.NamespacedName{Namespace: "media", Name: "heat-x"}, Kind: commonv1.MediaKindMovie,
				Tab: projection.TabMovies, Title: "Heat", ProviderID: "949",
			}}
		},
	}), c
}

// hits is a metadata search answering kind with docs, one JSON hit each.
func hits(kind commonv1.MediaKind, docs ...string) ui.MetadataSearch {
	return func(_ context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error) {
		if req.Kind != kind {
			return schema.MetadataResponse{Error: "wrong kind " + string(req.Kind)}, nil
		}
		out := schema.MetadataResponse{Kind: kind}
		for _, d := range docs {
			out.Results = append(out.Results, []byte(d))
		}
		return out, nil
	}
}

func answer(resp schema.MetadataResponse) ui.MetadataSearch {
	return func(context.Context, schema.MetadataRequest) (schema.MetadataResponse, error) { return resp, nil }
}

var (
	moviesRoot = &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "library"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie},
	}
	tvRoot = &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "media"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/tv", Kind: catalogv1.RootFolderKindSeries},
	}
	hdProfile = &catalogv1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd"},
		Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindVideo, Cutoff: "hd",
			Tiers: []catalogv1.Tier{{Name: "hd", Qualities: []string{"WEBDL-1080p"}}},
		},
	}
)

func TestAddSearchRendersHitsAndMarksWhatIsInTheLibrary(t *testing.T) {
	srv, _ := addServer(t, hits(commonv1.MediaKindMovie,
		`{"ids":{"tmdb":"949"},"title":"Heat","year":1995,"poster":"https://image.tmdb.org/t/p/w342/heat.jpg"}`,
		`{"ids":{"tmdb":"1"},"title":"Heat 2","year":2027,"poster":"https://evil.example/x.jpg"}`,
		`{"ids":{"imdb":"tt1"},"title":"No TMDB id"}`,
	), moviesRoot, hdProfile)
	rec := get(t, srv, "/library/movies/add/search?q=heat")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Heat 2")
	require.NotContains(t, body, "No TMDB id", "a hit that cannot key the kind is dropped")
	require.Contains(t, body, `href="/library/media/movie/heat-x"`, "the film in the library links to it")
	require.Contains(t, body, "In library")
	require.Contains(t, body, "/art/search?src=", "an allowed poster goes through the proxy")
	require.NotContains(t, body, "image.tmdb.org/t/p", "never a provider URL in the page")
	require.NotContains(t, body, "evil.example")
	requireThemedComponents(t, "/library/movies/add/search", `<div id="page-body">`+body)
}

func TestAddSearchErrors(t *testing.T) {
	slow := func(ctx context.Context, _ schema.MetadataRequest) (schema.MetadataResponse, error) {
		<-ctx.Done()
		return schema.MetadataResponse{}, ctx.Err()
	}
	for name, tc := range map[string]struct {
		search ui.MetadataSearch
		want   string
	}{
		"not responding":   {slow, "Metadata search is not responding"},
		"catalogarr down": {func(context.Context, schema.MetadataRequest) (schema.MetadataResponse, error) {
			return schema.MetadataResponse{}, fmt.Errorf("natsbus: %q: %w", "rpc.catalogarr.metadata.search", events.ErrNoResponders)
		}, "Metadata search is not responding"},
		"no provider":      {answer(schema.MetadataResponse{Error: "metadata: no movie metadata provider is configured to search"}), `href="/settings"`},
		"provider failure": {answer(schema.MetadataResponse{Error: "metadata: every movie search provider failed: tmdb: rate limited"}), "rate limited"},
		"no hits":          {hits(commonv1.MediaKindMovie), "No matches"},
		"unavailable":      {nil, "Metadata search is not available"},
	} {
		srv, _ := addServer(t, tc.search, moviesRoot, hdProfile)
		ui.SetAddSearchTimeoutForTest(srv, 50*time.Millisecond)
		rec := get(t, srv, "/library/movies/add/search?q=heat")
		require.Contains(t, rec.Body.String(), tc.want, name)
		if tc.want == "Metadata search is not responding" {
			requireTag(t, rec.Body.String(), `data-action="retry-search"`, `hx-get="/library/movies/add/search?q=heat"`)
		}
	}
	srv, _ := addServer(t, hits(commonv1.MediaKindMovie), moviesRoot, hdProfile)
	require.NotContains(t, get(t, srv, "/library/movies/add/search?q=h").Body.String(), "No matches", "one character is not searched")
}

// TestAddCreatesTheItemInTheRootFoldersNamespace: the root folder lives in
// "library", the ui in "media"; the movie must be created beside its root
// folder or its rootFolderRef names nothing.
func TestAddCreatesTheItemInTheRootFoldersNamespace(t *testing.T) {
	srv, c := addServer(t, hits(commonv1.MediaKindMovie), moviesRoot, hdProfile)
	form := url.Values{
		"title": {"Fight Club"}, "id": {"550"}, "rootFolder": {"library/movies"}, "qualityProfile": {"hd"},
		"monitored": {"false", "true"}, "monitor": {"movieOnly"}, "minimumAvailability": {"released"}, "searchOnAdd": {"false", "true"},
	}
	rec := post(t, srv, "/library/movies/add", form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	name := names.Movie("Fight Club", 550)
	require.Equal(t, "/library/library/movie/"+name, rec.Header().Get("Location"))
	var m catalogv1.Movie
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "library", Name: name}, &m))
	require.Equal(t, "movies", m.Spec.RootFolderRef)
	require.True(t, *m.Spec.Monitored, "the checkbox's true follows its hidden false")
	require.True(t, *m.Spec.AddOptions.SearchForMovie)

	again := post(t, srv, "/library/movies/add", form)
	require.Equal(t, http.StatusSeeOther, again.Code, "adding it again goes to it")
	require.Equal(t, rec.Header().Get("Location"), again.Header().Get("Location"))
}

// TestAddOfAnItemAlreadyInTheLibraryGoesToIt: an item the library already
// holds under another name -- a retitled film, one a rescan named from a
// folder -- is found by its provider id, and nothing is created.
func TestAddOfAnItemAlreadyInTheLibraryGoesToIt(t *testing.T) {
	srv, c := addServer(t, hits(commonv1.MediaKindMovie), moviesRoot, hdProfile)
	rec := post(t, srv, "/library/movies/add", url.Values{
		"title": {"Heat (Director's Cut)"}, "id": {"949"}, "rootFolder": {"library/movies"}, "qualityProfile": {"hd"}, "monitor": {"movieOnly"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/media/movie/heat-x", rec.Header().Get("Location"))
	var list catalogv1.MovieList
	require.NoError(t, c.List(t.Context(), &list))
	require.Empty(t, list.Items)
}

func TestAddRejectionRerendersTheForm(t *testing.T) {
	srv, _ := addServer(t, hits(commonv1.MediaKindMovie), moviesRoot, hdProfile)
	rec := post(t, srv, "/library/movies/add", url.Values{"title": {"No Root"}, "id": {"550"}, "qualityProfile": {"hd"}, "monitor": {"movieOnly"}})
	require.NotEqual(t, http.StatusSeeOther, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-error=`, `data-slot="alert"`)
}

func TestAddPageOffersTheKindsOwnChoices(t *testing.T) {
	srv, _ := addServer(t, hits(commonv1.MediaKindSeries, `{"IDs":{"tvdb":"81189"},"Title":"Breaking Bad","Year":2008}`), moviesRoot, tvRoot, hdProfile)
	body := get(t, srv, "/library/tv/add/search?q=breaking").Body.String()
	for _, want := range []string{`name="seriesType"`, `name="seasonFolder"`, `name="searchCutoffUnmet"`, `data-tui-select-value="pilot"`, `data-tui-select-value="media/tv"`, "Breaking Bad"} {
		require.Contains(t, body, want)
	}
	require.NotContains(t, body, "library/movies", "a movie root folder is not offered for a series")
	require.Equal(t, http.StatusNotFound, get(t, srv, "/library/nope/add").Code)
	page := get(t, srv, "/library/tv/add")
	require.Equal(t, http.StatusOK, page.Code)
	requireThemedComponents(t, "/library/tv/add", page.Body.String())
}

// TestAFreshlyAddedItemDoesNotLandOnA404: the library view is rebuilt
// every few seconds, so the item an add redirects to is in the cluster
// before it is in the view. Its detail page says it is being added and
// reloads, rather than answering 404 (final review, 2026-09-29).
func TestAFreshlyAddedItemDoesNotLandOnA404(t *testing.T) {
	fresh := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "fight-club-x", Namespace: "library"},
		Spec:       catalogv1.MovieSpec{TmdbID: 550, RootFolderRef: "movies", QualityProfileRef: "hd"},
	}
	srv, _ := addServer(t, hits(commonv1.MediaKindMovie), moviesRoot, hdProfile, fresh)
	rec := get(t, srv, "/library/library/movie/fight-club-x")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "being added")
	require.Contains(t, rec.Body.String(), `http-equiv="refresh"`)

	require.Equal(t, http.StatusNotFound, get(t, srv, "/library/library/movie/no-such-movie").Code, "an item that does not exist is still 404")
	require.Equal(t, http.StatusNotFound, get(t, srv, "/library/library/nonsense/fight-club-x").Code)
}
