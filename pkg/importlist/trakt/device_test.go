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

package trakt_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mediactl/clustarr/pkg/importlist/trakt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

func TestDeviceFlowStartThenPollPendingThenAuthorized(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/device/code":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/device_code.json"))
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/device/token":
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/device_token_pending.json"))
				return
			}
			_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/device_token_authorized.json"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	flow := trakt.NewDeviceFlow(trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, trakt.WithBaseURL(srv.URL))

	dc, err := flow.Start(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "5C5X5MDX", dc.UserCode)
	assert.Equal(t, "https://trakt.tv/activate", dc.VerificationURL)

	status, _, err := flow.Poll(t.Context(), dc)
	require.NoError(t, err)
	assert.Equal(t, trakt.PollStatusPending, status)

	status, tok, err := flow.Poll(t.Context(), dc)
	require.NoError(t, err)
	assert.Equal(t, trakt.PollStatusAuthorized, status)
	assert.Equal(t, "dbaf9757982a9e738f05d249b7b5b4a266b3990afb2fFF88e08e2da8bd82c3fb", tok.AccessToken)
	assert.Equal(t, "76ba4c9d287960a7202585dc793977529c1cec84b04c684bc5be4cbc36e8c4a", tok.RefreshToken)
	assert.False(t, tok.ExpiresAt.IsZero())
}

func TestDeviceFlowPollTerminalStates(t *testing.T) {
	tests := map[string]struct {
		status int
		want   trakt.PollStatus
	}{
		"invalid code": {status: http.StatusNotFound, want: trakt.PollStatusInvalidCode},
		"already used": {status: http.StatusConflict, want: trakt.PollStatusAlreadyUsed},
		"expired":      {status: http.StatusGone, want: trakt.PollStatusExpired},
		"denied":       {status: 418, want: trakt.PollStatusDenied},
		"slow down":    {status: http.StatusTooManyRequests, want: trakt.PollStatusSlowDown},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			flow := trakt.NewDeviceFlow(trakt.Credentials{}, trakt.WithBaseURL(srv.URL))
			status, tok, err := flow.Poll(t.Context(), trakt.DeviceCode{DeviceCode: "x"})
			require.NoError(t, err)
			assert.Equal(t, tt.want, status)
			assert.Zero(t, tok)
		})
	}
}

func TestDeviceFlowRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/oauth/token", r.URL.Path)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "refresh_token", body["grant_type"])
		assert.Equal(t, "old-refresh", body["refresh_token"])
		_, _ = w.Write(mustReadFile(t, "../../../testdata/importlist/trakt/token_refresh.json"))
	}))
	defer srv.Close()

	flow := trakt.NewDeviceFlow(trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, trakt.WithBaseURL(srv.URL))
	tok, err := flow.Refresh(t.Context(), "old-refresh")
	require.NoError(t, err)
	assert.Equal(t, "e58479e1a76e3b1d2e9b06d7c2e40c0e9c5a4b1d3d9db32d1e97f8e5e2e07c1f", tok.AccessToken)
	assert.NotEqual(t, "old-refresh", tok.RefreshToken) // Trakt refresh tokens are single-use
}
