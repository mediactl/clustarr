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

// Package importliststub serves three of pkg/importlist's providers from
// one process: Trakt's OAuth device-code flow plus watchlist (pkg/importlist/
// trakt), the Plex Discover watchlist (pkg/importlist/plex) and an
// mdblist.com list export (pkg/importlist/mdblist). Every route and JSON
// shape below is read straight off those packages' own request-building code
// and their existing recorded fixtures under testdata/importlist/ -- see
// each provider's doc comment for the exact source read.
//
// # Trakt and Plex cannot be reached from a deployed cluster today
//
// importarr/worker/importlist/provider.go's BuildProvider constructs both
// trakt.New and plex.New with no base-URL override -- trakt.New passes a nil
// *DeviceFlow and only trakt.WithHTTPClient, so trakt.DefaultBaseURL
// ("https://api.trakt.tv") is what it always dials; plex.New has no override
// parameter at all, so plex.defaultBaseURL ("https://discover.provider.plex.tv")
// is likewise fixed. importarr/controller/importlist/controller.go DOES carry
// a Reconciler.TraktBaseURL field for the device-code flow specifically, but
// importarr/run.go's own comment calls it "a test seam": no flag or
// environment variable threads it, so even the controller's Start/Poll calls
// cannot be redirected from a deployed binary, only from a Go test that
// constructs the Reconciler directly.
//
// This fixture is still built and deployed, per this task's brief, and its
// own unit tests drive it with the real pkg/importlist/trakt and
// pkg/importlist/plex clients (proving the wire shapes are exactly right) --
// but test/e2e's scenario 9 can exercise Trakt and Plex only up to creating
// the ImportList CR; it cannot wait on a sync that structurally cannot reach
// this fixture without an importarr code change, which is out of this task's
// file scope (importarr/ belongs to G1-3/G2-5). See scenario 9's own comment
// in test/e2e for the named skip this produces.
//
// mdblist has no such gap: MdbList.URL (api/catalog/v1alpha1/importlist_types.go)
// is a required, fully operator-supplied URL -- pkg/importlist/mdblist.List
// GETs it verbatim -- so config/e2e can point it at this fixture directly and
// scenario 9 exercises mdblist for real, end to end.
//
// It never talks to the Internet; the e2e cluster has no egress.
package importliststub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// pendingPollsBeforeAuthorized is how many POST /oauth/device/token calls
// for the SAME device_code answer "authorization_pending" before this stub
// answers authorized. Trakt's own interval (device_code.json: 5s) times
// this is comfortably inside a controller's requeue cadence, and a caller
// that polls at all proves the retry path -- an immediate "authorized"
// would prove nothing about DeviceAuthStatePending ever being observed.
const pendingPollsBeforeAuthorized = 2

// Server holds the recorded fixture directory every route reads from, plus
// the small amount of state the device-code flow needs across requests
// (poll counts) and the two recorded values (device_code, the authorized
// access token) parsed once at construction so the poll/watchlist handlers
// below never restate them.
type Server struct {
	recordedDir string
	logger      *slog.Logger

	deviceCodeJSON  []byte
	deviceCode      string
	authorizedJSON  []byte
	authorizedToken string

	mu    sync.Mutex
	polls map[string]int // device_code -> POST /oauth/device/token count
}

// NewHandler builds the stub. recordedDir holds testdata/importlist's three
// subdirectories (trakt, plex, mdblist), copied verbatim by
// images/Dockerfile.e2e-fixtures at image-build time; local `go run`
// callers point --recorded-dir at ../../testdata/importlist instead.
func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
	s := &Server{recordedDir: recordedDir, logger: logger, polls: map[string]int{}}
	s.loadDeviceCode()
	s.loadAuthorizedToken()

	mux := http.NewServeMux()

	// Trakt.
	mux.HandleFunc("POST /oauth/device/code", s.handleDeviceCode)
	mux.HandleFunc("POST /oauth/device/token", s.handleDeviceToken)
	mux.HandleFunc("POST /oauth/token", s.handleTokenRefresh)
	mux.HandleFunc("GET /users/{username}/watchlist/movies", s.handleTraktWatchlist("watchlist_movies.json"))
	mux.HandleFunc("GET /users/{username}/watchlist/shows", s.handleTraktWatchlist("watchlist_shows.json"))

	// Plex.
	mux.HandleFunc("GET /library/sections/watchlist/all", s.handlePlexWatchlist)

	// mdblist.
	mux.HandleFunc("GET /mdblist/list.json", s.handleMdblist)

	mux.HandleFunc("/", s.notFound)
	return mux
}

// serveRecorded reads relPath under s.recordedDir/provider and writes it as
// JSON, or 404s (loudly logged) when the image was built without it --
// mirroring tmdbstub.serveFile's own contract.
func (s *Server) serveRecorded(w http.ResponseWriter, provider, relPath string) {
	b, err := os.ReadFile(filepath.Join(s.recordedDir, provider, relPath))
	if err != nil {
		s.logger.Error("importliststub: recorded fixture missing", "provider", provider, "path", relPath, "error", err)
		http.Error(w, "fixture not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// loadDeviceCode reads testdata/importlist/trakt/device_code.json once at
// construction, so the poll handler below knows which device_code the
// GET .../oauth/device/code response actually promised, without restating
// it by hand and risking drift from the recorded fixture.
func (s *Server) loadDeviceCode() {
	b, err := os.ReadFile(filepath.Join(s.recordedDir, "trakt", "device_code.json"))
	if err != nil {
		s.logger.Error("importliststub: recorded device_code.json missing", "error", err)
		return
	}
	s.deviceCodeJSON = b
	var out struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		s.logger.Error("importliststub: parse device_code.json", "error", err)
		return
	}
	s.deviceCode = out.DeviceCode
}

func (s *Server) loadAuthorizedToken() {
	b, err := os.ReadFile(filepath.Join(s.recordedDir, "trakt", "device_token_authorized.json"))
	if err != nil {
		s.logger.Error("importliststub: recorded device_token_authorized.json missing", "error", err)
		return
	}
	s.authorizedJSON = b
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		s.logger.Error("importliststub: parse device_token_authorized.json", "error", err)
		return
	}
	s.authorizedToken = out.AccessToken
}

// handleDeviceCode is trakt.DeviceFlow.Start's target: POST
// /oauth/device/code with {"client_id": ...}.
func (s *Server) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	if s.deviceCodeJSON == nil {
		http.Error(w, "fixture not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.deviceCodeJSON)
}

// handleDeviceToken is trakt.DeviceFlow.Poll's target: POST
// /oauth/device/token with {"code": ..., "client_id": ..., "client_secret": ...}.
// The first pendingPollsBeforeAuthorized calls for a given code answer
// HTTP 400 (authorization_pending, per Trakt's own contract -- trakt.go's
// PollStatusPending case); every later call for that same code answers 200
// with the authorized token, so a caller's retry loop is genuinely
// exercised rather than skipped by an immediate success.
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.mu.Lock()
	s.polls[body.Code]++
	n := s.polls[body.Code]
	s.mu.Unlock()

	if s.deviceCode == "" || body.Code != s.deviceCode {
		s.logger.Warn("importliststub: device token poll for an unknown code", "code", body.Code)
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusNotFound)
		return
	}
	if n <= pendingPollsBeforeAuthorized {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending","error_description":"user has not yet approved this request"}`))
		return
	}
	if s.authorizedJSON == nil {
		http.Error(w, "fixture not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.authorizedJSON)
}

// handleTokenRefresh is trakt.DeviceFlow.Refresh's target: POST /oauth/token,
// grant_type=refresh_token. Always succeeds with the recorded refreshed
// token -- this fixture never forces a 401 on the watchlist route, so
// nothing in this stub's own flow exercises Refresh, but it is served for
// completeness and for any caller (a unit test, a future scenario) that
// wants to drive it directly.
func (s *Server) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	s.serveRecorded(w, "trakt", "token_refresh.json")
}

// handleTraktWatchlist requires the Authorization header carry the
// authorized access token before answering recordedFile -- an unauthorized
// or stale-token request 401s, the same shape trakt.List.Fetch's own retry
// logic (list.go: "refreshes ... and retries exactly once when the first
// request comes back 401") is built to react to.
func (s *Server) handleTraktWatchlist(recordedFile string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.authorizedToken == "" || got != s.authorizedToken {
			s.logger.Info("importliststub: trakt watchlist request with no valid bearer token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.serveRecorded(w, "trakt", recordedFile)
	}
}

// handlePlexWatchlist is plex.Watchlist.fetchPage's target:
// GET /library/sections/watchlist/all?type=1|2&X-Plex-Token=...
// &X-Plex-Container-Start=...&X-Plex-Container-Size=.... Only "type" and
// the start offset matter here: this stub answers its whole (small) fixture
// list on the first page (start=0) and an empty MediaContainer on any later
// page, which is enough for plex.Watchlist.Fetch's own "stop once a page
// comes back shorter than requested" loop to terminate after one request.
func (s *Server) handlePlexWatchlist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("X-Plex-Container-Start") != "" && q.Get("X-Plex-Container-Start") != "0" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":0,"Metadata":[]}}`))
		return
	}
	file := "watchlist_page1.json" // type=1, movie
	if q.Get("type") == "2" {
		file = "watchlist_series_page1.json"
	}
	s.serveRecorded(w, "plex", file)
}

// handleMdblist is the one endpoint MdbList.URL (a required, fully
// operator-supplied spec field) can point straight at -- pkg/importlist/
// mdblist.List.Fetch GETs cfg.URL verbatim with ?apikey=...&limit=1000
// appended, and this stub ignores both, answering the same recorded list of
// mixed movie/show rows regardless.
func (s *Server) handleMdblist(w http.ResponseWriter, r *http.Request) {
	s.serveRecorded(w, "mdblist", "items.json")
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.logger.Warn("importliststub: unrecognised route", "method", r.Method, "path", r.URL.Path)
	http.Error(w, "no fixture for this route", http.StatusNotFound)
}
