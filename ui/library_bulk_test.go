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

// The library's mass editor (2026-10-06, after Sonarr's): Select on the
// toolbar marks cards, and the bulk bar posts the selection to
// /library/{tab}/bulk, which runs each item page's own action.

func bulkMovie(name string, monitored bool) *catalogv1.Movie {
	return &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec:       catalogv1.MovieSpec{Monitored: boolPtr(monitored), QualityProfileRef: "hd", RootFolderRef: "movies", TmdbID: 1},
	}
}

func bulkLibrary() []projection.LibraryItem {
	mk := func(name string, kind commonv1.MediaKind, tab projection.Tab) projection.LibraryItem {
		return projection.LibraryItem{Ref: types.NamespacedName{Namespace: "media", Name: name}, Kind: kind, Tab: tab, Title: name}
	}
	return []projection.LibraryItem{
		mk("arrival", commonv1.MediaKindMovie, projection.TabMovies),
		mk("heat", commonv1.MediaKindMovie, projection.TabMovies),
		mk("andor", commonv1.MediaKindSeries, projection.TabTV),
	}
}

func bulkServer(t *testing.T, objs ...client.Object) (http.Handler, client.Client) {
	t.Helper()
	writer := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(objs...).Build()
	srv := ui.NewServer(t.Context(), ui.Options{
		Actions: actions.New(writer),
		Library: func(context.Context) []projection.LibraryItem { return bulkLibrary() },
	})
	return srv.Handler(), writer
}

func bulkPost(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

func TestBulkUnmonitorPatchesEverySelectedItemAndReturnsToTheTab(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("arrival", true), bulkMovie("heat", true))
	rec := bulkPost(t, h, "/library/movies/bulk", url.Values{
		"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/movie/heat"}, "return": {"/library/movies"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/library/movies", rec.Header().Get("Location"))
	for _, name := range []string{"arrival", "heat"} {
		var got catalogv1.Movie
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &got))
		require.False(t, *got.Spec.Monitored, name)
	}

	rec = bulkPost(t, h, "/library/movies/bulk", url.Values{"action": {"monitor"}, "item": {"media/movie/heat"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	var heat catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "heat"}, &heat))
	require.True(t, *heat.Spec.Monitored)
}

// The form reaches only what the tab shows: an item of another tab, one
// not in the library, a malformed one, no selection and an unknown action
// are refused before anything is written.
func TestBulkRefusesWhatTheTabDoesNotShow(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("arrival", true))
	for name, form := range map[string]url.Values{
		"another tab":    {"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/series/andor"}},
		"not in library": {"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/movie/brazil"}},
		"malformed":      {"action": {"unmonitor"}, "item": {"media/arrival"}},
		"nothing":        {"action": {"unmonitor"}},
		"unknown action": {"action": {"explode"}, "item": {"media/movie/arrival"}},
	} {
		rec := bulkPost(t, h, "/library/movies/bulk", form)
		require.Equal(t, http.StatusBadRequest, rec.Code, name)
		require.Contains(t, rec.Body.String(), `data-action-error="invalid"`, name)
	}
	var got catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "arrival"}, &got))
	require.True(t, *got.Spec.Monitored, "a refused form writes nothing, not even its valid items")
	require.Equal(t, http.StatusNotFound, bulkPost(t, h, "/library/podcasts/bulk", url.Values{"action": {"monitor"}}).Code)
}

func TestBulkDeleteRequestsEachItemsDelete(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("arrival", true), bulkMovie("heat", true))
	rec := bulkPost(t, h, "/library/movies/bulk", url.Values{
		"action": {"delete"}, "item": {"media/movie/arrival", "media/movie/heat"}, "files": {"true"}, "exclude": {"true"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	for _, name := range []string{"arrival", "heat"} {
		var got catalogv1.Movie
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &got))
		require.Equal(t, string(catalogv1.DeleteFiles), got.Annotations[catalogv1.AnnotationDelete], name)
		require.Equal(t, "true", got.Annotations[catalogv1.AnnotationDeleteAddExclusion], name)
	}
}

// One item's failure does not stop the others; the failures are reported.
func TestBulkAttemptsEveryItemAndReportsTheFailures(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("heat", true)) // arrival is in the library view but not the cluster
	rec := bulkPost(t, h, "/library/movies/bulk", url.Values{"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/movie/heat"}})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "arrival")
	var heat catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "heat"}, &heat))
	require.False(t, *heat.Spec.Monitored, "the item after the failure was still patched")
}

func TestLibraryPageHasTheMassEditor(t *testing.T) {
	h, _ := bulkServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	requireTag(t, body, `id="library-page"`, `group/library`, `data-tab="movies"`)
	// The selection bar floats at the foot of the window while the page is
	// selecting (2026-11-28, the Liquid Glass redesign, after Photos and
	// Files); the toolbar stays, its Select reading Done.
	bar := requireTag(t, body, `data-bulk-bar`, "fixed", "bottom-6", "hidden", "group-data-selecting/library:flex")
	require.NotEmpty(t, bar)
	require.NotContains(t, tagWith(t, body, `data-slot="menubar"`), "group-data-selecting/library:hidden")
	require.Less(t, strings.Index(body, `data-bulk-bar`), strings.Index(body, `id="library-rows"`), "the bar is above the grid")
	requireTag(t, body, `id="library-bulk"`, `data-bulk-form`, `action="/library/movies/bulk"`, `method="post"`)
	for _, action := range []string{"monitor", "unmonitor", "search", "refresh"} {
		b := requireTag(t, body, `data-bulk-action="`+action+`"`, `name="action"`, `value="`+action+`"`, `type="submit"`)
		require.Regexp(t, regexp.MustCompile(`\sdisabled(\s|>)`), b, "%s waits for a selection", action)
	}
	del := requireTag(t, body, `data-bulk-action="delete"`, `data-slot="dialog-trigger"`)
	require.Regexp(t, regexp.MustCompile(`\sdisabled(\s|>)`), del)
	// Delete asks first, in its own form: the dialog moves to <body>.
	require.Regexp(t, regexp.MustCompile(`<input type="hidden" name="action" value="delete">`), body)
	require.Equal(t, 2, strings.Count(body, `data-bulk-form`), "the bar's form and the delete dialog's")
	requireTag(t, body, `id="bulk-delete-files"`, `name="files"`)
	require.Contains(t, body, `data-selected-count`)

	// Each card has its selection mark, shown only while selecting.
	card := requireTag(t, body, `data-ref="media/arrival"`, "group/card")
	require.NotEmpty(t, card)
	require.Equal(t, 2, strings.Count(body, `data-select-mark`), "one mark per card")
	require.Contains(t, body, `data-card-details`)
	require.Contains(t, body, `var(--library-poster-width,160px)`, "the grid's column is the Options menu's poster size")

	headEnd := strings.Index(body, "</head>")
	require.GreaterOrEqual(t, headEnd, 0)
	head := body[:headEnd]
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/library.js"`), head)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/library.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	for _, hook := range []string{
		"data-selecting", "select-toggle", "select-all", "data-bulk-form", "data-bulk-action",
		"data-selected-count", `name = "item"`, "data-poster-size", "--library-poster-width", "data-library-details",
		"menubar-value-change", "menubar-checked-change", "htmx:sseMessage",
	} {
		require.Contains(t, rec.Body.String(), hook)
	}
}

// Move stores the selected series under a root folder of the other library
// (anime library design, 2026-11-25, §5): the root folder decides whether a
// series is TV or Anime, so the move is one spec patch of the root folder,
// the series type the folder makes it, and the folder's default profile --
// nothing on disk, which the file loop does after it. Under an anime root a
// series is anime; back under a TV root an anime becomes the folder's
// default type, and a daily series keeps its own.
func TestBulkMoveStoresASeriesUnderTheOtherLibrarysRootFolder(t *testing.T) {
	root := func(name string, typ catalogv1.SeriesType, profile string) *catalogv1.RootFolder {
		rf := &catalogv1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1.RootFolderSpec{Path: "/data/media/" + name, Kind: catalogv1.RootFolderKindSeries},
		}
		rf.Spec.Defaults.SeriesType, rf.Spec.Defaults.QualityProfileRef = typ, profile
		return rf
	}
	series := func(name, rootFolder string, typ catalogv1.SeriesType) *catalogv1.Series {
		return &catalogv1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1.SeriesSpec{TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: rootFolder, SeriesType: typ},
		}
	}
	item := func(name, rootFolder string, typ catalogv1.SeriesType, tab projection.Tab) projection.LibraryItem {
		return projection.LibraryItem{
			Ref: types.NamespacedName{Namespace: "media", Name: name}, Kind: commonv1.MediaKindSeries, Tab: tab, Title: name,
			RootFolder: rootFolder, SeriesType: string(typ), QualityProfileRef: "hd",
		}
	}
	movies := &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "media"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(
		root("tv", catalogv1.SeriesTypeStandard, "hd"), root("anime", catalogv1.SeriesTypeAnime, "anime-hd"), root("tv-archive", "", ""), movies,
		series("frieren", "tv", catalogv1.SeriesTypeAnime), series("andor", "tv", catalogv1.SeriesTypeStandard),
		series("the-daily", "tv", catalogv1.SeriesTypeDaily), series("bleach", "anime", catalogv1.SeriesTypeAnime),
		bulkMovie("heat", true),
	).Build()
	library := []projection.LibraryItem{
		item("frieren", "tv", catalogv1.SeriesTypeAnime, projection.TabTV), item("andor", "tv", catalogv1.SeriesTypeStandard, projection.TabTV),
		item("the-daily", "tv", catalogv1.SeriesTypeDaily, projection.TabTV), item("bleach", "anime", catalogv1.SeriesTypeAnime, projection.TabAnime),
		{Ref: types.NamespacedName{Namespace: "media", Name: "heat"}, Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies, Title: "heat"},
	}
	h := ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c), Library: func(context.Context) []projection.LibraryItem { return library },
	}).Handler()
	spec := func(name string) catalogv1.SeriesSpec {
		var s catalogv1.Series
		require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &s))
		return s.Spec
	}
	move := func(tab, target string, items ...string) *httptest.ResponseRecorder {
		return bulkPost(t, h, "/library/"+tab+"/bulk", url.Values{"action": {"move"}, "rootFolder": {target}, "item": items, "return": {"/library/" + tab}})
	}

	// TV to Anime: an anime not moved yet, and a series the classifier
	// called standard that is one.
	rec := move("tv", "media/anime", "media/series/frieren", "media/series/andor")
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	for _, name := range []string{"frieren", "andor"} {
		got := spec(name)
		require.Equal(t, "anime", got.RootFolderRef, name)
		require.Equal(t, catalogv1.SeriesTypeAnime, got.SeriesType, "%s: under an anime root a series is anime", name)
		require.Equal(t, "anime-hd", got.QualityProfileRef, "%s: the folder's default profile", name)
	}

	// Anime to TV: the folder's default type; a folder naming no profile
	// leaves the series' own.
	rec = move("anime", "media/tv", "media/series/bleach")
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	got := spec("bleach")
	require.Equal(t, "tv", got.RootFolderRef)
	require.Equal(t, catalogv1.SeriesTypeStandard, got.SeriesType)
	require.Equal(t, "hd", got.QualityProfileRef)

	// Between two TV roots a daily series stays daily, and keeps its
	// profile where the folder has none.
	rec = move("tv", "media/tv-archive", "media/series/the-daily")
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	got = spec("the-daily")
	require.Equal(t, "tv-archive", got.RootFolderRef)
	require.Equal(t, catalogv1.SeriesTypeDaily, got.SeriesType)
	require.Equal(t, "hd", got.QualityProfileRef)

	// Refused, nothing written: a root folder that is no series folder, one
	// that does not exist, and a film.
	for _, target := range []string{"media/movies", "media/nowhere", "elsewhere/anime", "anime", ""} {
		rec = move("tv", target, "media/series/the-daily")
		require.Equal(t, http.StatusBadRequest, rec.Code, "target %q", target)
	}
	require.Equal(t, "tv-archive", spec("the-daily").RootFolderRef)
	rec = move("movies", "media/anime", "media/movie/heat")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// The bulk bar offers Move on TV and on Anime, to the other library's root
// folders, and nowhere else: a film, an album or a book has one library.
func TestTheBulkBarOffersMoveBetweenTVAndAnime(t *testing.T) {
	root := func(name string, kind catalogv1.RootFolderKind, typ catalogv1.SeriesType) *catalogv1.RootFolder {
		rf := &catalogv1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1.RootFolderSpec{Path: "/data/media/" + name, Kind: kind},
		}
		rf.Spec.Defaults.SeriesType = typ
		return rf
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(
		root("tv", catalogv1.RootFolderKindSeries, catalogv1.SeriesTypeStandard),
		root("anime", catalogv1.RootFolderKindSeries, catalogv1.SeriesTypeAnime),
		root("movies", catalogv1.RootFolderKindMovie, ""),
	).Build()
	h := ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c), Library: func(context.Context) []projection.LibraryItem { return nil },
	}).Handler()
	page := func(tab string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/"+tab, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}

	tv := page("tv")
	trigger := requireTag(t, tv, `data-bulk-action="move"`, `data-slot="dialog-trigger"`)
	require.Regexp(t, regexp.MustCompile(`\sdisabled(\s|>)`), trigger, "Move waits for a selection")
	require.Contains(t, tv, `name="action" value="move"`)
	require.Contains(t, tv, `data-templ-value="media/anime"`)
	require.Contains(t, tv, "Anime · anime (/data/media/anime)")
	require.NotContains(t, tv, `data-templ-value="media/tv"`, "TV's own root folder is no move")

	anime := page("anime")
	require.Contains(t, anime, `data-bulk-action="move"`)
	require.Contains(t, anime, `data-templ-value="media/tv"`)
	require.Contains(t, anime, "TV · tv (/data/media/tv)")

	require.NotContains(t, page("movies"), `data-bulk-action="move"`)
}

// The Edit dialog cannot make a series anime, nor an anime something else
// (anime library design, 2026-11-25, §5): a series is anime by the root
// folder it is stored under, and Move changes both together. On the Anime
// tab a posted type is dropped; on TV "anime" is refused -- unless the
// series is an anime not moved yet, whose other fields must stay editable.
func TestTheEditDialogLeavesBeingAnimeToTheRootFolder(t *testing.T) {
	series := func(name, rootFolder string, typ catalogv1.SeriesType) *catalogv1.Series {
		return &catalogv1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1.SeriesSpec{TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: rootFolder, SeriesType: typ},
		}
	}
	item := func(name string, typ catalogv1.SeriesType, tab projection.Tab) projection.LibraryItem {
		return projection.LibraryItem{
			Ref: types.NamespacedName{Namespace: "media", Name: name}, Kind: commonv1.MediaKindSeries, Tab: tab, Title: name, SeriesType: string(typ),
		}
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(
		series("bleach", "anime", catalogv1.SeriesTypeAnime), series("andor", "tv", catalogv1.SeriesTypeStandard),
		series("frieren", "tv", catalogv1.SeriesTypeAnime),
	).Build()
	library := []projection.LibraryItem{
		item("bleach", catalogv1.SeriesTypeAnime, projection.TabAnime), item("andor", catalogv1.SeriesTypeStandard, projection.TabTV),
		item("frieren", catalogv1.SeriesTypeAnime, projection.TabTV),
	}
	h := ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c), Library: func(context.Context) []projection.LibraryItem { return library },
	}).Handler()
	edit := func(name, typ string) *httptest.ResponseRecorder {
		return bulkPost(t, h, "/library/media/series/"+name+"/edit", url.Values{
			"qualityProfile": {"uhd"}, "monitored": {"false", "true"}, "seriesType": {typ}, "return": {"/library/tv"},
		})
	}
	spec := func(name string) catalogv1.SeriesSpec {
		var s catalogv1.Series
		require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &s))
		return s.Spec
	}

	require.Equal(t, http.StatusSeeOther, edit("bleach", "daily").Code)
	require.Equal(t, catalogv1.SeriesTypeAnime, spec("bleach").SeriesType, "under an anime root a series stays anime")
	require.Equal(t, "uhd", spec("bleach").QualityProfileRef, "its other fields are saved")

	require.Equal(t, http.StatusBadRequest, edit("andor", "anime").Code)
	require.Equal(t, catalogv1.SeriesTypeStandard, spec("andor").SeriesType)
	require.Equal(t, "hd", spec("andor").QualityProfileRef, "a refused edit writes nothing")
	require.Equal(t, http.StatusSeeOther, edit("andor", "daily").Code)
	require.Equal(t, catalogv1.SeriesTypeDaily, spec("andor").SeriesType)

	require.Equal(t, http.StatusSeeOther, edit("frieren", "anime").Code, "an anime not moved yet keeps its type through an edit")
	require.Equal(t, catalogv1.SeriesTypeAnime, spec("frieren").SeriesType)
	require.Equal(t, "uhd", spec("frieren").QualityProfileRef)
}
