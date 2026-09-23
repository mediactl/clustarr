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

package gestdown_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
)

// Every JSON API response is read through a cap (CLAUDE.md: "Every HTTP
// response body is read through a cap"). Both of Gestdown's JSON endpoints
// -- the show lookup and the subtitle search -- used to decode straight off
// the body with no bound.
func TestOversizedJSONResponsesAreRefused(t *testing.T) {
	huge := []byte(`{"padding":"` + strings.Repeat("a", 5<<20) + `"}`)
	shows := readFixture(t, "shows.json")

	for _, tc := range []struct{ name, oversizedPrefix string }{
		{"show lookup", "/shows/external/tvdb/"},
		{"subtitle search", "/subtitles/get/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, tc.oversizedPrefix):
					_, _ = w.Write(huge)
				case strings.HasPrefix(r.URL.Path, "/shows/external/tvdb/"):
					_, _ = w.Write(shows)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
			_, err := p.Search(context.Background(), subtitles.Query{
				Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1,
				Languages: []subtitles.LangKey{"en"},
			})
			require.ErrorIs(t, err, gestdown.ErrResponseTooLarge)
		})
	}
}
