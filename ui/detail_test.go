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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// The movie page follows Radarr's (design 2026-09-24): a toolbar row of
// icon-over-label actions, a hero on the movie's fanart with the poster,
// the title with its monitored bookmark, certification, year, runtime and
// provider links, a facts row (path, status, quality profile, size,
// original language), genres and the overview; then Files, the extra
// files (sidecars) and the alternative titles. Everything on it is read
// from the Movie's gathered metadata and its MediaFile through the
// reader; with no reader the hero still renders from the projection's
// item.

const nervePath = "/data/media/movies/Nerve (2016) {tmdb-328387}"

// nervePosterDigest and nerveFanartDigest are the fixture's stored artwork
// digests: what the movie's status.artwork carries and what the hero's
// poster and backdrop [projection.ArtURL]s are built from, so a test can
// assert the exact route without hand-formatting it twice.
const (
	nervePosterDigest = "nerve-poster-digest"
	nerveFanartDigest = "nerve-fanart-digest"
)

func movieFixture(t *testing.T, withActions bool) (*ui.Server, client.Client) {
	t.Helper()
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "nerve", Namespace: "default", UID: "nerve-uid"},
		Spec:       catalogv1.MovieSpec{TmdbID: 328387, Monitored: ptr.To(true), QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
		Status: catalogv1.MovieStatus{
			Phase: catalogv1.MoviePhaseImported, HasFile: true, Path: nervePath, FileRef: ptr.To("nerve-file"),
			Metadata: &catalogv1.MovieMetadata{
				Title: "Nerve", OriginalTitle: "Nerve", Year: 2016, RuntimeMinutes: 96, Certification: "PG-13",
				Genres: []string{"Mystery", "Adventure", "Crime"}, OriginalLanguage: "en",
				Overview:        "Industrious high school senior Vee Delmonico has had it with living life on the sidelines.",
				ExternalIDs:     map[string]string{"imdb": "tt3531824"},
				AlternateTitles: []string{"NePBB", "Nerve : Voyeur ou Joueur?"},
			},
			// Artwork, not Metadata.Images: every image this ui links to comes
			// from the object store through GET /art, never a provider's own
			// URL (ADR-0011) -- SourceURL here is only what the metadata
			// gateway would have fetched from, not anything a page renders.
			Artwork: []catalogv1.ArtworkEntry{
				{
					Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider,
					SourceURL: "https://image.tmdb.org/t/p/w500/nerve.jpg", Digest: nervePosterDigest,
					SizeBytes: 1, UpdatedAt: metav1.Now(),
				},
				{
					Type: catalogv1.ImageTypeFanart, Source: catalogv1.ArtworkSourceProvider,
					SourceURL: "https://image.tmdb.org/t/p/original/nerve-fanart.jpg", Digest: nerveFanartDigest,
					SizeBytes: 1, UpdatedAt: metav1.Now(),
				},
			},
		},
	}
	file := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "nerve-file", Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "nerve"},
			Path:      nervePath + "/Nerve (2016) {tmdb-328387} - [Bluray-1080p Proper][DTS 5.1][x264]-DRONES.mkv",
			SizeBytes: 8267000000,
			Quality:   commonv1.Quality{Name: "Bluray-1080p"}, Revision: commonv1.Revision{Version: 2, Repack: true},
			ReleaseGroup: "DRONES", Languages: []string{"English"}, FormatScore: -9995, MatchedFormats: []string{"DTS", "Repack/Proper"},
		},
		Status: catalogv1.MediaFileStatus{
			MediaInfo: &commonv1.MediaInfo{
				VideoCodec: "h264",
				Audio:      []commonv1.AudioStream{{Codec: "dts", Channels: 6, ChannelLayout: "5.1(side)", Language: "eng"}},
			},
			Sidecars: []catalogv1.Sidecar{{Path: nervePath + "/Nerve (2016) {tmdb-328387}.en.srt", Language: "en"}},
		},
	}
	rf := &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(movie, file, rf).Build()
	mk := func(name, title string) projection.LibraryItem {
		return projection.LibraryItem{
			Ref: types.NamespacedName{Namespace: "default", Name: name}, Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies,
			Title: title, Year: 2016, Monitored: true, Phase: "Imported", HasFile: true, QualityProfileRef: "hd-bluray-web",
			Poster: projection.ArtURL(commonv1.MediaKindMovie, types.UID(name+"-uid"), catalogv1.ImageTypePoster, name+"-poster-digest"),
		}
	}
	opts := ui.Options{
		Reader: c,
		Library: func(context.Context) []projection.LibraryItem {
			return []projection.LibraryItem{mk("alien", "Alien"), mk("nerve", "Nerve"), mk("zulu", "Zulu")}
		},
	}
	if withActions {
		opts.Actions = actions.New(c)
	}
	return ui.NewServer(t.Context(), opts), c
}

func detailPage(t *testing.T, srv *ui.Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, path)
	return rec.Body.String()
}

// section returns body from the element carrying anchor to the end of
// the next section marker (or the body's end), so a test can look inside
// one part of the page.
func section(t *testing.T, body, anchor string) string {
	t.Helper()
	at := strings.Index(body, anchor)
	require.GreaterOrEqual(t, at, 0, "no element carries %s", anchor)
	rest := body[at:]
	if end := regexp.MustCompile(`data-section="`).FindStringIndex(rest[len(anchor):]); end != nil {
		return rest[:len(anchor)+end[0]]
	}
	return rest
}

func TestMoviePageHeroReadsTheGatheredMetadata(t *testing.T) {
	srv, _ := movieFixture(t, false)
	body := detailPage(t, srv, "/library/default/movie/nerve")

	hero := requireTag(t, body, `data-hero`, `data-ref="default/nerve"`, `data-kind="movie"`, `data-monitored="true"`,
		`data-phase="Imported"`, `data-hasfile="true"`, `data-year="2016"`, `data-profile="hd-bluray-web"`, `data-poster="art"`)
	require.False(t, strings.HasPrefix(hero, "<a "))
	heroAt := strings.Index(body, `data-hero`)
	heroBody := section(t, body, `data-hero`)

	backdropURL := projection.ArtURL(commonv1.MediaKindMovie, "nerve-uid", catalogv1.ImageTypeFanart, nerveFanartDigest)
	posterURL := projection.ArtURL(commonv1.MediaKindMovie, "nerve-uid", catalogv1.ImageTypePoster, "nerve-poster-digest")
	requireTag(t, heroBody, `data-backdrop`, `src="`+backdropURL+`"`)
	requireTag(t, heroBody, `alt="Nerve"`, `src="`+posterURL+`"`)
	require.NotRegexp(t, regexp.MustCompile(`<img[^>]*src="https?://`), body,
		"no <img> ever hotlinks a provider's own URL -- every one points at this ui's own /art route (ADR-0011)")
	require.Regexp(t, regexp.MustCompile(`<h1[^>]*>[^<]*Nerve`), heroBody)

	// Radarr's bookmark beside the title is the monitored toggle.
	toggle := requireTag(t, heroBody, `data-action="set-monitored"`, `aria-label="Unmonitor"`, `type="submit"`)
	require.Contains(t, toggle, `data-slot="button"`)
	require.Contains(t, heroBody, `action="/library/default/movie/nerve/monitor"`)
	require.Contains(t, heroBody, `name="monitored" value="false"`)

	require.Regexp(t, regexp.MustCompile(`data-slot="badge"[^>]*>[^<]*PG-13`), heroBody, "the certification is a badge")
	require.Contains(t, heroBody, ">2016<")
	require.Contains(t, heroBody, "1h 36m")
	requireTag(t, heroBody, `href="https://www.themoviedb.org/movie/328387"`, `rel="noreferrer"`, `target="_blank"`)
	requireTag(t, heroBody, `href="https://www.imdb.com/title/tt3531824/"`, `rel="noreferrer"`)

	fact := func(name string) string {
		return section(t, heroBody, `data-fact="`+name+`"`)
	}
	require.Contains(t, fact("path"), nervePath)
	require.Contains(t, fact("status"), "Downloaded")
	requireTag(t, fact("status"), `data-status="downloaded"`, `bg-emerald-500`)
	require.Contains(t, fact("profile"), "hd-bluray-web")
	require.Contains(t, fact("size"), "7.69 GiB")
	require.Contains(t, fact("language"), "English")
	require.Contains(t, fact("genres"), "Mystery, Adventure, Crime")
	require.Regexp(t, regexp.MustCompile(`data-overview[^>]*>[^<]*Industrious high school senior`), heroBody)

	requireTag(t, body, `data-nav="prev"`, `href="/library/default/movie/alien"`)
	requireTag(t, body, `data-nav="next"`, `href="/library/default/movie/zulu"`)
	require.Less(t, strings.Index(body, `data-toolbar`), heroAt, "the toolbar sits above the hero")
}

func TestMoviePageToolbarHasRadarrsActions(t *testing.T) {
	srv, _ := movieFixture(t, false)
	body := detailPage(t, srv, "/library/default/movie/nerve")
	bar := section(t, body, `data-toolbar`)
	heroAt := strings.Index(bar, `data-hero`)
	require.GreaterOrEqual(t, heroAt, 0)
	bar = bar[:heroAt]

	refresh := requireTag(t, bar, `data-action="refresh-metadata"`, `data-slot="button"`, `type="submit"`)
	require.Contains(t, refresh, `data-toolbar-button`, "toolbar buttons are icon-over-label")
	require.Contains(t, bar, `action="/library/default/movie/nerve/refresh"`)
	require.Contains(t, bar, `name="scan" value="true"`, "Refresh & Scan asks for the folder scan too")
	require.Contains(t, bar, "Refresh &amp; Scan")
	requireTag(t, bar, `data-action="search-now"`, `data-slot="button"`, `data-toolbar-button`)
	require.Contains(t, bar, "Search Movie")
	require.NotContains(t, bar, `data-action="rescan"`, "the RootFolder rescan is the library page's, not the movie's")
	require.NotContains(t, bar, `data-action="set-monitored"`, "monitoring is the bookmark on the title")
	require.Contains(t, bar, `<svg`, "every toolbar button has its icon")
}

func TestMoviePageListsFilesExtrasAndTitles(t *testing.T) {
	srv, c := movieFixture(t, false)
	body := detailPage(t, srv, "/library/default/movie/nerve")

	files := section(t, body, `data-section="files"`)
	require.Contains(t, files, `data-slot="table"`, "the files are a table")
	for _, head := range []string{"Relative Path", "Video Codec", "Audio Info", "Size", "Languages", "Quality", "Release Group", "Formats"} {
		require.Contains(t, files, ">"+head+"<", "column %s", head)
	}
	row := section(t, files, `data-file="nerve-file"`)
	require.Contains(t, row, "Nerve (2016) {tmdb-328387} - [Bluray-1080p Proper][DTS 5.1][x264]-DRONES.mkv", "the path is relative to the movie's folder")
	require.NotContains(t, row, nervePath+"/")
	require.Contains(t, row, ">h264<")
	require.Contains(t, row, "DTS - 5.1")
	require.Contains(t, row, "7.69 GiB")
	require.Regexp(t, regexp.MustCompile(`data-slot="badge"[^>]*>[^<]*English`), row)
	require.Regexp(t, regexp.MustCompile(`data-slot="badge"[^>]*>[^<]*Bluray-1080p`), row)
	require.Contains(t, row, ">DRONES<")
	require.Regexp(t, regexp.MustCompile(`data-slot="badge"[^>]*>[^<]*DTS<`), row)
	require.Regexp(t, regexp.MustCompile(`data-slot="badge"[^>]*>[^<]*Repack/Proper`), row)
	require.Contains(t, row, "-9995")

	extras := section(t, body, `data-section="extras"`)
	require.Contains(t, extras, `data-slot="table"`)
	extra := section(t, extras, `data-extra=`)
	require.Contains(t, extra, "Nerve (2016) {tmdb-328387}.en.srt")
	require.Contains(t, extra, "Subtitle")
	require.Contains(t, extra, ">en<")

	titles := section(t, body, `data-section="titles"`)
	require.Contains(t, titles, `data-slot="table"`)
	require.Contains(t, titles, ">NePBB<")
	require.Contains(t, titles, "Nerve : Voyeur ou Joueur?")

	// No sidecars: Radarr's "No extra files to manage."; no alternative
	// titles: no Titles section at all.
	var file catalogv1.MediaFile
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve-file"}, &file))
	file.Status.Sidecars = nil
	require.NoError(t, c.Update(t.Context(), &file))
	var movie catalogv1.Movie
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &movie))
	movie.Status.Metadata.AlternateTitles = nil
	require.NoError(t, c.Update(t.Context(), &movie))
	body = detailPage(t, srv, "/library/default/movie/nerve")
	require.Contains(t, section(t, body, `data-section="extras"`), "No extra files to manage.")
	require.NotContains(t, body, `data-section="titles"`)
}

func TestMoviePageWithoutAReaderStillRendersTheHero(t *testing.T) {
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies,
		Title: "Heat", Year: 1995, Monitored: false, Phase: "Wanted",
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})
	body := detailPage(t, srv, "/library/default/movie/heat")
	requireTag(t, body, `data-hero`, `data-ref="default/heat"`, `data-poster="none"`)
	requireTag(t, body, `data-action="set-monitored"`, `aria-label="Monitor"`)
	require.Contains(t, section(t, body, `data-fact="status"`), "Missing")
	require.NotContains(t, body, `data-backdrop`)
	require.NotContains(t, body, `data-nav="prev"`, "a lone item has no neighbours")
	require.NotContains(t, body, `data-nav="next"`)
	require.Contains(t, section(t, body, `data-section="files"`), "No file on disk.")
}

// TestRefreshAndScanCreatesAScanOfTheMoviesFolder: Radarr's "Refresh &
// Scan" refreshes the metadata and rescans the movie's own folder, so the
// form's scan=true creates a LibraryScan of the RootFolder restricted to
// the folder under it, beside the refresh annotation.
func TestRefreshAndScanCreatesAScanOfTheMoviesFolder(t *testing.T) {
	srv, c := movieFixture(t, true)
	rec := postForm(t, srv, "/library/default/movie/nerve/refresh",
		url.Values{"return": {"/library/default/movie/nerve"}, "scan": {"true"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/default/movie/nerve", rec.Header().Get("Location"))

	var movie catalogv1.Movie
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &movie))
	require.NotEmpty(t, movie.Annotations[catalogv1.AnnotationRefreshMetadata])

	var scans catalogv1.LibraryScanList
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 1)
	require.Equal(t, "movies", scans.Items[0].Spec.RootFolderRef)
	require.Equal(t, "Nerve (2016) {tmdb-328387}", scans.Items[0].Spec.Subpath, "the scan is restricted to the movie's folder")
	require.Equal(t, actions.OriginUI, scans.Items[0].Labels[actions.LabelOrigin])

	// Without scan=true the refresh alone happens, as before.
	rec = postForm(t, srv, "/library/default/movie/nerve/refresh", url.Values{"return": {"/library/default/movie/nerve"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 1)
}

func TestRefreshAndScanSkipsTheScanForAnItemWithNoFolder(t *testing.T) {
	srv, c := movieFixture(t, true)
	var movie catalogv1.Movie
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &movie))
	movie.Status.Path = ""
	require.NoError(t, c.Update(t.Context(), &movie))

	rec := postForm(t, srv, "/library/default/movie/nerve/refresh",
		url.Values{"return": {"/library/default/movie/nerve"}, "scan": {"true"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	var scans catalogv1.LibraryScanList
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Empty(t, scans.Items, "nothing on disk yet: the refresh happens, no scan is created")
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &movie))
	require.NotEmpty(t, movie.Annotations[catalogv1.AnnotationRefreshMetadata])
}

// TestSeriesPageHeroReadsTheSeriesMetadata: the series page shares the
// hero, filled from the Series' own metadata.
func TestSeriesPageHeroReadsTheSeriesMetadata(t *testing.T) {
	srv, _ := seriesFixture(t)
	body := detailPage(t, srv, "/library/default/series/andor")
	requireTag(t, body, `data-hero`, `data-ref="default/andor"`, `data-kind="series"`)
	require.Contains(t, section(t, body, `data-toolbar`), "Search Series")
	require.Contains(t, section(t, body, `data-toolbar`), "Refresh &amp; Scan")
	require.Regexp(t, regexp.MustCompile(`<h1[^>]*>[^<]*Andor`), body)
	require.Contains(t, body, ">2022<")
	require.NotContains(t, body, `data-section="files"`, "a series' files live on its seasons")
}
