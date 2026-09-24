//go:build e2e

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

// Scenario 18 -- the Plex Metadata Provider (Task D2, design spec §D.6).
//
// ui/plex (Task D1, ceafb8a..b8fd3e6) mounts two read-only roots on the ui
// service's own HTTP server: /plex/movies (type 1) and /plex/tv (types 2,
// 3, 4), over the same projection.Index the Library page already reads
// from. This file walks the flow research §9 infers a real PMS follows,
// against ui's real server rather than against ui/plex's handlers
// directly the way ui/plex/*_test.go's golden-file unit tests do:
//
//  1. GET /plex/movies returns the MediaProvider root (identifier, the one
//     declared type).
//  2. POST /plex/movies/library/metadata/matches with the fixture Movie's
//     tmdb guid (spec §D.4 rule 1: guid beats title) returns exactly one
//     result.
//  3. GET that result's own metadata key returns the full object: the
//     resolved title, and an absolute thumb URL under the ui's own
//     --external-url (spec §D.5; D1's own doc comment: "thumb/art are
//     absolute under the external URL").
//  4. GET that thumb URL returns 200 with an image/* Content-Type -- see
//     the "artwork leg" comment below for why this step is gated behind a
//     poll rather than a bare assertion.
//  5. GET /plex/tv/library/metadata/{seriesKey}/children lists the fixture
//     Series' seasons.
//  6. GET .../grandchildren with X-Plex-Container-Size: 1 windows to one
//     episode while totalSize still reports the true episode count (spec
//     §D.2's paging rule).
//
// The fixture Movie and Series are the same ones TestLibraryRescan
// (libraryscan_test.go, fixtureTmdbID = 27205, Inception, the in-cluster
// TMDB stub) and TestSeriesAndEpisodes (series_test.go, tvdb 121361, Game
// of Thrones standard numbering, two episodes both in season 1, the
// in-cluster TVDB stub) already use -- created directly here rather than
// through a RootFolder scan, since the Plex provider reads only
// Movie/Series/Episode status, never MediaFile.
//
// `ui` is reached over `kubectl port-forward` (portForwardService,
// test/e2e/helpers_test.go), exactly as TestUIPipelineAndDownloadsPages
// does and for the identical reason (main_test.go's own package doc
// comment: the test binary runs on the HOST and kind publishes no
// extraPortMappings for any Service).
//
// Build-tagged e2e. Per CLAUDE.md's standing instruction this file is
// WRITTEN, NEVER RUN: there is no kind cluster in this environment, `make
// e2e` is not invoked, and Phase H is what eventually executes it.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// plexMovieType is Plex's numeric "movie" provider type (research §2's
// table; spec §D.1).
const plexMovieType = 1

// plexArtworkWaitTimeout bounds the poll for catalogarr's artwork Fetcher
// to have downloaded the fixture Movie's poster before the thumb-fetch leg
// either runs or names why it could not (see that step's own comment).
const plexArtworkWaitTimeout = 3 * time.Minute

// plexProviderRoot is GET {root}'s body (research §2, spec §D.1/§D.2),
// reduced to the fields this scenario asserts on.
type plexProviderRoot struct {
	MediaProvider struct {
		Identifier string `json:"identifier"`
		Types      []struct {
			Type int `json:"type"`
		} `json:"Types"`
	} `json:"MediaProvider"`
}

// plexMetadataItem is one entry of a MediaContainer's own Metadata[]
// (research §5), reduced to the fields this scenario reads.
type plexMetadataItem struct {
	RatingKey string `json:"ratingKey"`
	Key       string `json:"key"`
	Guid      string `json:"guid"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Thumb     string `json:"thumb"`
}

// plexMediaContainer is the whole response body for match, metadata,
// children and grandchildren (research §3, spec §D.2): every route this
// scenario calls except the provider root itself.
type plexMediaContainer struct {
	MediaContainer struct {
		Offset     int                `json:"offset"`
		TotalSize  int                `json:"totalSize"`
		Size       int                `json:"size"`
		Identifier string             `json:"identifier"`
		Metadata   []plexMetadataItem `json:"Metadata"`
	} `json:"MediaContainer"`
}

// TestPlexProvider is scenario 18. See this file's package doc comment for
// the full flow and why it is written, never run.
func TestPlexProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	// --- Fixtures: a Movie the in-cluster TMDB stub answers for, and a
	// standard-numbering Series the in-cluster TVDB stub answers for. Both
	// routes are proven elsewhere in this suite (TestLibraryRescan,
	// TestSeriesAndEpisodes); this scenario only needs the objects
	// reconciled, not a MediaFile behind either of them.
	movieRF := newRootFolder(ctx, t, "e2e-plex-movie-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-plex-movie", fixtureTmdbID, QualityProfileName, movieRF.Name,
		catalogv1alpha1.MinimumAvailabilityAnnounced)
	liveMovie := waitForMovieSettled(ctx, t, movie, "Inception")

	seriesRF := newRootFolder(ctx, t, "e2e-plex-series-rf", catalogv1alpha1.RootFolderKindSeries, "tv")
	series := createSeries(ctx, t, seriesRF.Name, 121361, catalogv1alpha1.SeriesTypeStandard)
	liveSeries := waitForSeriesReady(ctx, t, series, fixtureEpisodesPerSeries)

	base, _ := portForwardService(ctx, t, "ui", uiServicePort)
	client := &http.Client{Timeout: 15 * time.Second}

	// --- 1. GET /plex/movies -> the MediaProvider root.
	var root plexProviderRoot
	status, err := plexGetJSON(ctx, client, base+"/plex/movies", nil, &root)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "GET /plex/movies")
	require.Equal(t, "tv.plex.agents.custom.clustarr.movies", root.MediaProvider.Identifier)
	require.Len(t, root.MediaProvider.Types, 1, "the movies root declares type 1 only (spec §D.1)")
	require.Equal(t, plexMovieType, root.MediaProvider.Types[0].Type)

	// --- 2. POST .../matches with the fixture's tmdb guid -> exactly the
	// Movie above. A real PMS always sends title too (research §4's table:
	// "Support Required? Yes"), guid or no.
	matchReq := map[string]any{
		"type":  plexMovieType,
		"guid":  fmt.Sprintf("tmdb://%d", fixtureTmdbID),
		"title": "Inception",
		"year":  2010,
	}
	var matched plexMediaContainer
	status, err = plexPostJSON(ctx, client, base+"/plex/movies/library/metadata/matches", matchReq, nil, &matched)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "POST /plex/movies/library/metadata/matches")
	require.Len(t, matched.MediaContainer.Metadata, 1, "matches=%+v", matched.MediaContainer.Metadata)
	result := matched.MediaContainer.Metadata[0]
	require.Equal(t, string(liveMovie.UID), result.RatingKey,
		"a match result's ratingKey is the Movie's own UID (spec §D.3)")
	require.NotEmpty(t, result.Key)

	// --- 3. GET the match result's own key -> the full object.
	var metaResp plexMediaContainer
	status, err = plexGetJSON(ctx, client, base+"/plex/movies"+result.Key, nil, &metaResp)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "GET /plex/movies%s", result.Key)
	require.Len(t, metaResp.MediaContainer.Metadata, 1)
	md := metaResp.MediaContainer.Metadata[0]
	require.Equal(t, "Inception", md.Title)

	// --- 4. The artwork leg: buildImages/thumbAndArt (ui/plex/images.go)
	// omit thumb entirely -- never blank it (spec §D.5) -- until
	// catalogarr's artwork Fetcher has actually recorded an ArtworkEntry
	// for the poster. That is a second, independent reconcile behind the
	// metadata refresh this scenario already waited for
	// (waitForMovieSettled only gates on status.metadata and
	// status.available), so poll rather than assume one settled Ready
	// pass also finished the artwork fetch.
	thumb := md.Thumb
	if thumb == "" {
		thumb = waitForPlexThumb(ctx, client, base+"/plex/movies"+result.Key)
	}
	if thumb == "" {
		// The e2e cluster has no egress (config/e2e/importarr-e2e-patch.yaml's
		// own doc comment says so plainly), and
		// pkg/metadata/clients/tmdb.posterBaseURL is the real
		// https://image.tmdb.org CDN -- test/fixtures/tmdbstub only ever
		// re-serves the recorded /movie and /find JSON, never image bytes.
		// Whether catalogarr's artwork Fetcher can reach a poster at all in
		// this cluster is therefore a fact outside app/** and config/e2e's
		// current fixtures, both out of this task's file scope, so this
		// skips by name rather than failing on a gap it cannot fix.
		t.Skipf("Movie %s never gained a poster thumb within %s: the e2e cluster has no egress to the real "+
			"image.tmdb.org CDN catalogarr's artwork Fetcher would need, and no in-cluster fixture serves image "+
			"bytes for it today -- skipping the thumb-fetch assertion rather than asserting on a signal this "+
			"environment cannot currently produce", movie.Name, plexArtworkWaitTimeout)
	}

	thumbURL, err := url.Parse(thumb)
	require.NoError(t, err, "thumb must be a valid absolute URL: %q", thumb)
	// The test process runs on the HOST (main_test.go's own package doc
	// comment), so it cannot resolve --external-url's in-cluster DNS name
	// the way a real PMS reachable from inside the cluster could -- only
	// the port-forward's own local base. thumb is served by this SAME ui
	// process either way, so rewriting host+scheme and keeping the path and
	// query reaches the identical handler a real PMS would.
	thumbReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+thumbURL.RequestURI(), nil)
	require.NoError(t, err)
	thumbResp, err := client.Do(thumbReq)
	require.NoError(t, err)
	defer func() { _ = thumbResp.Body.Close() }()
	_, _ = io.Copy(io.Discard, thumbResp.Body)
	require.Equal(t, http.StatusOK, thumbResp.StatusCode, "GET %s", thumb)
	contentType := thumbResp.Header.Get("Content-Type")
	require.True(t, strings.HasPrefix(contentType, "image/"),
		"GET %s Content-Type=%q, want image/*", thumb, contentType)

	// --- 5. GET /plex/tv/.../{seriesKey}/children -> the Series' seasons.
	// The Series' own ratingKey is its UID (spec §D.3); no match step is
	// needed, the object already exists from createSeries above.
	seriesKey := string(liveSeries.UID)

	var childrenResp plexMediaContainer
	status, err = plexGetJSON(ctx, client, base+"/plex/tv/library/metadata/"+seriesKey+"/children", nil, &childrenResp)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "GET /plex/tv/library/metadata/%s/children", seriesKey)
	require.NotEmpty(t, childrenResp.MediaContainer.Metadata, "a show's /children must list its seasons")
	for _, season := range childrenResp.MediaContainer.Metadata {
		require.Equal(t, "season", season.Type)
	}

	// --- 6. GET .../grandchildren with X-Plex-Container-Size: 1 -> exactly
	// one episode windowed, but totalSize still the true episode count
	// (spec §D.2).
	var grandchildrenResp plexMediaContainer
	status, err = plexGetJSON(ctx, client, base+"/plex/tv/library/metadata/"+seriesKey+"/grandchildren",
		map[string]string{"X-Plex-Container-Size": "1"}, &grandchildrenResp)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "GET /plex/tv/library/metadata/%s/grandchildren", seriesKey)
	require.Equal(t, 1, grandchildrenResp.MediaContainer.Size,
		"X-Plex-Container-Size: 1 must window the response to exactly one episode")
	require.EqualValues(t, fixtureEpisodesPerSeries, grandchildrenResp.MediaContainer.TotalSize,
		"totalSize must report the true episode count regardless of the page window")
	require.Len(t, grandchildrenResp.MediaContainer.Metadata, 1)
	require.Equal(t, "episode", grandchildrenResp.MediaContainer.Metadata[0].Type)
}

// waitForPlexThumb polls metadataURL until its Metadata[0].thumb is
// non-empty or plexArtworkWaitTimeout elapses, returning whichever it saw
// last -- empty on a timeout, which the caller treats as "skip, do not
// fail" (see the call site's own comment).
func waitForPlexThumb(ctx context.Context, c *http.Client, metadataURL string) string {
	var thumb string
	_ = wait.PollUntilContextTimeout(ctx, pollInterval, plexArtworkWaitTimeout, true, func(ctx context.Context) (bool, error) {
		var resp plexMediaContainer
		status, err := plexGetJSON(ctx, c, metadataURL, nil, &resp)
		if err != nil || status != http.StatusOK || len(resp.MediaContainer.Metadata) != 1 {
			//nolint:nilerr // keep polling; a port-forward hiccup or a not-yet-settled response is transient
			return false, nil
		}
		thumb = resp.MediaContainer.Metadata[0].Thumb
		return thumb != "", nil
	})
	return thumb
}

// plexGetJSON GETs targetURL, setting any of headers, and decodes a 200
// body into out (which may be nil to skip decoding). It is this file's own
// minimal JSON client rather than a reuse of ui_test.go's httpGetString: a
// real PMS is an external HTTP consumer of the documented wire contract
// (research/spec), not a caller of ui/plex's internal Go types, so this
// package deliberately does not import ui/plex for its response shapes.
func plexGetJSON(ctx context.Context, c *http.Client, targetURL string, headers map[string]string, out any) (status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w (body: %s)", targetURL, err, body)
		}
	}
	return resp.StatusCode, nil
}

// plexPostJSON is [plexGetJSON]'s POST counterpart: reqBody is marshalled
// as the request body with Content-Type application/json, plus any of
// headers.
func plexPostJSON(
	ctx context.Context, c *http.Client, targetURL string, reqBody any, headers map[string]string, out any,
) (status int, err error) {
	b, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w (body: %s)", targetURL, err, body)
		}
	}
	return resp.StatusCode, nil
}
