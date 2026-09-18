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

package rescan_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/importarr/worker/rescan"
)

func TestProgressEncodeDecodeRoundTrip(t *testing.T) {
	seenAt := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   rescan.Progress
	}{
		{
			name: "zero value",
			in:   rescan.Progress{},
		},
		{
			name: "mid-walk checkpoint",
			in:   rescan.Progress{FilesSeen: 12, FilesMatched: 10, ItemsCreated: 2, ItemsUpdated: 8, FilesSkipped: 2},
		},
		{
			name: "final tally with unmatched files",
			in: rescan.Progress{
				Done: true, FilesSeen: 12, FilesMatched: 10, ItemsCreated: 2, ItemsUpdated: 8, FilesSkipped: 2,
				Unmatched: []rescan.UnmatchedFile{{
					Path:       "a.mkv",
					Reason:     "ambiguous",
					Candidates: []string{"movie-a", "movie-b"},
					SeenAt:     seenAt,
				}},
			},
		},
		{
			name: "failed walk",
			in:   rescan.Progress{Done: true, Error: "walk /data/media/movies: permission denied"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.in.Encode()
			require.NoError(t, err)

			out, err := rescan.DecodeProgress(data)
			require.NoError(t, err)
			assert.Equal(t, tc.in, out)
		})
	}
}

func TestDecodeProgressRejectsGarbage(t *testing.T) {
	_, err := rescan.DecodeProgress([]byte("{"))
	assert.Error(t, err)
}

func TestProgressKeyIsNamespacedUnderScan(t *testing.T) {
	assert.Equal(t, "scan.abc-123", rescan.ProgressKey("abc-123"))
}
