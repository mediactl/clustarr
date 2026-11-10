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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
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
		"not responding": {slow, "Metadata search is not responding"},
		"catalog down": {func(context.Context, schema.MetadataRequest) (schema.MetadataResponse, error) {
			return schema.MetadataResponse{}, fmt.Errorf("natsbus: %q: %w", "rpc.catalog.metadata.search", events.ErrNoResponders)
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

// TestAddRejectionRerendersTheForm: a rejected add shows the error with the
// owner's search and choices as they were, the form open, so nothing is
// typed or chosen twice (spec: Errors).
func TestAddRejectionRerendersTheForm(t *testing.T) {
	srv, _ := addServer(t, hits(commonv1.MediaKindMovie), moviesRoot, hdProfile)
	rec := post(t, srv, "/library/movies/add", url.Values{
		"q": {"fight"}, "title": {"Fight Club"}, "id": {"550"}, "rootFolder": {"library/movies"},
		"qualityProfile": {""}, "monitor": {"none"}, "minimumAvailability": {"inCinemas"},
		"monitored": {"false"}, "searchOnAdd": {"false"},
	})
	require.NotEqual(t, http.StatusSeeOther, rec.Code)
	body := rec.Body.String()
	requireTag(t, body, `data-action-error=`, `data-slot="alert"`)
	requireTag(t, pageBody(t, body), `name="q"`, `value="fight"`)
	requireTag(t, body, `data-add-hit="550"`)
	require.Regexp(t, `data-slot="collapsible"[^>]*\bdata-open\b`, body, "the rejected hit's form is open")
	requireTag(t, body, `name="monitor"`, `value="none"`)
	requireTag(t, body, `name="minimumAvailability"`, `value="inCinemas"`)
	require.NotRegexp(t, `id="add-monitored-550"[^>]*\bchecked\b`, body, "an unticked box stays unticked")
}

func TestAddPageOffersTheKindsOwnChoices(t *testing.T) {
	srv, _ := addServer(t, hits(commonv1.MediaKindSeries, `{"IDs":{"tvdb":"81189"},"Title":"Breaking Bad","Year":2008}`), moviesRoot, tvRoot, hdProfile)
	body := get(t, srv, "/library/tv/add/search?q=breaking").Body.String()
	for _, want := range []string{`name="seriesType"`, `name="seasonFolder"`, `name="searchCutoffUnmet"`, `data-templ-value="pilot"`, `data-templ-value="media/tv"`, "Breaking Bad"} {
		require.Contains(t, body, want)
	}
	require.NotContains(t, body, "library/movies", "a movie root folder is not offered for a series")
	// A series starts with every season but the specials monitored unless
	// the owner picks otherwise (owner, 2026-10-27).
	requireTag(t, body, `name="monitor"`, `value="unmonitorSpecials"`)
	require.Contains(t, body, `data-templ-value="none"`, "none is still offered")
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

// TestAddOffersOnlyRootFoldersOfTheKind: an Author goes in a book root
// folder, never an audiobook or comic one, which the rescan would read as
// audiobooks or comics (final review, 2026-09-29).
func TestAddOffersOnlyRootFoldersOfTheKind(t *testing.T) {
	books := &catalogv1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: "books", Namespace: "media"}, Spec: catalogv1.RootFolderSpec{Path: "/data/media/books", Kind: catalogv1.RootFolderKindBook}}
	audio := &catalogv1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: "audiobooks", Namespace: "media"}, Spec: catalogv1.RootFolderSpec{Path: "/data/media/audiobooks", Kind: catalogv1.RootFolderKindAudiobook}}
	ebook := &catalogv1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "ebook"}, Spec: catalogv1.QualityProfileSpec{MediaKind: catalogv1.ProfileMediaKindBook, Cutoff: "e", Tiers: []catalogv1.Tier{{Name: "e", Qualities: []string{"EPUB"}}}}}
	srv, _ := addServer(t, hits(commonv1.MediaKindAuthor, `{"IDs":{"olauthor":"OL21594A"},"Title":"Jane Austen"}`), books, audio, ebook)
	body := get(t, srv, "/library/books/add/search?q=austen").Body.String()
	require.Contains(t, body, `data-templ-value="media/books"`)
	require.NotContains(t, body, "media/audiobooks")
}

// TestAddRefusesARootFolderThePageDidNotOffer: the posted root folder names
// the item's namespace, so a hand-made POST must not be able to pick any
// namespace or a root folder of another kind.
func TestAddRefusesARootFolderThePageDidNotOffer(t *testing.T) {
	srv, c := addServer(t, hits(commonv1.MediaKindMovie), moviesRoot, tvRoot, hdProfile)
	for _, root := range []string{"kube-system/movies", "media/tv", "library/nope"} {
		rec := post(t, srv, "/library/movies/add", url.Values{
			"title": {"Fight Club"}, "id": {"550"}, "rootFolder": {root}, "qualityProfile": {"hd"}, "monitor": {"movieOnly"},
		})
		require.NotEqual(t, http.StatusSeeOther, rec.Code, root)
	}
	var list catalogv1.MovieList
	require.NoError(t, c.List(t.Context(), &list))
	require.Empty(t, list.Items)
}

// The non-video tabs' Add New (2026-11-10 design §9.1): a comic root, an
// audiobook root reading Audible's UK marketplace, a book root, and a
// profile of each kind.
var (
	comicsRoot = &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "comics", Namespace: "library"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/comics", Kind: catalogv1.RootFolderKindComic},
	}
	audiobooksRoot = &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "audiobooks", Namespace: "library"},
		Spec: catalogv1.RootFolderSpec{
			Path: "/data/media/audiobooks", Kind: catalogv1.RootFolderKindAudiobook,
			Defaults: catalogv1.RootDefaults{Region: catalogv1.AudiobookRegionUK},
		},
	}
	booksRoot = &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "books", Namespace: "library"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/books", Kind: catalogv1.RootFolderKindBook},
	}
	cbzProfile = &catalogv1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "cbz"},
		Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindComic, Cutoff: "cbz",
			Tiers: []catalogv1.Tier{{Name: "cbz", Qualities: []string{"CBZ"}}},
		},
	}
	m4bProfile = &catalogv1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "m4b"},
		Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindAudiobook, Cutoff: "m4b",
			Tiers: []catalogv1.Tier{{Name: "m4b", Qualities: []string{"M4B"}}},
		},
	}
	ebookProfile = &catalogv1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "ebook"},
		Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindBook, Cutoff: "epub",
			Tiers: []catalogv1.Tier{{Name: "epub", Qualities: []string{"EPUB"}}},
		},
	}
)

// addServerWith is addServer over the given library.
func addServerWith(t *testing.T, search ui.MetadataSearch, library []projection.LibraryItem, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	return addServerOpts(t, search, nil, library, objs...)
}

// The Comics tab's Add New searches the comic providers through the one
// metadata search and reads each hit's source off its id key -- a ComicVine
// volume or a MangaDex manga -- which rides the form and names the Comic's
// immutable source; the form offers the comic roots and profiles, Mylar's
// monitor modes, the kind (comic or manga), new issues and the search; a
// comic the library holds is found under its "<source>:<id>" ProviderID.
func TestAddComicsReadsEachHitsSourceAndCreatesTheComic(t *testing.T) {
	inLibrary := []projection.LibraryItem{{
		Ref: types.NamespacedName{Namespace: "media", Name: "berserk-x"}, Kind: commonv1.MediaKindComic,
		Tab: projection.TabComics, Title: "Berserk", ProviderID: "mangadex:801513ba",
	}}
	srv, c := addServerWith(t, hits(commonv1.MediaKindComic,
		`{"IDs":{"comicvine":"56907"},"Title":"Saga","Year":2012,"Poster":"https://comicvine.gamespot.com/a/saga.jpg"}`,
		`{"IDs":{"mangadex":"801513ba","anilist":"30002"},"Title":"Berserk","Year":1989}`,
		`{"IDs":{"metron":"7"},"Title":"No source we add"}`,
	), inLibrary, comicsRoot, cbzProfile, moviesRoot, hdProfile)
	rec := get(t, srv, "/library/comics/add/search?q=saga")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	requireTag(t, body, `data-add-hit="56907"`, `data-add-source="comicvine"`)
	require.Regexp(t, `data-hit-source="comicvine"[^>]*>\s*ComicVine`, body, "the hit says which provider it came from")
	requireTag(t, body, `name="source"`, `value="comicvine"`)
	requireTag(t, body, `data-add-hit="801513ba"`, `data-add-source="mangadex"`)
	require.Contains(t, body, `href="/library/media/comic/berserk-x"`, "the manga in the library links to it, by its source-prefixed id")
	require.NotContains(t, body, "No source we add", "a hit with neither id is dropped")
	for _, want := range []string{`name="comicKind"`, `data-templ-value="manga"`, `name="monitor"`, `data-templ-value="missing"`, "Monitor new issues", `data-templ-value="library/comics"`, `data-templ-value="cbz"`, "Start search for missing issues"} {
		require.Contains(t, body, want)
	}
	require.NotContains(t, body, "library/movies", "a movie root is not offered for a comic")
	require.NotContains(t, body, `data-templ-value="hd"`, "a video profile is not offered for a comic")

	rec = post(t, srv, "/library/comics/add", url.Values{
		"title": {"Saga"}, "id": {"56907"}, "source": {"comicvine"}, "rootFolder": {"library/comics"}, "qualityProfile": {"cbz"},
		"monitor": {"missing"}, "comicKind": {"comic"}, "monitorNewItems": {"none"}, "monitored": {"false", "true"}, "searchOnAdd": {"false", "true"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	name := names.Comic("Saga", "comicvine", "56907")
	require.Equal(t, "/library/library/comic/"+name, rec.Header().Get("Location"), "the comic's own page")
	var co catalogv1.Comic
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "library", Name: name}, &co))
	require.Equal(t, catalogv1.ComicSourceComicVine, co.Spec.Source)
	require.Equal(t, "56907", co.Spec.SourceID)
	require.Equal(t, catalogv1.ComicKindComic, co.Spec.Kind)
	require.Equal(t, catalogv1.ComicMonitorMissing, co.Spec.AddOptions.Monitor)
	require.False(t, *co.Spec.MonitorNewIssues)
	require.True(t, *co.Spec.Monitored)
	require.True(t, *co.Spec.AddOptions.SearchForMissing, "the form's search checkbox is spec.addOptions.searchForMissing, as a Series' is")

	// The manga the library holds is not created twice: its add goes to it.
	rec = post(t, srv, "/library/comics/add", url.Values{"title": {"Berserk"}, "id": {"801513ba"}, "source": {"mangadex"}, "rootFolder": {"library/comics"}, "qualityProfile": {"cbz"}, "monitor": {"all"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/media/comic/berserk-x", rec.Header().Get("Location"))

	// A comic posted without its source is refused, and the form comes back.
	rec = post(t, srv, "/library/comics/add", url.Values{"q": {"saga"}, "title": {"Saga"}, "id": {"1"}, "rootFolder": {"library/comics"}, "qualityProfile": {"cbz"}, "monitor": {"all"}})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "comic source")
}

// The Audiobooks tab's Add New asks Audible's catalog in the audiobook
// root's region (defaults.region, else us), which the form's region select
// defaults to and the Audiobook is created in; its hits are keyed by ASIN,
// and one the library holds is found under "asin:<id>".
func TestAddAudiobooksSearchesInTheRootsRegion(t *testing.T) {
	var asked schema.MetadataRequest
	search := func(_ context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error) {
		asked = req
		return schema.MetadataResponse{Kind: req.Kind, Results: [][]byte{
			[]byte(`{"IDs":{"asin":"B002V1OF70"},"Title":"Dune","Poster":"https://m.media-amazon.com/images/I/dune.jpg"}`),
			[]byte(`{"IDs":{"asin":"B0INLIB"},"Title":"In the library"}`),
		}}, nil
	}
	inLibrary := []projection.LibraryItem{{
		Ref: types.NamespacedName{Namespace: "media", Name: "in-library-x"}, Kind: commonv1.MediaKindAudiobook,
		Tab: projection.TabAudiobooks, Title: "In the library", ProviderID: "asin:B0INLIB",
	}}
	srv, c := addServerWith(t, search, inLibrary, audiobooksRoot, m4bProfile, booksRoot, ebookProfile)
	body := get(t, srv, "/library/audiobooks/add/search?q=dune").Body.String()
	require.Equal(t, commonv1.MediaKindAudiobook, asked.Kind)
	require.Equal(t, "uk", asked.Region, "the audiobook root's marketplace")
	requireTag(t, body, `data-add-hit="B002V1OF70"`)
	require.Contains(t, body, "/art/search?src=", "Audible's image host goes through the proxy")
	requireTag(t, body, `name="region"`, `value="uk"`)
	require.Contains(t, body, `data-templ-value="jp"`, "every marketplace is offered")
	require.Contains(t, body, `href="/library/media/audiobook/in-library-x"`)
	require.NotContains(t, body, `name="monitor"`, "an audiobook has no monitor modes")
	require.Contains(t, body, `name="monitored"`)
	require.Contains(t, body, `name="searchOnAdd"`)
	require.NotContains(t, body, "library/books", "a book root is not offered for an audiobook")

	rec := post(t, srv, "/library/audiobooks/add", url.Values{
		"title": {"Dune"}, "id": {"B002V1OF70"}, "rootFolder": {"library/audiobooks"}, "qualityProfile": {"m4b"},
		"region": {"uk"}, "monitored": {"false", "true"}, "searchOnAdd": {"false"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	name := names.Audiobook("Dune", "B002V1OF70")
	require.Equal(t, "/library/library/audiobook/"+name, rec.Header().Get("Location"))
	var ab catalogv1.Audiobook
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "library", Name: name}, &ab))
	require.Equal(t, "B002V1OF70", ab.Spec.ASIN)
	require.Equal(t, catalogv1.AudiobookRegionUK, ab.Spec.Region)
	require.False(t, *ab.Spec.AddOptions.SearchForMissing)

	// With no region on any audiobook root, Audible's US catalog.
	plain := audiobooksRoot.DeepCopy()
	plain.Spec.Defaults.Region = ""
	srv, _ = addServerWith(t, search, nil, plain, m4bProfile)
	body = get(t, srv, "/library/audiobooks/add/search?q=dune").Body.String()
	require.Equal(t, "us", asked.Region)
	requireTag(t, body, `name="region"`, `value="us"`)
}

// The Books tab adds an Author, or a standalone Book (design §9.1, §9.3):
// the page switches between the two, the query kept; the book search asks
// Open Library's works and its form offers the book roots and profiles and
// monitored alone (a Book has no add options); the Book is created with no
// author and its own profile and root, and the page sent to is the book's.
func TestAddBooksOffersAnAuthorOrAStandaloneBook(t *testing.T) {
	srv, c := addServerWith(t, hits(commonv1.MediaKindBook,
		`{"IDs":{"olwork":"OL893415W","olauthor":"OL79034A"},"Title":"Dune","Year":1965,"Poster":"https://covers.openlibrary.org/b/id/1-M.jpg"}`,
	), nil, booksRoot, ebookProfile, audiobooksRoot, m4bProfile)

	page := get(t, srv, "/library/books/add?q=dune")
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	require.Contains(t, body, "Add Author")
	requireTag(t, body, `data-add-kinds="author"`)
	requireTag(t, body, `data-add-kind="author"`, `hx-get="/library/books/add?q=dune"`)
	requireTag(t, body, `data-add-kind="book"`, `hx-get="/library/books/add?kind=book&amp;q=dune"`, `hx-target="#page-body"`)
	require.NotContains(t, body, `hx-vals`, "the tab's own kind rides no hx-vals")

	page = get(t, srv, "/library/books/add?kind=book&q=dune")
	require.Equal(t, http.StatusOK, page.Code)
	body = page.Body.String()
	require.Contains(t, body, "Add Book")
	requireTag(t, body, `data-add-kinds="book"`)
	requireTag(t, body, `hx-get="/library/books/add/search"`, `name="q"`, `hx-vals="{&#34;kind&#34;:&#34;book&#34;}"`)
	requireThemedComponents(t, "/library/books/add?kind=book", body)
	require.Equal(t, http.StatusNotFound, get(t, srv, "/library/books/add?kind=comic").Code, "a kind the tab does not add")
	require.Equal(t, http.StatusNotFound, get(t, srv, "/library/movies/add?kind=book").Code)

	body = get(t, srv, "/library/books/add/search?q=dune&kind=book").Body.String()
	requireTag(t, body, `data-add-hit="OL893415W"`)
	requireTag(t, body, `name="kind"`, `value="book"`)
	require.Contains(t, body, `action="/library/books/add"`)
	require.Contains(t, body, `data-templ-value="library/books"`)
	require.Contains(t, body, `data-templ-value="ebook"`)
	require.NotContains(t, body, "library/audiobooks", "an audiobook root is not offered for a book")
	require.NotContains(t, body, `name="monitor"`, "a book has no monitor modes")
	require.NotContains(t, body, `name="searchOnAdd"`, "a book has no add options")
	require.NotContains(t, body, `name="monitorNewItems"`)
	require.Contains(t, body, `name="monitored"`)
	require.Contains(t, body, "/art/search?src=", "an Open Library cover goes through the proxy")

	rec := post(t, srv, "/library/books/add", url.Values{
		"kind": {"book"}, "title": {"Dune"}, "id": {"OL893415W"}, "rootFolder": {"library/books"}, "qualityProfile": {"ebook"},
		"monitored": {"false", "true"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	name := names.Book("Dune", "OL893415W")
	require.Equal(t, "/library/library/book/"+name, rec.Header().Get("Location"), "the book's own page")
	var bk catalogv1.Book
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "library", Name: name}, &bk))
	require.Equal(t, "OL893415W", bk.Spec.WorkID)
	require.Nil(t, bk.Spec.AuthorRef, "a standalone book")
	require.Equal(t, "ebook", *bk.Spec.QualityProfileRef)
	require.Equal(t, "books", *bk.Spec.RootFolderRef)
	var authors catalogv1.AuthorList
	require.NoError(t, c.List(t.Context(), &authors))
	require.Empty(t, authors.Items, "the Books tab's own kind was not what was posted")

	// A failed search's retry keeps the kind.
	slow := func(ctx context.Context, _ schema.MetadataRequest) (schema.MetadataResponse, error) {
		<-ctx.Done()
		return schema.MetadataResponse{}, ctx.Err()
	}
	srv, _ = addServerWith(t, slow, nil, booksRoot, ebookProfile)
	ui.SetAddSearchTimeoutForTest(srv, 50*time.Millisecond)
	body = get(t, srv, "/library/books/add/search?q=dune&kind=book").Body.String()
	requireTag(t, body, `data-action="retry-search"`, `hx-get="/library/books/add/search?q=dune&amp;kind=book"`)
}

// The non-video tabs' add pages render whole, with the kind's choices,
// so a tab with nothing added yet still offers its Add New.
func TestEveryTabHasAnAddPage(t *testing.T) {
	srv, _ := addServerWith(t, nil, nil, comicsRoot, cbzProfile, audiobooksRoot, m4bProfile, booksRoot, ebookProfile)
	for _, tab := range projection.Tabs() {
		rec := get(t, srv, "/library/"+string(tab)+"/add")
		require.Equal(t, http.StatusOK, rec.Code, tab)
		require.Contains(t, rec.Body.String(), "Metadata search is not available", tab)
	}
}

// The Comics tab reads a MangaDex hit by the key the MangaDex client keys
// it with (extid.KeyMangaDex), which it spells as the comic source rather
// than import -- the ui binary links no metadata client
// (test/guards' TestUILinksNoAppOrManagerCode).
func TestMangaDexHitsAreKeyedAsTheClientKeysThem(t *testing.T) {
	require.Equal(t, string(catalogv1.ComicSourceMangaDex), extid.KeyMangaDex)
	require.Equal(t, string(catalogv1.ComicSourceComicVine), metadata.KeyComicVine)
}

// addServerOpts is addServerWith with the lookup an audiobook's add asks
// for its author (ui.Options.MetadataLookup).
func addServerOpts(t *testing.T, search ui.MetadataSearch, lookup ui.MetadataLookup, library []projection.LibraryItem, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	return ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c), Namespace: "media", MetadataSearch: search, MetadataLookup: lookup,
		Library: func(context.Context) []projection.LibraryItem { return library },
	}), c
}

// recordingSearch answers each MetadataRequest.Source from answers, or
// fails it from errs (a context.DeadlineExceeded waits the request out),
// and records every request it was asked.
func recordingSearch(answers map[string]schema.MetadataResponse, errs map[string]error) (ui.MetadataSearch, func() []schema.MetadataRequest) {
	var (
		mu    sync.Mutex
		asked []schema.MetadataRequest
	)
	search := func(ctx context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error) {
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		if err, ok := errs[req.Source]; ok {
			if errors.Is(err, context.DeadlineExceeded) {
				<-ctx.Done()
				return schema.MetadataResponse{}, ctx.Err()
			}
			return schema.MetadataResponse{}, err
		}
		return answers[req.Source], nil
	}
	return search, func() []schema.MetadataRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]schema.MetadataRequest(nil), asked...)
	}
}

// The Comics tab's search asks the gateway once per source -- ComicVine and
// MangaDex, MetadataRequest.Source naming each (task C4a) -- at once, and
// merges the answers in the keys' order with each hit's source badge, so a
// manga appears beside ComicVine's volumes where the unnamed search answered
// from the first provider alone; a hit's id is read under the key of the
// source that answered it. Each card shows the volume's publisher, issue
// count and start year, and a manga's creators (design §9.4). A source no
// provider is configured for, or one that is down or slow, is skipped while
// another answers; every source unconfigured is the "no provider" prompt,
// and every source failed the one search's error.
func TestAddComicsAsksEachSourceAndShowsTheVolumesFields(t *testing.T) {
	saga := `{"ids":{"comicvine":"56907"},"title":"Saga","year":2012,"publisher":"Image Comics","issueCount":66,"startYear":2012}`
	berserk := `{"ids":{"mangadex":"801513ba","comicvine":"999"},"title":"Berserk","startYear":1989,"issueCount":41,"authors":[{"name":"Kentaro Miura"}]}`
	comicVine := schema.MetadataResponse{Kind: commonv1.MediaKindComic, Results: [][]byte{[]byte(saga)}}
	mangaDex := schema.MetadataResponse{Kind: commonv1.MediaKindComic, Results: [][]byte{[]byte(berserk)}}
	noMangaDex := schema.MetadataResponse{Error: `metadata: no comic metadata provider of type "mangadex" is configured to search`}
	noComicVine := schema.MetadataResponse{Error: `metadata: no comic metadata provider of type "comicvine" is configured to search`}

	t.Run("both sources answer", func(t *testing.T) {
		search, asked := recordingSearch(map[string]schema.MetadataResponse{"comicvine": comicVine, "mangadex": mangaDex}, nil)
		srv, _ := addServerWith(t, search, nil, comicsRoot, cbzProfile)
		rec := get(t, srv, "/library/comics/add/search?q=saga")
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()

		reqs := asked()
		require.Len(t, reqs, 2, "one request per source")
		var sources []string
		for _, r := range reqs {
			require.Equal(t, commonv1.MediaKindComic, r.Kind)
			require.Equal(t, "saga", r.Text)
			sources = append(sources, r.Source)
		}
		require.ElementsMatch(t, []string{"comicvine", "mangadex"}, sources)

		require.Less(t, strings.Index(body, `data-add-hit="56907"`), strings.Index(body, `data-add-hit="801513ba"`),
			"ComicVine's volumes first, then MangaDex's manga, whichever answered first")
		requireTag(t, body, `data-add-hit="56907"`, `data-add-source="comicvine"`)
		requireTag(t, body, `data-add-hit="801513ba"`, `data-add-source="mangadex"`)
		require.Regexp(t, `data-hit-source="mangadex"[^>]*>\s*MangaDex`, body)
		require.NotContains(t, body, `data-add-hit="999"`, "a hit MangaDex answered is a MangaDex comic, whatever other ids it names")
		require.Regexp(t, `data-hit-publisher[^>]*>\s*Image Comics`, body)
		require.Regexp(t, `data-hit-issues[^>]*>\s*66 issues`, body)
		require.Contains(t, body, ">2012<", "the volume's start year")
		require.Contains(t, body, ">1989<", "a start year with no year is the year shown")
		require.Regexp(t, `data-hit-issues[^>]*>\s*41 issues`, body)
		require.Regexp(t, `data-hit-authors[^>]*>\s*by Kentaro Miura`, body, "a manga's creators")
		require.NotContains(t, body, `name="authorName"`, "only an audiobook's form carries its author")
		requireThemedComponents(t, "/library/comics/add/search", `<div id="page-body">`+body)
	})

	t.Run("a source no provider is configured for is skipped", func(t *testing.T) {
		search, _ := recordingSearch(map[string]schema.MetadataResponse{"comicvine": comicVine, "mangadex": noMangaDex}, nil)
		srv, _ := addServerWith(t, search, nil, comicsRoot, cbzProfile)
		body := get(t, srv, "/library/comics/add/search?q=saga").Body.String()
		requireTag(t, body, `data-add-hit="56907"`)
		require.NotContains(t, body, `href="/settings"`)
		require.NotContains(t, body, `role="alert"`)
	})

	t.Run("a source that is down is skipped", func(t *testing.T) {
		search, _ := recordingSearch(map[string]schema.MetadataResponse{"comicvine": noComicVine, "mangadex": mangaDex},
			map[string]error{"comicvine": errors.New("comicvine: 503")})
		srv, _ := addServerWith(t, search, nil, comicsRoot, cbzProfile)
		body := get(t, srv, "/library/comics/add/search?q=berserk").Body.String()
		requireTag(t, body, `data-add-hit="801513ba"`)
		require.NotContains(t, body, "Metadata search failed")
	})

	t.Run("every source unconfigured is the no-provider prompt", func(t *testing.T) {
		search, _ := recordingSearch(map[string]schema.MetadataResponse{"comicvine": noComicVine, "mangadex": noMangaDex}, nil)
		srv, _ := addServerWith(t, search, nil, comicsRoot, cbzProfile)
		body := get(t, srv, "/library/comics/add/search?q=saga").Body.String()
		require.Contains(t, body, `href="/settings"`)
		require.NotContains(t, body, "No matches")
	})

	t.Run("every source failed is the error", func(t *testing.T) {
		search, _ := recordingSearch(nil, map[string]error{"comicvine": context.DeadlineExceeded, "mangadex": errors.New("mangadex: 503")})
		srv, _ := addServerWith(t, search, nil, comicsRoot, cbzProfile)
		ui.SetAddSearchTimeoutForTest(srv, 50*time.Millisecond)
		body := get(t, srv, "/library/comics/add/search?q=saga").Body.String()
		require.Contains(t, body, "Metadata search is not responding", "the first source's failure, in the keys' order")
		requireTag(t, body, `data-action="retry-search"`, `hx-get="/library/comics/add/search?q=saga"`)

		search, _ = recordingSearch(nil, map[string]error{"comicvine": errors.New("comicvine: 503"), "mangadex": errors.New("mangadex: 502")})
		srv, _ = addServerWith(t, search, nil, comicsRoot, cbzProfile)
		body = get(t, srv, "/library/comics/add/search?q=saga").Body.String()
		require.Contains(t, body, "Metadata search failed: comicvine: 503")
	})
}

// anAuthor is an Author of ns the gateway has named.
func anAuthor(ns, name, metadataName string) *catalogv1.Author {
	return &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1.AuthorSpec{OpenLibraryID: "OL79034A", RootFolderRef: "books", QualityProfileRef: "ebook"},
		Status:     catalogv1.AuthorStatus{Metadata: &catalogv1.AuthorMetadata{Name: metadataName}},
	}
}

// An audiobook hit shows its authors, narrators and release date (design
// §9.5), and its form carries the first author's name and ASIN, which the
// add resolves to the Author it links (D11, §5.6): the author is looked up
// by ASIN through the bound MetadataLookup, in the form's region, and the
// Author of the root's namespace whose name is the answer's -- through
// release.TitleNorm, so case and punctuation do not part them -- is
// spec.authorRef, beside the form's search as spec.addOptions.searchForMissing.
// An author the lookup has no page for, a lookup that fails, no Author or
// two of the name, another namespace's Author, a hit naming no author and a
// ui with no lookup bound all link none and still add the audiobook.
func TestAddAudiobooksShowThePeopleAndLinkTheAuthor(t *testing.T) {
	dune := `{"ids":{"asin":"B002V1OF70"},"title":"Dune","year":2008,"releaseDate":"2008-08-12T00:00:00Z","publisher":"Macmillan Audio",` +
		`"authors":[{"name":"Frank Herbert","asin":"B000AP9A6K"},{"name":"Somebody Else","asin":"B0ELSE"}],"narrators":["Scott Brick","Orlagh Cassidy"]}`
	search := hits(commonv1.MediaKindAudiobook, dune)
	herbert := anAuthor("library", "frank-herbert-x", "Frank Herbert")
	elsewhere := anAuthor("media", "frank-herbert-media", "Frank Herbert")
	unnamed := anAuthor("library", "unnamed-x", "")
	namesake := anAuthor("library", "frank-herbert-ii", "Frank  Herbert.")

	type lookupCall struct {
		mu    sync.Mutex
		asked []schema.MetadataRequest
	}
	lookupAnswering := func(calls *lookupCall, resp schema.MetadataResponse, err error) ui.MetadataLookup {
		return func(_ context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error) {
			calls.mu.Lock()
			defer calls.mu.Unlock()
			calls.asked = append(calls.asked, req)
			return resp, err
		}
	}
	found := schema.MetadataResponse{Kind: commonv1.MediaKindAuthor, Provider: "audnexus", IDs: map[string]string{"asin": "B000AP9A6K"},
		Result: []byte(`{"ids":{"asin":"B000AP9A6K"},"name":"frank herbert","genres":["Science Fiction & Fantasy"]}`)}
	form := url.Values{
		"title": {"Dune"}, "id": {"B002V1OF70"}, "rootFolder": {"library/audiobooks"}, "qualityProfile": {"m4b"},
		"region": {"uk"}, "monitored": {"false", "true"}, "searchOnAdd": {"false", "true"},
		"authorName": {"Frank Herbert"}, "authorASIN": {"B000AP9A6K"},
	}
	name := names.Audiobook("Dune", "B002V1OF70")
	added := func(t *testing.T, c client.Client) catalogv1.Audiobook {
		t.Helper()
		var ab catalogv1.Audiobook
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "library", Name: name}, &ab))
		return ab
	}

	t.Run("the hit's people and the linked author", func(t *testing.T) {
		calls := &lookupCall{}
		srv, c := addServerOpts(t, search, lookupAnswering(calls, found, nil), nil, audiobooksRoot, m4bProfile, herbert, elsewhere, unnamed)
		body := get(t, srv, "/library/audiobooks/add/search?q=dune").Body.String()
		require.Regexp(t, `data-hit-authors[^>]*>\s*by Frank Herbert, Somebody Else`, body)
		require.Regexp(t, `data-hit-narrators[^>]*>\s*narrated by Scott Brick, Orlagh Cassidy`, body)
		require.Regexp(t, `data-hit-release[^>]*>\s*12 Aug 2008`, body)
		require.NotContains(t, body, ">2008<", "the release date stands in for the bare year")
		require.Regexp(t, `data-hit-publisher[^>]*>\s*Macmillan Audio`, body)
		requireTag(t, body, `name="authorName"`, `value="Frank Herbert"`)
		requireTag(t, body, `name="authorASIN"`, `value="B000AP9A6K"`)
		require.NotContains(t, body, "B0ELSE", "only the first author rides the form")
		requireThemedComponents(t, "/library/audiobooks/add/search", `<div id="page-body">`+body)

		rec := post(t, srv, "/library/audiobooks/add", form)
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
		require.Equal(t, "/library/library/audiobook/"+name, rec.Header().Get("Location"))
		ab := added(t, c)
		require.NotNil(t, ab.Spec.AuthorRef, "the Author of the root's namespace whose name the lookup answered")
		require.Equal(t, "frank-herbert-x", *ab.Spec.AuthorRef)
		require.True(t, *ab.Spec.AddOptions.SearchForMissing, "the form's search checkbox is spec.addOptions.searchForMissing")
		require.Equal(t, catalogv1.AudiobookRegionUK, ab.Spec.Region)
		require.Equal(t, []schema.MetadataRequest{{Kind: commonv1.MediaKindAuthor, IDs: map[string]string{"asin": "B000AP9A6K", "region": "uk"}}}, calls.asked,
			"one lookup, Kind author by ASIN in the form's region")
	})

	for tc, want := range map[string]struct {
		lookup ui.MetadataLookup
		objs   []client.Object
		form   url.Values
	}{
		"an author the provider has no page for": {lookup: lookupAnswering(&lookupCall{}, schema.MetadataResponse{Error: metadata.ErrNotFound.Error()}, nil), objs: []client.Object{herbert}},
		"a lookup that fails":                    {lookup: lookupAnswering(&lookupCall{}, schema.MetadataResponse{}, errors.New("natsbus: timeout")), objs: []client.Object{herbert}},
		"a lookup answering an error":            {lookup: lookupAnswering(&lookupCall{}, schema.MetadataResponse{Error: "metadata: no audiobook author metadata provider is configured"}, nil), objs: []client.Object{herbert}},
		"no Author of the name":                  {lookup: lookupAnswering(&lookupCall{}, found, nil), objs: []client.Object{unnamed}},
		"two Authors of the name":                {lookup: lookupAnswering(&lookupCall{}, found, nil), objs: []client.Object{herbert, namesake}},
		"another namespace's Author":             {lookup: lookupAnswering(&lookupCall{}, found, nil), objs: []client.Object{elsewhere}},
		"no lookup bound":                        {lookup: nil, objs: []client.Object{herbert}},
	} {
		t.Run(tc, func(t *testing.T) {
			objs := append([]client.Object{audiobooksRoot, m4bProfile}, want.objs...)
			srv, c := addServerOpts(t, search, want.lookup, nil, objs...)
			rec := post(t, srv, "/library/audiobooks/add", form)
			require.Equal(t, http.StatusSeeOther, rec.Code, "the add never fails for its author link: %s", rec.Body.String())
			ab := added(t, c)
			require.Nil(t, ab.Spec.AuthorRef, "no authorRef at all, not an empty one")
			require.True(t, *ab.Spec.AddOptions.SearchForMissing)
		})
	}

	t.Run("a hit naming no author asks no lookup", func(t *testing.T) {
		calls := &lookupCall{}
		srv, c := addServerOpts(t, search, lookupAnswering(calls, found, nil), nil, audiobooksRoot, m4bProfile, herbert)
		plain := url.Values{}
		for k, v := range form {
			if k != "authorName" && k != "authorASIN" {
				plain[k] = v
			}
		}
		rec := post(t, srv, "/library/audiobooks/add", plain)
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
		require.Nil(t, added(t, c).Spec.AuthorRef)
		require.Empty(t, calls.asked)
	})

	t.Run("a rejected add keeps the author on the form", func(t *testing.T) {
		srv, _ := addServerOpts(t, search, lookupAnswering(&lookupCall{}, found, nil), nil, audiobooksRoot, m4bProfile, herbert)
		bad := url.Values{}
		for k, v := range form {
			bad[k] = v
		}
		bad["qualityProfile"] = []string{""}
		bad["q"] = []string{"dune"}
		rec := post(t, srv, "/library/audiobooks/add", bad)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := rec.Body.String()
		requireTag(t, body, `name="authorName"`, `value="Frank Herbert"`)
		requireTag(t, body, `name="authorASIN"`, `value="B000AP9A6K"`)
	})
}
