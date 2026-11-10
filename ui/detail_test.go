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
		Spec:       catalogv1.MovieSpec{TmdbID: 328387, Monitored: new(true), QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
		Status: catalogv1.MovieStatus{
			Phase: catalogv1.MoviePhaseImported, HasFile: true, Path: nervePath, FileRef: new("nerve-file"),
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
			Sidecars: []catalogv1.Sidecar{{Name: "Nerve (2016) {tmdb-328387}.en.srt", Language: "en"}},
			// The loop's blocks on the file (ADR-0016, F8.7): the item page's
			// Subtitles table and the Files table's Transcode column.
			Subtitles: &catalogv1.SubtitlesStatus{Phase: catalogv1.SubtitlesPhaseWanted, Items: []catalogv1.SubtitleItemStatus{
				{LangKey: "en", State: catalogv1.SubtitleStateDownloaded, Score: 92, ScoreOutOf: 100, Provider: "opensubtitles"},
				{
					LangKey: "es", State: catalogv1.SubtitleStatePending, LastError: "every eligible provider is throttled",
					NextSearchAt: new(metav1.NewTime(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))),
				},
			}},
			Transcode: &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseRunning},
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
	requireTag(t, heroBody, `href="https://www.themoviedb.org/movie/328387"`, `rel="noreferrer"`, `target="_blank"`, `data-provider-link="tmdb"`)
	requireTag(t, heroBody, `href="https://www.imdb.com/title/tt3531824/"`, `rel="noreferrer"`, `data-provider-link="imdb"`)
	// Each provider link is its provider's logo (owner, 2026-11-04), not
	// the generic external-link icon.
	requireTag(t, heroBody, `src="/static/providers/tmdb.svg"`, `alt="TMDB"`)
	requireTag(t, heroBody, `src="/static/providers/imdb.svg"`, `alt="IMDb"`)
	require.NotContains(t, heroBody, "lucide-external-link", "a provider link shows its logo")

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

	// The bar is one button group (owner, 2026-11-04), the group itself
	// with no panel around it ("Simplify the toolbar ... like the large
	// variant"), its buttons at the large size: every action is a direct
	// child of the group, its form hidden and named by the button's form
	// attribute, and reads one word, the whole action in its title.
	requireTag(t, body, `data-toolbar`, `data-slot="button-group"`, `role="group"`, `glass`)
	refresh := requireTag(t, bar, `data-action="refresh-metadata"`, `data-slot="button"`, `type="submit"`, `form="refresh-nerve"`, `title="Refresh &amp; Scan"`)
	require.Contains(t, refresh, `data-toolbar-button`, "toolbar buttons are icon-over-label")
	requireTag(t, bar, `id="refresh-nerve"`, `action="/library/default/movie/nerve/refresh"`, `hidden`)
	require.Contains(t, bar, `name="scan" value="true"`, "Refresh & Scan asks for the folder scan too")
	require.Regexp(t, `>\s*Refresh\s*<`, bar, "the button reads one word")
	require.NotContains(t, bar, "Refresh &amp; Scan</span>")
	requireTag(t, bar, `data-action="search-now"`, `data-slot="button"`, `data-toolbar-button`, `form="search-nerve"`, `title="Search Movie"`)
	require.Regexp(t, `>\s*Search\s*<`, bar)
	require.NotContains(t, bar, `data-action="rescan"`, "the RootFolder rescan is the library page's, not the movie's")
	require.NotContains(t, bar, `data-action="set-monitored"`, "monitoring is the bookmark on the title")
	require.Contains(t, bar, `<svg`, "every toolbar button has its icon")
	require.NotContains(t, bar, `data-slot="menubar"`, "an item page's bar has no menus")

	dryRun := requireTag(t, bar, `data-action="rename-dry-run"`, `data-slot="button"`, `type="submit"`, `form="rename-dry-run-nerve"`, `title="Preview Rename (dry run)"`)
	require.Contains(t, dryRun, `data-toolbar-button`)
	require.Contains(t, bar, `action="/library/default/movie/nerve/rename"`)
	require.Contains(t, bar, `name="rename" value="dryRun"`)
	require.Regexp(t, `>\s*Preview\s*<`, bar)
	apply := requireTag(t, bar, `data-action="rename-apply"`, `data-slot="button"`, `type="submit"`, `form="rename-nerve"`)
	require.Contains(t, apply, `data-toolbar-button`)
	require.NotContains(t, apply, `title=`, "Rename says the whole action itself")
	require.Contains(t, bar, `name="rename" value="apply"`)
	require.Regexp(t, `>\s*Rename\s*<`, bar, "the apply button reads plain \"Rename\"")
	for _, id := range []string{"search-nerve", "rename-dry-run-nerve", "rename-nerve"} {
		requireTag(t, bar, `id="`+id+`"`, `hidden`)
	}
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

// TestMoviePageListsTheFilesSubtitlesAndItsTranscode: the item page reads
// the loop's blocks on its file (loop spec §6.9, §7.7; F8.7) -- a Subtitles
// table with the block's phase and one row per language (state, score,
// provider, next search, last error), and the Files table's Transcode
// column from status.transcode.
func TestMoviePageListsTheFilesSubtitlesAndItsTranscode(t *testing.T) {
	srv, _ := movieFixture(t, false)
	body := detailPage(t, srv, "/library/default/movie/nerve")
	sub := section(t, body, `data-section="subtitles"`)
	require.Contains(t, sub, ">Wanted<", "the block's phase")
	en := subtitleRow(t, sub, "en")
	require.Contains(t, en, ">downloaded<")
	require.Contains(t, en, "92/100")
	require.Contains(t, en, ">opensubtitles<")
	es := subtitleRow(t, sub, "es")
	require.Contains(t, es, ">pending<")
	require.Contains(t, es, "2026-10-12")
	require.Contains(t, es, "every eligible provider is throttled")
	require.NotContains(t, es, "opensubtitles", "each row is its own language's")
	require.Contains(t, section(t, body, `data-section="files"`), "Running", "the file's transcode phase")
}

// subtitleRow is the Subtitles table's row for lang, up to the next row.
func subtitleRow(t *testing.T, sub, lang string) string {
	t.Helper()
	anchor := `data-subtitle="` + lang + `"`
	at := strings.Index(sub, anchor)
	require.GreaterOrEqual(t, at, 0, "no row carries %s", anchor)
	rest := sub[at+len(anchor):]
	if end := strings.Index(rest, `data-subtitle="`); end >= 0 {
		rest = rest[:end]
	}
	return rest
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

// TestRenameActionCreatesADryRunOrApplyScanOfTheMoviesFolder: the per-item
// "Rename" actions (probe-driven naming design §5) create a LibraryScan of
// the movie's own folder with spec.rename set to the posted mode, exactly as
// "Refresh & Scan" restricts its rescan.
func TestRenameActionCreatesADryRunOrApplyScanOfTheMoviesFolder(t *testing.T) {
	srv, c := movieFixture(t, true)
	rec := postForm(t, srv, "/library/default/movie/nerve/rename",
		url.Values{"return": {"/library/default/movie/nerve"}, "rename": {"dryRun"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/default/movie/nerve", rec.Header().Get("Location"))

	var scans catalogv1.LibraryScanList
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 1)
	require.Equal(t, "movies", scans.Items[0].Spec.RootFolderRef)
	require.Equal(t, "Nerve (2016) {tmdb-328387}", scans.Items[0].Spec.Subpath, "restricted to the movie's own folder")
	require.Equal(t, catalogv1.ScanRenameDryRun, scans.Items[0].Spec.Rename)
	require.Equal(t, actions.OriginUI, scans.Items[0].Labels[actions.LabelOrigin])

	rec = postForm(t, srv, "/library/default/movie/nerve/rename",
		url.Values{"return": {"/library/default/movie/nerve"}, "rename": {"apply"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Len(t, scans.Items, 2)
	applied := scans.Items[0]
	if applied.Spec.Rename != catalogv1.ScanRenameApply {
		applied = scans.Items[1]
	}
	require.Equal(t, catalogv1.ScanRenameApply, applied.Spec.Rename)
}

func TestRenameActionRefusesAnItemWithNoFolder(t *testing.T) {
	srv, c := movieFixture(t, true)
	var movie catalogv1.Movie
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &movie))
	movie.Status.Path = ""
	require.NoError(t, c.Update(t.Context(), &movie))

	rec := postForm(t, srv, "/library/default/movie/nerve/rename",
		url.Values{"return": {"/library/default/movie/nerve"}, "rename": {"dryRun"}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var scans catalogv1.LibraryScanList
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Empty(t, scans.Items, "nothing on disk yet: nothing is created")
}

func TestRenameActionRefusesAnUnknownMode(t *testing.T) {
	srv, c := movieFixture(t, true)
	rec := postForm(t, srv, "/library/default/movie/nerve/rename",
		url.Values{"return": {"/library/default/movie/nerve"}, "rename": {"sideways"}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var scans catalogv1.LibraryScanList
	require.NoError(t, c.List(t.Context(), &scans, client.InNamespace("default")))
	require.Empty(t, scans.Items)
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

// A series with nothing on disk and nothing wanted -- every series right
// after it is added, since nothing is monitored by default -- reads
// Unmonitored on its page, not Missing; one with every wanted episode on
// disk reads Downloaded.
func TestSeriesPageStatusReadsItsEpisodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hasFile bool
		missing int32
		want    string
	}{
		{"quiet", false, 0, "Unmonitored"},
		{"complete", true, 0, "Downloaded"},
		{"gaps", true, 2, "Missing"},
	} {
		item := projection.LibraryItem{
			Ref: types.NamespacedName{Namespace: "default", Name: tc.name}, Kind: commonv1.MediaKindSeries, Tab: projection.TabTV,
			Title: tc.name, Monitored: true, Phase: "Ready", HasFile: tc.hasFile, MissingEpisodes: tc.missing,
		}
		srv := ui.NewServer(t.Context(), ui.Options{
			Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
		})
		body := detailPage(t, srv, "/library/default/series/"+tc.name)
		require.Contains(t, section(t, body, `data-fact="status"`), tc.want, tc.name)
	}
}

// countingReader is the fixture's reader with its MediaFile Gets and its
// Lists of a container's children (episodes, albums, books, issues)
// counted, so a test can say what a page render read; the Edit dialog's
// QualityProfile List is not counted.
type countingReader struct {
	client.Reader
	fileGets, lists int
}

func (c *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*catalogv1.MediaFile); ok {
		c.fileGets++
	}
	return c.Reader.Get(ctx, key, obj, opts...)
}

func (c *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch list.(type) {
	case *catalogv1.EpisodeList, *catalogv1.AlbumList, *catalogv1.BookList, *catalogv1.IssueList:
		c.lists++
	}
	return c.Reader.List(ctx, list, opts...)
}

// bigSeries is a series of n episodes, each with a file of 1 GiB under its
// season folder, with its rollup saying so (status.episodeFileCount, the
// seasons' sizes) unless files overrides the count the rollup reports.
func bigSeries(name string, n int, files int32) []client.Object {
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       catalogv1.SeriesSpec{TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: "tv", Monitored: new(true)},
		Status: catalogv1.SeriesStatus{
			Path: "/data/tv/" + name, EpisodeFileCount: files,
			Seasons: []catalogv1.SeasonStatus{{Number: 1, EpisodeCount: int32(n), EpisodeFileCount: files, SizeBytes: int64(n) << 30}}, //nolint:gosec // a test's count
		},
	}
	objs := []client.Object{series}
	for i := 1; i <= n; i++ {
		ep := fmt.Sprintf("%s-s01e%04d", name, i)
		objs = append(objs,
			&catalogv1.Episode{
				ObjectMeta: metav1.ObjectMeta{Name: ep, Namespace: "default"},
				Spec:       catalogv1.EpisodeSpec{SeriesRef: name, SeasonNumber: 1, EpisodeNumber: int32(i)}, //nolint:gosec // a test's count
				Status:     catalogv1.EpisodeStatus{HasFile: true, FileRef: new(ep + "-file")},
			},
			&catalogv1.MediaFile{
				ObjectMeta: metav1.ObjectMeta{Name: ep + "-file", Namespace: "default"},
				Spec: catalogv1.MediaFileSpec{
					MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: ep},
					Path:     fmt.Sprintf("/data/tv/%s/Season 01/%s.mkv", name, ep), SizeBytes: 1 << 30,
				},
			})
	}
	return objs
}

// containerServer is a server over objs through a counting reader, with
// the series and artist cards the pages are reached from.
func containerServer(t testing.TB, objs []client.Object, items ...projection.LibraryItem) (*ui.Server, *countingReader) {
	t.Helper()
	scheme, err := ui.NewReaderScheme() // libraryTestScheme, for a benchmark too
	require.NoError(t, err)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&catalogv1.Episode{}, ui.EpisodeSeriesRefField, ui.IndexEpisodeBySeriesRef).
		WithIndex(&catalogv1.Album{}, ui.AlbumArtistRefField, ui.IndexAlbumByArtistRef).
		WithObjects(objs...).Build()
	reader := &countingReader{Reader: c}
	return ui.NewServer(t.Context(), ui.Options{
		Reader:  reader,
		Library: func(context.Context) []projection.LibraryItem { return items },
	}), reader
}

func seriesCard(name string, files int32, size int64) projection.LibraryItem {
	return projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: name}, Kind: commonv1.MediaKindSeries, Tab: projection.TabTV,
		Title: name, Monitored: true, QualityProfileRef: "hd", EpisodeFileCount: files, SizeBytes: size,
	}
}

// A container's page reads its children's files for its Size fact and its
// Delete dialog only up to a bound (ui.maxContainerFiles, 500): a series the
// rollup counts over it is read no further -- no episode List, no MediaFile
// Get -- its hero shows no Size fact, and its Delete dialog counts from the
// rollup, the count and the seasons' size its card carries; a series under
// it reads one file per episode, as before. An artist's rollup counts
// albums, not tracks, so its albums are listed and the Gets are skipped once
// the tracks' files run over the bound; its dialog, with no rollup count of
// files at hand, names the folder alone.
func TestContainerFilesAreBoundedByTheRollup(t *testing.T) {
	// Over the bound: the rollup says 501 files, and the cluster holds the
	// three the fake would otherwise read.
	objs := bigSeries("big", 3, 501)
	srv, reader := containerServer(t, objs, seriesCard("big", 501, 5<<30))
	page := detailPage(t, srv, "/library/default/series/big")
	require.NotContains(t, page, `data-fact="size"`, "no Size fact from files the page did not read")
	require.Regexp(t, `data-delete-files>[^<]*501 file\(s\), 5\.00 GiB<`, page, "the Delete dialog counts from the rollup")
	require.Contains(t, page, `data-delete-folder>/data/tv/big<`)
	require.Zero(t, reader.fileGets, "no file is read for a container over the bound")
	require.Zero(t, reader.lists, "nor are its children listed when the rollup already says so")

	// Under the bound: every file is read, once.
	srv, reader = containerServer(t, bigSeries("small", 3, 3), seriesCard("small", 3, 3<<30))
	page = detailPage(t, srv, "/library/default/series/small")
	require.Contains(t, section(t, page, `data-fact="size"`), "3.00 GiB")
	require.Regexp(t, `data-delete-files>[^<]*3 file\(s\), 3\.00 GiB<`, page)
	require.Equal(t, 3, reader.fileGets)

	// An artist whose albums' tracks name more files than the bound: the
	// albums are listed (the rollup counts albums, a floor), the Gets are
	// not made.
	artist := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "prolific", Namespace: "default"},
		Spec:       catalogv1.ArtistSpec{MusicBrainzID: "mb-p", QualityProfileRef: "lossless", RootFolderRef: "music"},
		Status:     catalogv1.ArtistStatus{Path: "/data/music/Prolific", AlbumFileCount: 3},
	}
	albums := []client.Object{artist}
	for a := 1; a <= 3; a++ {
		album := &catalogv1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("prolific-%d", a), Namespace: "default"},
			Spec:       catalogv1.AlbumSpec{ArtistRef: "prolific", ReleaseGroupID: fmt.Sprintf("rg-%d", a)},
			Status:     catalogv1.AlbumStatus{TrackFileCount: 200},
		}
		for tr := 1; tr <= 200; tr++ {
			album.Status.Tracks = append(album.Status.Tracks, catalogv1.Track{RecordingID: fmt.Sprintf("rec-%d-%d", a, tr), FileRef: new(fmt.Sprintf("prolific-%d-%03d", a, tr))})
		}
		albums = append(albums, album)
	}
	srv, reader = containerServer(t, albums, projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "prolific"}, Kind: commonv1.MediaKindArtist, Tab: projection.TabMusic,
		Title: "Prolific", Monitored: true, QualityProfileRef: "lossless",
	})
	page = detailPage(t, srv, "/library/default/artist/prolific")
	require.NotContains(t, page, `data-fact="size"`)
	require.NotContains(t, page, `data-delete-files`, "an artist's rollup counts albums, not files, so the dialog names the folder alone")
	require.Contains(t, page, `data-delete-folder>/data/music/Prolific<`)
	require.Zero(t, reader.fileGets, "600 track files are over the bound")
	require.Equal(t, 2, reader.lists, "the albums were listed to learn that, once beside the page's own albums table")
}

// BenchmarkContainerFiles times a series page's render over the fake
// reader -- the slow end, a JSON decode per Get -- at 50 and 500 files
// (the bound, every file read) and at 1,100 (over it, nothing read), the
// numbers ui.maxContainerFiles' doc records: 2026-11-10, on an i7-10750H,
// 3.95 ms, 12.7 ms and 2.9 ms a render.
func BenchmarkContainerFiles(b *testing.B) {
	for _, n := range []int{50, 500, 1100} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			srv, reader := containerServer(b, bigSeries("bench", n, int32(n)), seriesCard("bench", int32(n), int64(n)<<30)) //nolint:gosec // a benchmark's count
			req := httptest.NewRequest(http.MethodGet, "/library/default/series/bench", nil)
			b.ResetTimer()
			for b.Loop() {
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					b.Fatal(rec.Code)
				}
			}
			b.ReportMetric(float64(reader.fileGets)/float64(b.N), "gets/op")
		})
	}
}
