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

package plex

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParsePagingDefaults is spec §D.2/research §3: start 0, size 20 when
// neither header nor query param is present.
func TestParsePagingDefaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/plex/tv/library/metadata/x/children", nil)
	p := parsePaging(r)
	require.Equal(t, 0, p.start)
	require.Equal(t, 20, p.size)
}

// TestParsePagingHeaderWins proves the header form is read first, per
// research §3's own precedence ("also accepted as query parameters").
func TestParsePagingHeaderWins(t *testing.T) {
	r := httptest.NewRequest("GET", "/plex/tv/library/metadata/x/children?X-Plex-Container-Start=9", nil)
	r.Header.Set("X-Plex-Container-Start", "3")
	p := parsePaging(r)
	require.Equal(t, 3, p.start)
}

// TestParsePagingQueryFallback proves the query-parameter form works when
// no header is sent -- research §3: "also accepted as query parameters of
// the same name".
func TestParsePagingQueryFallback(t *testing.T) {
	r := httptest.NewRequest("GET", "/plex/tv/library/metadata/x/children?X-Plex-Container-Start=4&X-Plex-Container-Size=2", nil)
	p := parsePaging(r)
	require.Equal(t, 4, p.start)
	require.Equal(t, 2, p.size)
}

// TestParsePagingNegativeStartClamps proves a negative start (malformed
// input) never reaches a negative slice index.
func TestParsePagingNegativeStartClamps(t *testing.T) {
	r := httptest.NewRequest("GET", "/plex/tv/library/metadata/x/children", nil)
	r.Header.Set("X-Plex-Container-Start", "-5")
	p := parsePaging(r)
	require.Equal(t, 0, p.start)
}

// TestWindowMetadataBoundary is Review Focus 3, at the unit level: start
// equal to or past total answers an empty, non-nil slice and the true
// total, never an out-of-range panic.
func TestWindowMetadataBoundary(t *testing.T) {
	items := []Metadata{{RatingKey: "a"}, {RatingKey: "b"}, {RatingKey: "c"}}

	window, total := windowMetadata(items, pageRequest{start: 3, size: 20})
	require.Equal(t, 3, total)
	require.NotNil(t, window)
	require.Empty(t, window)

	window, total = windowMetadata(items, pageRequest{start: 10, size: 20})
	require.Equal(t, 3, total)
	require.Empty(t, window)

	window, total = windowMetadata(items, pageRequest{start: 1, size: 1})
	require.Equal(t, 3, total)
	require.Equal(t, []Metadata{{RatingKey: "b"}}, window)

	// A window that would run past the end is clamped, not padded.
	window, total = windowMetadata(items, pageRequest{start: 2, size: 20})
	require.Equal(t, 3, total)
	require.Equal(t, []Metadata{{RatingKey: "c"}}, window)
}
