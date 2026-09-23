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

package cardigannstub

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loginFormDefinition loads the exact bundled definition test/e2e's
// scenario 10 will use, from repo-root testdata/cardigann -- the single
// source of truth this stub's own doc comment names, never a copy.
func loginFormDefinition(t *testing.T) *cardigann.Definition {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/cardigann/login-form.yml")
	require.NoError(t, err)
	def, err := cardigann.Load(raw)
	require.NoError(t, err)
	return def
}

// TestRealEngineLogsInAndSearches is this fixture's own guard rail, the
// same role torznabstub's TestEmbeddedDocumentsParse plays for XML: it
// drives the stub with the REAL pkg/cardigann.Engine -- the same code
// indexarr's Indexer controller runs -- so a shape mismatch between this
// server and login-form.yml fails `make test` today, not twenty minutes
// into hack/e2e.sh as an Indexer that never authenticates.
func TestRealEngineLogsInAndSearches(t *testing.T) {
	srv := httptest.NewServer(NewHandler("../../../testdata/cardigann", discardLogger()))
	defer srv.Close()

	def := loginFormDefinition(t)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{
		"username": Username, "password": Password,
	})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}

	// Before login, Search refuses to run at all -- the definition requires
	// a session and none has been established yet.
	_, err = eng.Search(context.Background(), def, cfg, cardigann.Query{})
	require.ErrorIs(t, err, cardigann.ErrSessionRequired)

	sess, err := eng.Login(context.Background(), def, cfg)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.NotEmpty(t, sess.Cookies)

	cfg.Session = sess
	releases, err := eng.Search(context.Background(), def, cfg, cardigann.Query{})
	require.NoError(t, err)
	require.Len(t, releases, 1)
	rel := releases[0]
	require.Equal(t, ResultTitle, rel.Title)
	require.Equal(t, srv.URL+ResultDownloadPath, rel.Link)
	require.EqualValues(t, 42, *rel.Seeders)
}

// TestWrongCredentialsFail proves the stub actually gates on the posted
// form, not merely on reaching /login -- the negative half of the same
// login.error check TestRealEngineLogsInAndSearches's happy path exercises.
func TestWrongCredentialsFail(t *testing.T) {
	srv := httptest.NewServer(NewHandler("../../../testdata/cardigann", discardLogger()))
	defer srv.Close()

	def := loginFormDefinition(t)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{
		"username": Username, "password": "wrong",
	})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}
	_, err = eng.Login(context.Background(), def, cfg)
	require.Error(t, err)
	var loginErr *cardigann.LoginError
	require.ErrorAs(t, err, &loginErr)
}

// TestTorrentIsSessionGated proves ResultDownloadPath is not a free URL:
// fetched without the session cookie it 401s, and indexarr's Torznab
// facade (GET /{indexer}/download) is what is expected to supply that
// cookie on a real caller's behalf -- see indexarr/download/fetch.go's
// NewFetcherFor, which seeds a cookie jar from the Indexer's stored
// session Secret before making exactly this request.
func TestTorrentIsSessionGated(t *testing.T) {
	srv := httptest.NewServer(NewHandler("../../../testdata/cardigann", discardLogger()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + ResultDownloadPath) //nolint:noctx,gosec // test-local fixture URL, not user input
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	req, err := http.NewRequest(http.MethodGet, srv.URL+ResultDownloadPath, nil) //nolint:noctx
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: SessionCookieValue})
	resp2, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	require.Equal(t, TorrentBytes, body)
}

// TestRecordedFixtureMissing404s proves NewHandler fails loudly, not
// silently, when the image was built without login-form.html -- the same
// guard tmdbstub's serveFile gives every recorded-JSON route.
func TestRecordedFixtureMissing404s(t *testing.T) {
	srv := httptest.NewServer(NewHandler(t.TempDir(), discardLogger()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/login") //nolint:noctx,gosec // test-local fixture URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
