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

package opensubtitlescom_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

func TestLoginCachesTheTokenAndDoesNotReLoginOnASecondCall(t *testing.T) {
	loginCalls := 0
	fixture, err := os.ReadFile("../../../../testdata/subtitles/opensubtitles/login.json")
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			loginCalls++
			assert.Equal(t, "test-api-key", r.Header.Get("Api-Key"))
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixture)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "test-api-key", Username: "u", Password: "p", Endpoint: srv.URL, UserAgent: "clustarr-test",
	})

	require.NoError(t, p.EnsureLoggedIn(context.Background()))
	require.NoError(t, p.EnsureLoggedIn(context.Background()))
	assert.Equal(t, 1, loginCalls, "a cached token must not trigger a second /login call")
}
