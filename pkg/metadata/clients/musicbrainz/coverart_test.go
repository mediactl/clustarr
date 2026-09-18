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

package musicbrainz_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
)

func TestCoverArtURL(t *testing.T) {
	const releaseMBID = "d2b5c1b8-1a6c-4b3d-9c1a-1e5f9c6d2a11"

	tests := []struct {
		name string
		size int
		want string
	}{
		{"250px", 250, "https://coverartarchive.org/release/d2b5c1b8-1a6c-4b3d-9c1a-1e5f9c6d2a11/front-250"},
		{"500px", 500, "https://coverartarchive.org/release/d2b5c1b8-1a6c-4b3d-9c1a-1e5f9c6d2a11/front-500"},
		{"1200px", 1200, "https://coverartarchive.org/release/d2b5c1b8-1a6c-4b3d-9c1a-1e5f9c6d2a11/front-1200"},
		{"full size (0)", 0, "https://coverartarchive.org/release/d2b5c1b8-1a6c-4b3d-9c1a-1e5f9c6d2a11/front"},
		{"unrecognised size falls back to full size", 999, "https://coverartarchive.org/release/d2b5c1b8-1a6c-4b3d-9c1a-1e5f9c6d2a11/front"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, musicbrainz.CoverArtURL(releaseMBID, tt.size))
		})
	}
}
