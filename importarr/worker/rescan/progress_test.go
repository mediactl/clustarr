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
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
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
			name: "resumable checkpoint with the breakdown",
			in: rescan.Progress{
				FilesSeen: 12, FilesMatched: 7, FilesSkipped: 4, Unchanged: 2, Transcoded: 1, Deferred: 1,
				HandedOver: 1, NotMedia: 5, Parts: 1, Extras: 2, Samples: 3, Unreadable: 1,
				Resume: "/data/media/movies/Heat (1995)/Heat (1995).mkv",
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
	// "-" escapes to "--". A Kubernetes UID is already a legal NATS KV key,
	// so the doubling buys nothing on the normal path -- but routing every
	// key through the one escaper is the point. LeaseKey and PendingKey were
	// safe only by accident of what their caller happened to pass, and that
	// is the shape that produced two separate illegal-key defects this
	// phase. Keys are opaque; consistency is worth more than readability.
	assert.Equal(t, "scan.abc--123", rescan.ProgressKey("abc-123"))
	assert.True(t, events.ValidKVKey(rescan.ProgressKey("abc-123")))

	// The reason this matters at all: the worker falls back to an empty UID,
	// and "scan." is a trailing dot, which nats.go rejects on Put and on
	// Delete alike -- so the checkpoint would fail and, for anything with a
	// finalizer, could not be cleaned up either.
	assert.True(t, events.ValidKVKey(rescan.ProgressKey("")),
		"an empty UID must still produce a legal key, not a trailing dot")
	assert.NotEqual(t, rescan.ProgressKey(""), rescan.ProgressKey("\x00"))
}

// walkOrderLess must agree with the order filepath.WalkDir really visits a
// tree in, or a resumed walk would pass over files it never counted (or
// count some twice). It is checked against a real walk, including the case
// a plain string comparison gets wrong: "a/b" is visited before "a-c".
func TestWalkOrderLessMatchesWalkDir(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a/b", "a-c", "a/b/z.mkv", "a/c.mkv", "a-c/x.mkv", "B.mkv", "a.mkv", "a/b.mkv", "ab/y.mkv"} {
		p := filepath.Join(root, rel)
		if filepath.Ext(rel) == "" {
			require.NoError(t, os.MkdirAll(p, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
	var visited []string
	require.NoError(t, filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		visited = append(visited, p)
		return err
	}))
	for i := range visited {
		for j := range visited {
			assert.Equalf(t, i < j, rescan.WalkOrderLess(visited[i], visited[j]),
				"%s before %s", visited[i], visited[j])
		}
	}
	assert.False(t, "a/b" < "a-c", "setup: the plain string order disagrees here")
}

// Summary is the Ready condition's message: the counters the status has,
// then the breakdown it has no field for, zero clauses left out.
func TestProgressSummary(t *testing.T) {
	assert.Equal(t, "0 files seen, 0 matched, 0 unmatched", rescan.Progress{}.Summary())
	assert.Equal(t,
		"12 files seen, 7 matched, 4 skipped (2 unchanged, 1 transcoded, left to catalogarr, 1 changed during the scan, left to the next), "+
			"1 unmatched; 1 transcoded files changed on disk, handed to catalogarr; 2 could not be read; "+
			"9 other files not considered (5 not media, 3 samples, 1 partial downloads)",
		rescan.Progress{
			FilesSeen: 12, FilesMatched: 7, FilesSkipped: 4, Unchanged: 2, Transcoded: 1, Deferred: 1,
			HandedOver: 1, Unreadable: 2, NotMedia: 5, Samples: 3, Parts: 1,
			Unmatched: []rescan.UnmatchedFile{{Path: "x.mkv"}},
		}.Summary())
}
