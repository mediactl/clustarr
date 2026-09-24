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

// Package cardigannstub serves a real Cardigann tracker page: a form login
// (test/data/cardigann/login-form.yml's own shape, served from
// login-form.html) and a search results page, matched exactly against how
// pkg/cardigann/login.go's loginForm and pkg/cardigann/search.go's
// searchOnePath parse them -- not a shape this package invented.
//
// login-form.yml (read directly by test/e2e as the IndexerDefinition's
// spec.yaml; this package never parses it) is the "test fixture — form
// login with a scraped CSRF token" definition under test/data/cardigann/,
// chosen because it is the one bundled definition that exercises BOTH login
// and search with nothing else in the way: 1337x.yml's search.paths use Go
// template conditionals this fixture would have to reimplement pixel for
// pixel, and login-form.yml has no such thing -- one login step (GET
// /login, scrape a CSRF token, POST username/password/csrf_token, check
// div.error) and one search step (GET /browse, no query parameters at all,
// since the definition declares no search.inputs -- verified against
// pkg/cardigann/search.go's buildSearchRequest, which sends only what
// search.inputs/path.inputs name).
//
// Two more routes are session-gated like a real private tracker's: GET
// /dashboard, the definition's login.test (selector "a.logout"), which
// pkg/cardigann's Login runs after every login method since gap fix Z6 --
// a login the tracker did not accept is redirected to /login and fails the
// test -- and GET /torrent/{name} (a
// session-gated download target, reached only through indexarr's Torznab
// facade -- GET /{indexer}/download -- never by pkg/cardigann.Engine.Download
// itself, since app/indexer/download's Fetcher is a separate, generic
// session-cookie fetch that never consults the definition's download:
// block; see app/indexer/download/doc.go, "The download URL is never
// rewritten"). Both are served so scenario 10 (G4-1) can prove the session
// carries all the way from login through a facade-mediated fetch.
//
// It never talks to the Internet; the e2e cluster has no egress.
package cardigannstub

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

// Username, Password and CSRFToken are the credentials and scraped token
// this stub accepts. CSRFToken must equal login-form.html's embedded
// input[name=csrf_token] value ("tok-abc123") -- pkg/cardigann's
// SelectorInputs extraction scrapes it from the page this stub serves, so
// the two are the same fixture text, not independently chosen.
const (
	Username  = "e2e-cardigann-user"
	Password  = "e2e-cardigann-pass" //nolint:gosec // fixture credential, not a real secret
	CSRFToken = "tok-abc123"
)

// SessionCookieName and SessionCookieValue are the cookie a successful
// POST /login sets, and that GET /browse and GET /torrent/{name} require.
const (
	SessionCookieName  = "session"
	SessionCookieValue = "e2e-cardigann-session"
)

// Search result fields. ResultTitle, ResultSize and ResultSeeders are
// scraped by login-form.yml's search.fields (title: td.title, size:
// td.size, seeders: td.seeders); ResultDownloadPath is what td.title a's
// href resolves to -- a path, not a magnet URI, on purpose: pkg/cardigann's
// mapResultToRelease resolves a non-magnet download field against
// cfg.BaseURL (cardigann/search.go), and a same-origin, session-gated path
// is what lets scenario 10 prove indexarr's Torznab facade brokers an
// AUTHENTICATED fetch that an unauthenticated caller of the same URL
// cannot perform itself -- a magnet URI would skip that proof entirely
// (cardigann/download.go returns a magnet: link unread, no session
// involved).
const (
	ResultTitle        = "Clustarr.E2E.Fixture.Cardigann.S01E01.1080p.WEB-DL"
	ResultSize         = "1.4 GB"
	ResultSeeders      = "42"
	ResultDownloadPath = "/torrent/e2e-fixture.torrent"
	TorrentContentType = "application/x-bittorrent"
)

// TorrentBytes is what GET ResultDownloadPath answers with once
// authenticated -- not a valid bencoded torrent, because nothing in this
// scenario parses it as one; it is a fixed byte string the e2e test
// compares its download RESPONSE body against, the same role
// test/fixtures/torznabstub's embedded XML plays for a Torznab search.
var TorrentBytes = []byte("d8:e2e-fixture-cardigann-torrent-contentse")

// errorHTML is served on a failed login: login-form.yml's login.error block
// selects "div.error".
const errorHTML = `<!DOCTYPE html><html><body><div class="error">invalid username or password</div></body></html>`

// dashboardHTML matches login-form.yml's login.test block (path: dashboard,
// selector: a.logout), served to a request carrying the session; Login runs
// that test after every login method.
const dashboardHTML = `<!DOCTYPE html><html><body><a class="logout" href="/logout">Logout</a></body></html>`

// browseHTMLTemplate is login-form.yml's search.rows/fields shape: one
// <tr> with td.title (containing the download link), td.size and
// td.seeders. A second <tr> with none of those classes is included on
// purpose -- rows.selector is a bare "tr", and evaluateRow must skip a row
// whose non-optional fields (title) do not match rather than erroring the
// whole search, exactly as a real tracker's header row would.
const browseHTMLTemplate = `<!DOCTYPE html><html><body><table>
<tr><th>Name</th><th>Size</th><th>Seeders</th></tr>
<tr><td class="title"><a href="%s">%s</a></td><td class="size">%s</td><td class="seeders">%s</td></tr>
</table></body></html>`

// NewHandler builds the stub. recordedDir holds login-form.html, copied
// verbatim from test/data/cardigann (images/Dockerfile.e2e-fixtures COPYs it
// there at image-build time; local `go run` callers point --recorded-dir at
// ../../testdata/cardigann instead).
func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", serveLoginPage(recordedDir, logger))
	mux.HandleFunc("POST /login", handleLoginSubmit(logger))
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		if !hasSession(r) {
			logger.Info("cardigannstub: /dashboard with no valid session; redirecting to the login page")
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dashboardHTML))
	})
	mux.HandleFunc("GET /browse", handleBrowse(logger))
	mux.HandleFunc("GET "+ResultDownloadPath, handleTorrent(logger))
	mux.HandleFunc("/", notFound(logger))
	return mux
}

func serveLoginPage(recordedDir string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(recordedDir, "login-form.html"))
		if err != nil {
			logger.Error("cardigannstub: recorded login-form.html missing", "path", recordedDir, "error", err)
			http.Error(w, "fixture not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	}
}

// handleLoginSubmit is login-form.yml's login.path/login.submitPath target
// (both default to "login", so this is the same path the GET above serves):
// pkg/cardigann.loginForm POSTs username, password and the scraped
// csrf_token here.
func handleLoginSubmit(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		user, pass, csrf := r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("csrf_token")
		if user != Username || pass != Password || csrf != CSRFToken {
			logger.Warn("cardigannstub: login rejected", "username", user, "csrfOK", csrf == CSRFToken)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(errorHTML))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: SessionCookieValue, Path: "/"})
		w.WriteHeader(http.StatusOK)
	}
}

// hasSession reports whether r carries the cookie handleLoginSubmit sets.
func hasSession(r *http.Request) bool {
	c, err := r.Cookie(SessionCookieName)
	return err == nil && c.Value == SessionCookieValue
}

// handleBrowse is login-form.yml's search.path target. It is reachable
// with no query parameters at all -- verified against
// pkg/cardigann/search.go's buildSearchRequest: the definition declares no
// search.inputs, so nothing is ever sent. A request with no valid session
// answers the same page with zero result rows, mirroring a tracker whose
// browse page renders but shows nothing to a logged-out visitor -- there is
// no search.error block in login-form.yml to trip, so this is the only
// signal an unauthenticated pkg/cardigann caller would see, and it is
// correct: Engine.Search itself already refuses to run before Login when
// def.RequiresSession() (ErrSessionRequired), so this path only matters to
// a caller that bypasses that guard, such as an ad hoc curl.
func handleBrowse(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !hasSession(r) {
			logger.Info("cardigannstub: /browse with no valid session; answering an empty result set")
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><table></table></body></html>`))
			return
		}
		_, _ = fmt.Fprintf(w, browseHTMLTemplate, ResultDownloadPath, ResultTitle, ResultSize, ResultSeeders)
	}
}

// handleTorrent is ResultDownloadPath: session-gated, exactly like a real
// private tracker's download link. It is reached two ways in scenario 10 --
// directly (expected 401, proving the gate is real) and through indexarr's
// Torznab facade (GET /{indexer}/download), whose Fetcher carries the
// Indexer's stored session cookie (app/indexer/download/fetch.go's
// NewFetcherFor) and so is expected to succeed.
func handleTorrent(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hasSession(r) {
			logger.Info("cardigannstub: torrent fetch with no valid session", "path", r.URL.Path)
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", TorrentContentType)
		_, _ = w.Write(TorrentBytes)
	}
}

// notFound logs every unrecognised route, for the same reason every other
// stub in this tree does: "nobody serves that endpoint" must show up as a
// line in the stub's log, not as a twenty-minute timeout in a scenario.
func notFound(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("cardigannstub: unrecognised route", "method", r.Method, "path", r.URL.Path)
		http.Error(w, "no fixture for this route", http.StatusNotFound)
	}
}
