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

// seriesFixture is one series with two seasons on the cluster -- season 2
// unmonitored by a spec override the status rollup has not caught up with
// -- plus season 1's two episodes and one of season 2's, and the library
// card the projection would build for it.
func seriesFixture(t *testing.T) (*ui.Server, projection.LibraryItem) {
	t.Helper()
	return seriesFixtureOver(t, func(r client.Reader) client.Reader { return r })
}

// seriesFixtureWithActions is seriesFixture with actions over its client,
// which it also returns, for a test that writes.
func seriesFixtureWithActions(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	var c client.Client
	srv, _ := seriesFixtureBuild(t, func(r client.Reader) client.Reader { return r }, func(cl client.Client) { c = cl })
	return srv, c
}

// seriesFixtureOver is seriesFixture with the server's reader wrapped.
func seriesFixtureOver(t *testing.T, wrap func(client.Reader) client.Reader) (*ui.Server, projection.LibraryItem) {
	t.Helper()
	return seriesFixtureBuild(t, wrap, nil)
}

// seriesFixtureBuild builds the series fixture; withActions, when set, is
// given the client and the server gets actions over it.
func seriesFixtureBuild(t *testing.T, wrap func(client.Reader) client.Reader, withActions func(client.Client)) (*ui.Server, projection.LibraryItem) {
	t.Helper()
	aired := metav1.NewTime(time.Date(2022, time.September, 21, 0, 0, 0, 0, time.UTC))
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default"},
		Spec: catalogv1.SeriesSpec{
			TvdbID: 393189, QualityProfileRef: "web-1080p", RootFolderRef: "tv",
			Seasons: []catalogv1.SeasonSpec{{Number: 2, Monitored: new(false)}},
		},
		Status: catalogv1.SeriesStatus{
			Metadata: &catalogv1.SeriesMetadata{Title: "Andor", Year: 2022, SeasonTitles: []catalogv1.SeasonTitle{{Season: 1, Title: "Book 1: Water"}}},
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
	withAudio := func(ep *catalogv1.Episode) *catalogv1.Episode {
		ep.Status.Audio = &catalogv1.AudioState{
			Wanted: []string{"en", "ja"}, Present: []string{"ja"}, Missing: []string{"en"},
			Graft: "failed", Reason: "AlignmentRejected: coverage 12%",
		}
		return ep
	}
	other := &catalogv1.Episode{ // another series' episode, never listed here
		ObjectMeta: metav1.ObjectMeta{Name: "silo-s01e01", Namespace: "default"},
		Spec:       catalogv1.EpisodeSpec{SeriesRef: "silo", SeasonNumber: 1, EpisodeNumber: 1},
	}
	// Kassa's file is on the cluster, with everything the season table's
	// file columns read: four frozen languages (one past the three shown),
	// a score, an HDR10 10-bit HEVC probe with two audio tracks, a skipped
	// transcode, a subtitle still wanted and two skip segments. That Would
	// Be Me has a file the Episode names no MediaFile for, so its row has
	// no file columns.
	kassa := episode("andor-s01e01", 1, 1, "Kassa", true)
	kassa.Status.FileRef = new("andor-s01e01-file")
	kassaFile := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e01-file", Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			Path: "/data/tv/Andor/Season 01/Andor - S01E01 - Kassa.mkv", Quality: commonv1.Quality{Name: "WEBDL-1080p"},
			Languages: []string{"English", "Japanese", "French", "German"}, FormatScore: 1250,
		},
		Status: catalogv1.MediaFileStatus{
			MediaInfo: &commonv1.MediaInfo{
				VideoCodec: "hevc", VideoProfile: "Main 10", VideoBitDepth: 10, Hdr: commonv1.HdrFormatHDR10,
				Width: 1920, Height: 1080, FpsMilli: 23976, VideoBitrateKbps: 5400, Container: "matroska",
				Audio: []commonv1.AudioStream{
					{Codec: "eac3", Channels: 6, Language: "eng"},
					{Codec: "aac", Channels: 2, Language: "jpn", Title: "Commentary"},
				},
			},
			Transcode: &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseSkipped, Reason: "Excluded", Message: "transcode.clustarr.io/exclude on the series"},
			Subtitles: &catalogv1.SubtitlesStatus{Phase: catalogv1.SubtitlesPhaseWanted, Items: []catalogv1.SubtitleItemStatus{
				{LangKey: "en", State: catalogv1.SubtitleStateDownloaded, Provider: "opensubtitles", Score: 180, ScoreOutOf: 360},
				{LangKey: "fr", State: catalogv1.SubtitleStatePending},
			}},
			Markers: &catalogv1.FileMarkers{Result: catalogv1.MarkersFound, Segments: []catalogv1.MarkerSegment{
				{Kind: catalogv1.MarkerIntro, StartMs: 228664, EndMs: 246143},
				{Kind: catalogv1.MarkerCredits, StartMs: 3431000, EndMs: 3480000},
			}},
		},
	}
	reader := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(series,
		withAudio(episode("andor-s01e02", 1, 2, "That Would Be Me", true)),
		kassa, kassaFile,
		episode("andor-s02e01", 2, 1, "One Year Later", false),
		other,
	).WithIndex(&catalogv1.Episode{}, ui.EpisodeSeriesRefField, ui.IndexEpisodeBySeriesRef).Build()
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "andor"}, Kind: commonv1.MediaKindSeries,
		Tab: projection.TabTV, Title: "Andor", Year: 2022, QualityProfileRef: "web-1080p", Monitored: true,
	}
	opts := ui.Options{
		Reader:  wrap(reader),
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	}
	if withActions != nil {
		withActions(reader)
		opts.Actions = actions.New(reader)
	}
	srv := ui.NewServer(t.Context(), opts)
	return srv, item
}

// episodeSelectors records the field selector of every Episode List.
type episodeSelectors struct {
	client.Reader
	got []string
}

func (r *episodeSelectors) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*catalogv1.EpisodeList); ok {
		lo := (&client.ListOptions{}).ApplyOptions(opts)
		sel := ""
		if lo.FieldSelector != nil {
			sel = lo.FieldSelector.String()
		}
		r.got = append(r.got, sel)
	}
	return r.Reader.List(ctx, list, opts...)
}

// Expanding a season lists that series' episodes, selected by
// spec.seriesRef (the ui cache's index, Episode's selectable field on the
// apiserver), not every Episode in the namespace -- 15,630 on the owner's
// library, deep-copied per expand.
func TestSeasonRouteSelectsTheSeriesEpisodes(t *testing.T) {
	rec := &episodeSelectors{}
	srv, _ := seriesFixtureOver(t, func(r client.Reader) client.Reader { rec.Reader = r; return rec })
	req := httptest.NewRequest(http.MethodGet, "/library/default/series/andor/seasons/1", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{ui.EpisodeSeriesRefField + "=andor"}, rec.got, "the namespace's episodes were listed, not the series'")
	require.Equal(t, 2, strings.Count(w.Body.String(), `data-episode=`))
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
	requireTag(t, body, `data-season="1"`, `data-monitored="true"`, `data-episodes="12"`, `data-files="8"`)
	// The spec override wins over the rollup.
	requireTag(t, body, `data-season="2"`, `data-monitored="false"`, `data-episodes="12"`, `data-files="0"`)
	require.Contains(t, body, `hx-get="/library/default/series/andor/seasons/1"`)
	require.Contains(t, body, `hx-trigger="collapsible-open-change[detail.open] once"`, "a season's episodes load on its collapsible's first open")
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
	requireTag(t, partial, `data-episode="1"`, `data-monitored="true"`, `data-hasfile="true"`, `data-quality="WEBDL-1080p"`, `data-phase="Imported"`)
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
	requireTag(t, page, `data-episode="1"`, `data-monitored="true"`, `data-hasfile="false"`)
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

// seriesActionFixture is seriesFixture with a writer, so the toggles have
// somewhere to write; writer doubles as the reader.
func seriesActionFixture(t *testing.T, withActions bool) (*ui.Server, client.Client) {
	t.Helper()
	aired := metav1.NewTime(time.Date(2022, time.September, 21, 0, 0, 0, 0, time.UTC))
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default"},
		Spec: catalogv1.SeriesSpec{
			TvdbID: 393189, QualityProfileRef: "web-1080p", RootFolderRef: "tv",
			Seasons: []catalogv1.SeasonSpec{{Number: 2, Monitored: new(false)}},
		},
		Status: catalogv1.SeriesStatus{Seasons: []catalogv1.SeasonStatus{
			{Number: 1, Monitored: true, EpisodeCount: 12, EpisodeFileCount: 8},
			{Number: 2, Monitored: true, EpisodeCount: 12, NextAiring: &aired},
		}},
	}
	episode := &catalogv1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e01", Namespace: "default"},
		Spec:       catalogv1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: 1, Monitored: new(true)},
		Status:     catalogv1.EpisodeStatus{Title: "Kassa", HasFile: true, FileQuality: &commonv1.Quality{Name: "WEBDL-1080p"}},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(series, episode).Build()
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "andor"}, Kind: commonv1.MediaKindSeries,
		Tab: projection.TabTV, Title: "Andor", QualityProfileRef: "web-1080p", Monitored: true,
	}
	opts := ui.Options{
		Reader:  c,
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	}
	if withActions {
		opts.Actions = actions.New(c)
	}
	return ui.NewServer(t.Context(), opts), c
}

func postForm(t *testing.T, srv *ui.Server, path string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// A season toggle from htmx replies with the re-rendered season component
// carrying the value just written; a plain form post redirects as every
// other action does. Either way spec.seasons holds the new entry.
func TestSeasonMonitorToggleRepliesWithTheComponentToHTMX(t *testing.T) {
	srv, c := seriesActionFixture(t, true)
	rec := postForm(t, srv, "/library/default/series/andor/seasons/2/monitor", url.Values{"monitored": {"true"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html")
	requireTag(t, body, `data-season="2"`, `data-monitored="true"`)
	require.NotContains(t, body, `data-action-error`)

	var got catalogv1.Series
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "andor"}, &got))
	require.Equal(t, []catalogv1.SeasonSpec{{Number: 2, Monitored: new(true)}}, got.Spec.Seasons)

	rec = postForm(t, srv, "/library/default/series/andor/seasons/1/monitor",
		url.Values{"monitored": {"false"}, "return": {"/library/default/series/andor"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/default/series/andor", rec.Header().Get("Location"))
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "andor"}, &got))
	require.Equal(t, map[int32]bool{1: false, 2: true}, func() map[int32]bool {
		m := map[int32]bool{}
		for _, s := range got.Spec.Seasons {
			m[s.Number] = *s.Monitored
		}
		return m
	}())
}

// An episode toggle from htmx replies with the re-rendered row.
func TestEpisodeMonitorToggleRepliesWithTheRowToHTMX(t *testing.T) {
	srv, c := seriesActionFixture(t, true)
	rec := postForm(t, srv, "/library/default/episode/andor-s01e01/monitor", url.Values{"monitored": {"false"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html")
	requireTag(t, body, `data-episode="1"`, `data-monitored="false"`, `data-hasfile="true"`, `data-quality="WEBDL-1080p"`)
	require.Contains(t, body, "Kassa")
	var got catalogv1.Episode
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "andor-s01e01"}, &got))
	require.False(t, *got.Spec.Monitored)
}

// A toggle that fails from htmx gets its component back with the error in
// it -- swapped in, so the page stays usable and the failure is visible
// where it happened -- rather than the bare error page a form post gets.
func TestFailedToggleFromHTMXRepliesWithTheComponentAndAnInlineError(t *testing.T) {
	srv, _ := seriesActionFixture(t, false) // no writer: every action is ErrNoWriter
	rec := postForm(t, srv, "/library/default/series/andor/seasons/2/monitor", url.Values{"monitored": {"true"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	// The component shows the value that still stands.
	requireTag(t, body, `data-season="2"`, `data-monitored="false"`)
	require.Contains(t, body, `data-action-error="no-writer"`)

	rec = postForm(t, srv, "/library/default/episode/andor-s01e01/monitor", url.Values{"monitored": {"false"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	requireTag(t, body, `data-episode="1"`, `data-monitored="true"`)
	require.Contains(t, body, `data-action-error="no-writer"`)

	rec = postForm(t, srv, "/library/default/series/andor/seasons/2/monitor", url.Values{"monitored": {"true"}}, false)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "a form post keeps the error page")
}

// The item header offers "Refresh metadata" (design
// 2026-09-23-library-page-design, "Metadata refresh"): a form post to
// /library/{ns}/{kind}/{name}/refresh that writes the refresh annotation
// through the UI's action and redirects like every other action.
func TestRefreshMetadataButtonPostsTheAnnotationAndRedirects(t *testing.T) {
	srv, c := seriesActionFixture(t, true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/series/andor", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action="refresh-metadata"`)
	require.Contains(t, rec.Body.String(), `action="/library/default/series/andor/refresh"`)

	rec = postForm(t, srv, "/library/default/series/andor/refresh", url.Values{"return": {"/library/default/series/andor"}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/default/series/andor", rec.Header().Get("Location"))
	var got catalogv1.Series
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "andor"}, &got))
	require.NotEmpty(t, got.Annotations[catalogv1.AnnotationRefreshMetadata])

	// The generic detail page (a movie) carries the button too.
	movie := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Heat", Monitored: true,
	}
	plain := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{movie} },
	})
	rec = httptest.NewRecorder()
	plain.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/movie/heat", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `action="/library/default/movie/heat/refresh"`)
}

// An episode row shows the dub its file lacks and what the graft is doing,
// a failure's reason as the tooltip (anime dual-audio spec §9).
func TestAnEpisodeRowShowsItsAudio(t *testing.T) {
	srv, _ := seriesFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/library/default/series/andor/seasons/1", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `data-audio="no en dub · graft failed"`)
	require.Contains(t, w.Body.String(), `title="AlignmentRejected: coverage 12%"`)
	require.Equal(t, 1, strings.Count(w.Body.String(), "data-audio="), "only the episode with something to say")
}

// An episode row carries its file's columns (owner, 2026-11-04): the
// frozen languages, three as badges and the rest behind a count, the
// format score, the probe's video and first audio track with the count of
// the others, and the transcode, subtitles and markers as cells, each
// naming its detail in the tooltip. Only the row whose Episode names a
// MediaFile has them; the toggle's reply keeps them, so a swapped row
// loses nothing.
func TestAnEpisodeRowShowsItsFile(t *testing.T) {
	srv, _ := seriesFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/library/default/series/andor/seasons/1", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	for _, want := range []string{
		`data-languages="English,Japanese,French,German"`,
		`data-more-languages="1"`, `title="German"`,
		`data-score="+1250"`,
		`data-video="HEVC 10-bit HDR10"`, `title="Main 10, 1920x1080, 23.976 fps, 5400 kbps, HDR10, matroska"`,
		`data-audio-codec="EAC3 - 5.1 +1"`, "eng: EAC3 - 5.1\njpn: AAC - 2.0 (Commentary)",
		`data-transcode="Skipped"`, `title="Excluded; transcode.clustarr.io/exclude on the series"`,
		`data-subtitles="Wanted fr"`, "en: downloaded (opensubtitles) 180/360\nfr: pending",
		`data-markers="intro · credits"`, `title="intro 3:48–4:06 (TheIntroDB), credits 57:11–58:00 (TheIntroDB)"`,
	} {
		require.Contains(t, body, want)
	}
	for _, attr := range []string{"data-languages=", "data-score=", "data-video=", "data-transcode=", "data-subtitles=", "data-markers="} {
		require.Equal(t, 1, strings.Count(body, attr), "%s: only the episode whose Episode names a file", attr)
	}
	for _, head := range []string{"Languages", "Score", "Video", "Audio", "Transcode", "Subtitles", "Markers"} {
		require.Contains(t, body, ">"+head+" <", "column %s", head)
	}

	srv, c := seriesFixtureWithActions(t)
	form := url.Values{"monitored": {"false"}}
	req = httptest.NewRequest(http.MethodPost, "/library/default/episode/andor-s01e01/monitor", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `data-video="HEVC 10-bit HDR10"`, "the toggle's reply keeps the file columns")
	var ep catalogv1.Episode
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "andor-s01e01"}, &ep))
	require.NotNil(t, ep.Spec.Monitored)
	require.False(t, *ep.Spec.Monitored)
}

// A season the provider names shows that name beside its label, as the
// Plex provider titles it; one it does not shows the label alone.
func TestSeasonHeaderShowsTheSeasonsOwnName(t *testing.T) {
	srv, _ := seriesFixture(t)
	body := detailPage(t, srv, "/library/default/series/andor")
	one := section(t, body, `id="season-1"`)
	require.Contains(t, one, "Season 1")
	require.Contains(t, one, `data-season-title`)
	require.Contains(t, one, "Book 1: Water")
	two := section(t, body, `id="season-2"`)
	require.Contains(t, two, "Season 2")
	require.NotContains(t, two, `data-season-title`)
}
