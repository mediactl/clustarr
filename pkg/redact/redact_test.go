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

package redact_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/redact"
)

func TestURL(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"apikey", "https://idx.example/api?t=search&apikey=s3cret", "https://idx.example/api"},
		{"plex token", "https://discover.provider.plex.tv/library/sections/watchlist/all?X-Plex-Token=s3cret&type=1", "https://discover.provider.plex.tv/library/sections/watchlist/all"},
		{"userinfo", "https://user:s3cret@idx.example:9117/api?passkey=s3cret#frag", "https://idx.example:9117/api"},
		{"bare query", "https://idx.example/api?", "https://idx.example/api"},
		{"unparseable keeps no query", "https://idx.example/a%zz?apikey=s3cret", "https://idx.example/a%zz"},
		{"unparseable keeps no userinfo", "https://user:s3cret@idx example/a?apikey=s3cret", "https://idx example/a"},
		{"unparseable without scheme", "%zz@s3cret", "<unparseable URL>"},
		{"opaque", "user:s3cret@host", "user:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redact.URL(tc.in)
			require.Equal(t, tc.want, got)
			require.NotContains(t, got, "s3cret")
		})
	}
}

func TestHost(t *testing.T) {
	require.Equal(t, "https://tracker.example:8443", redact.Host("https://u:s3cret@tracker.example:8443/download/s3cret/1.torrent?passkey=s3cret"))
	require.Equal(t, "<unparseable URL>", redact.Host("/download/s3cret/1.torrent"))
	require.Equal(t, "<unparseable URL>", redact.Host("https://tracker.example/%zz/s3cret"))
	require.Empty(t, redact.Host(""))
}

// A transport failure from a real http.Client: the redacted error keeps its
// type, its cause and its timeout-ness, and loses the secret.
func TestErrKeepsTheCauseAndDropsTheSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/dl/pathsecret?passkey=s3cret", nil)
	require.NoError(t, err)
	_, raw := http.DefaultClient.Do(req)
	require.Error(t, raw)
	require.Contains(t, raw.Error(), "s3cret", "the premise: net/http quotes the whole URL")

	for name, red := range map[string]func(error) error{"Err": redact.Err, "ErrHost": redact.ErrHost} {
		t.Run(name, func(t *testing.T) {
			got := red(raw)
			require.NotContains(t, got.Error(), "s3cret", "no query survives either")
			require.ErrorIs(t, got, context.DeadlineExceeded)
			var ue *url.Error
			require.ErrorAs(t, got, &ue)
			require.Equal(t, "Get", ue.Op)
			var ne net.Error
			require.ErrorAs(t, got, &ne)
			require.True(t, ne.Timeout())
		})
	}
	require.Contains(t, redact.Err(raw).Error(), "/dl/pathsecret", "Err keeps the path")
	require.NotContains(t, redact.ErrHost(raw).Error(), "pathsecret", "ErrHost keeps only the host")
	require.Contains(t, redact.ErrHost(raw).Error(), "127.0.0.1")
}

func TestErrRewritesAParseError(t *testing.T) {
	_, raw := url.Parse("https://idx.example/%zz?apikey=s3cret") //nolint:staticcheck // SA1007: the invalid URL is the point
	require.Error(t, raw)
	require.Contains(t, raw.Error(), "s3cret")
	require.NotContains(t, redact.Err(raw).Error(), "s3cret")
}

func TestErrLeavesOtherErrorsAlone(t *testing.T) {
	require.NoError(t, redact.Err(nil))
	plain := errors.New("boom")
	require.Same(t, plain, redact.Err(plain))
	wrapped := fmt.Errorf("ctx: %w", context.Canceled)
	require.Same(t, wrapped, redact.Err(wrapped))
}
