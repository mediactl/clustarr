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

// Package nonvideostub serves MusicBrainz, Open Library, Audnexus and
// ComicVine -- the four non-video metadata providers pkg/metadata/clients
// has real clients for -- from one process, re-serving the recorded JSON
// under testdata/metadata/{musicbrainz,openlibrary,audnexus,comicvine}/.
// Every route below is read off each client's own request-building code
// (pkg/metadata/clients/<provider>/<provider>.go's doGet calls, or, for
// MusicBrainz, its own unit test's asserted request paths -- that client
// wraps go.uploadedlobster.com/musicbrainzws2, which this package does not
// import), never guessed.
//
// # One process, four path prefixes
//
// Each client's own default base URL already differs (musicbrainz.org,
// openlibrary.org, api.audnex.us, comicvine.gamespot.com/api), and
// MetadataProvider.spec.baseURL (catalogarr/metadata/registry.go's baseURL
// helper) is a full replacement, not a suffix -- so config/e2e can point
// each of the four MetadataProviders at this ONE Service, differing only
// in path prefix: http://nonvideo-stub.clustarr-system.svc/{musicbrainz,
// openlibrary,audnexus,comicvine}. This mirrors torznabstub's "three
// personalities, selected by path" shape, one level higher: four real
// upstream APIs share a service instead of three failure modes of one.
//
// It never talks to the Internet; the e2e cluster has no egress.
package nonvideostub

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

// ComicVineAPIKey is the key this stub accepts on ComicVine's api_key query
// parameter. ComicVine is the one provider here that requires a secretRef
// (catalogarr/metadata/registry.go's ComicVine case); the other three need
// none, matching pkg/metadata/clients' own constructors.
const ComicVineAPIKey = "e2e-fixture-key"

// NewHandler builds the stub. recordedDir holds testdata/metadata's four
// provider subdirectories, copied verbatim by images/Dockerfile.e2e-fixtures
// (it already COPYs the whole testdata/metadata tree for tmdbstub/tvdbstub);
// local `go run` callers point --recorded-dir at ../../testdata/metadata
// instead.
func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// MusicBrainz: base "/musicbrainz". go.uploadedlobster.com/musicbrainzws2
	// requests are unauthenticated apart from a mandatory User-Agent (which
	// this stub does not enforce -- catalogarr/metadata's own client
	// construction is what supplies it, and enforcing it here would only
	// duplicate that client's own required-field check).
	mux.HandleFunc("GET /musicbrainz/artist/a74b1b7f-71a5-4011-9441-d0b5e4122711",
		serveFile(filepath.Join(recordedDir, "musicbrainz", "artist_radiohead.json"), logger))
	mux.HandleFunc("GET /musicbrainz/release-group/", musicbrainzReleaseGroups(recordedDir, logger))

	// Open Library: base "/openlibrary".
	mux.HandleFunc("GET /openlibrary/isbn/9780141439518.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "isbn_9780141439518.json"), logger))
	mux.HandleFunc("GET /openlibrary/authors/OL21594A.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "author_OL21594A.json"), logger))
	mux.HandleFunc("GET /openlibrary/authors/OL21594A/works.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "works_OL21594A.json"), logger))
	mux.HandleFunc("GET /openlibrary/works/OL138052W.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "work_OL138052W.json"), logger))
	mux.HandleFunc("GET /openlibrary/search.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "search_pride_and_prejudice.json"), logger))

	// Audnexus: base "/audnexus".
	mux.HandleFunc("GET /audnexus/books/B0036I54I6",
		serveFile(filepath.Join(recordedDir, "audnexus", "book_B0036I54I6.json"), logger))
	mux.HandleFunc("GET /audnexus/books/B0036I54I6/chapters",
		serveFile(filepath.Join(recordedDir, "audnexus", "chapters_B0036I54I6.json"), logger))

	// ComicVine: base "/comicvine". Every route requires api_key -- verified
	// against comicvine.go's doGet, which always appends it -- so a missing
	// or wrong key surfaces spec.secretRef plumbing bugs the same way
	// torznabstub's APIKey check does for Torznab.
	//
	// The volume route's pattern is deliberately the exact, literal path
	// "/comicvine/volume/4050-18257" -- net/http's ServeMux matches it only
	// when the request carries the full, prefixed guid, exactly as
	// ComicVine's real GET /volume/{guid} endpoint requires (docs/research/
	// metadata.md §2.5). A request for the bare numeric id
	// ("/comicvine/volume/18257") falls through to notFound below instead
	// of matching here -- do not widen this to a wildcard pattern that
	// would accept both shapes; that is exactly the blind spot G2-5's fake
	// provider had (comicvine.go's Volume/Issues shape defect) that let
	// Comic->Issue ship broken against the real API.
	mux.HandleFunc("GET /comicvine/volume/4050-18257", comicVineGated(
		filepath.Join(recordedDir, "comicvine", "volume_18257.json"), logger))
	mux.HandleFunc("GET /comicvine/search/", comicVineGated(
		filepath.Join(recordedDir, "comicvine", "search_batman.json"), logger))
	// The issues route cannot enforce its id shape via the mux pattern the
	// way the volume route does above -- filter=volume:{id} is a query
	// parameter, not part of the path -- so comicVineIssues checks it
	// explicitly: ComicVine's filter=volume:{id} syntax wants the bare
	// numeric id ("volume:18257"), never the prefixed guid
	// ("volume:4050-18257") the volume route above requires. A stub that
	// served this route for either shape would accept exactly the
	// mismatched pair G2-5's fake provider did, which is how "Comic.spec.
	// sourceID sent unchanged to both Volume and Issues" survived: one of
	// the two real ComicVine endpoints must reject that value, and a fixture
	// that ignores the filter's shape can never catch it.
	mux.HandleFunc("GET /comicvine/issues/", comicVineIssues(
		filepath.Join(recordedDir, "comicvine", "issues_volume_18257.json"), logger))

	mux.HandleFunc("/", notFound(logger))
	return mux
}

// musicbrainzReleaseGroups answers both the browse (?artist=<mbid>, list
// endpoint) and the single-lookup (/release-group/{mbid}) shapes -- both hit
// the SAME "/release-group/" prefix in musicbrainzws2's own client, verified
// against musicbrainz_test.go's own two request-path assertions
// ("/release-group/" with an artist query parameter for the browse, versus
// "/release-group/{id}" with no query for the lookup).
func musicbrainzReleaseGroups(recordedDir string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/musicbrainz/release-group/" && r.URL.Query().Get("artist") != "":
			serveFile(filepath.Join(recordedDir, "musicbrainz", "browse_releasegroups_radiohead.json"), logger)(w, r)
		case r.URL.Path == "/musicbrainz/release-group/0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6":
			serveFile(filepath.Join(recordedDir, "musicbrainz", "releasegroup_kid_a.json"), logger)(w, r)
		default:
			notFound(logger)(w, r)
		}
	}
}

// comicVineGated requires api_key=ComicVineAPIKey before serving path.
func comicVineGated(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != ComicVineAPIKey {
			logger.Warn("nonvideostub: comicvine request with a missing or wrong api_key", "path", r.URL.Path)
			http.Error(w, `{"error":"Invalid API Key","status_code":100}`, http.StatusUnauthorized)
			return
		}
		serveFile(path, logger)(w, r)
	}
}

// comicVineIssuesFilter is the only filter value this stub has recorded
// data for: the bare numeric volume id, matching ComicVine's real
// filter=volume:{id} syntax (see NewHandler's comment on the issues route
// for why the mux pattern alone cannot enforce this the way the volume
// route's literal path does).
const comicVineIssuesFilter = "volume:18257"

// comicVineIssues gates on api_key like comicVineGated, then additionally
// rejects any filter value other than comicVineIssuesFilter -- most
// notably the prefixed guid form ("volume:4050-18257") a client that still
// had G2-5's shape defect would send. ComicVine's own filter parser reports
// a malformed filter as an HTTP 200 with a non-1 status_code rather than an
// HTTP error status (docs/research/metadata.md §2.5's status-envelope
// note), but this stub answers 400 instead: doGet only maps HTTP status
// today (the TODO on Client.Volume), so a 200-with-bad-body would be
// decoded as if it were a valid, empty issue list and the caller would
// never see a failure -- a 400 makes the shape mismatch loud instead of
// silently returning zero issues.
func comicVineIssues(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != ComicVineAPIKey {
			logger.Warn("nonvideostub: comicvine request with a missing or wrong api_key", "path", r.URL.Path)
			http.Error(w, `{"error":"Invalid API Key","status_code":100}`, http.StatusUnauthorized)
			return
		}
		if filter := r.URL.Query().Get("filter"); filter != comicVineIssuesFilter {
			logger.Warn("nonvideostub: comicvine issues request with the wrong filter shape",
				"filter", filter, "want", comicVineIssuesFilter)
			http.Error(w, `{"error":"Filter Error","status_code":102}`, http.StatusBadRequest)
			return
		}
		serveFile(path, logger)(w, r)
	}
}

// serveFile answers with the recorded JSON at path, or 404s (loudly
// logged) when the image was built without it -- mirroring tmdbstub's own
// serveFile contract exactly. It re-reads per request on purpose: a stub
// that picks up an edited fixture without a restart is easier to debug
// than one that caches.
func serveFile(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		if err != nil {
			logger.Error("nonvideostub: recorded fixture missing", "path", path, "error", err)
			http.Error(w, "fixture not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

// notFound logs every unrecognised route.
func notFound(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("nonvideostub: unrecorded route", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
		http.Error(w, "no recorded fixture for this route", http.StatusNotFound)
	}
}
