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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

// Every JSON API response is read through a cap (CLAUDE.md: "Every HTTP
// response body is read through a cap"). Each case serves a well-formed but
// oversized JSON body -- a padded string value past 4 MiB -- on one
// endpoint, and the call must fail with ErrResponseTooLarge rather than
// buffer it whole and decode it.
func TestOversizedJSONResponsesAreRefused(t *testing.T) {
	login, err := os.ReadFile("../../../../test/data/subtitles/opensubtitles/login.json")
	require.NoError(t, err)
	huge := []byte(`{"padding":"` + strings.Repeat("a", 5<<20) + `"}`)

	for _, tc := range []struct {
		name, oversized string
		call            func(*opensubtitlescom.Provider) error
	}{
		{"login", "/login", func(p *opensubtitlescom.Provider) error {
			return p.EnsureLoggedIn(context.Background())
		}},
		{"search", "/subtitles", func(p *opensubtitlescom.Provider) error {
			_, err := p.Search(context.Background(), subtitles.Query{Kind: "movie", IDs: map[string]string{"imdb": "1"}})
			return err
		}},
		{"download link", "/download", func(p *opensubtitlescom.Provider) error {
			_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "1"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case tc.oversized:
					_, _ = w.Write(huge)
				case "/login":
					_, _ = w.Write(login)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			p := opensubtitlescom.New(opensubtitlescom.Config{
				APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL, UserAgent: "clustarr-test",
			})
			err := tc.call(p)
			require.Error(t, err)
			require.True(t, errors.Is(err, opensubtitlescom.ErrResponseTooLarge), "got %v", err)
		})
	}
}
