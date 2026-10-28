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

package tuner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv"
)

// TestAFetchErrorNeverNamesTheCredentials: an M3U playlist URL is the
// person's own, and iptv.Redact keeps its last path segment, which may be
// the password or hold the username. A refused fetch and a failed
// connection both name the URL with its credentials replaced by their
// placeholders, in the message a status and a log line carry.
func TestAFetchErrorNeverNamesTheCredentials(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for _, c := range []struct{ base, tmpl, want string }{
		{srv.URL, "/get/{username}/{password}", "/…/{password}"},
		{srv.URL, "/lists/{username}.m3u", "/…/{username}.m3u"},
		{closed.URL, "/get/{username}/{password}", "/…/{password}"},
	} {
		cfg := Config{Type: "M3U", PlaylistURLTemplate: c.base + c.tmpl, Username: "usr-9q", Password: "pw-4k"}
		tu := New(Options{})
		_, err := tu.get(context.Background(), cfg, cfg.PlaylistURL())
		require.Error(t, err, c.tmpl)
		for _, msg := range []string{err.Error(), iptv.RedactError(fmt.Errorf("fetch playlist: %w", err))} {
			assert.NotContains(t, msg, "usr-9q", c.tmpl)
			assert.NotContains(t, msg, "pw-4k", c.tmpl)
			assert.Contains(t, msg, c.want, c.tmpl)
		}
	}
}

// TestAFetchErrorStillReachesItsCause: the scrubbed error unwraps to what
// failed, so a caller can still ask what it was.
func TestAFetchErrorStillReachesItsCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := Config{Type: "M3U", PlaylistURLTemplate: "http://127.0.0.1:1/get/{username}/{password}", Username: "u", Password: "p"}
	_, err := New(Options{}).get(ctx, cfg, cfg.PlaylistURL())
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
}
