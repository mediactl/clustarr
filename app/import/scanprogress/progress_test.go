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

package scanprogress_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/events"
)

func TestProgressEncodeDecodeRoundTrip(t *testing.T) {
	seenAt := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   scanprogress.Progress
	}{
		{
			name: "zero value",
			in:   scanprogress.Progress{},
		},
		{
			name: "mid-walk checkpoint",
			in:   scanprogress.Progress{FilesSeen: 12, FilesMatched: 10, ItemsCreated: 2, ItemsUpdated: 8, FilesSkipped: 2},
		},
		{
			name: "final tally with unmatched files",
			in: scanprogress.Progress{
				Done: true, FilesSeen: 12, FilesMatched: 10, ItemsCreated: 2, ItemsUpdated: 8, FilesSkipped: 2,
				Unmatched: []scanprogress.UnmatchedFile{{
					Path:       "a.mkv",
					Reason:     "ambiguous",
					Candidates: []string{"movie-a", "movie-b"},
					SeenAt:     seenAt,
				}},
			},
		},
		{
			name: "resumable checkpoint with the breakdown",
			in: scanprogress.Progress{
				FilesSeen: 12, FilesMatched: 7, FilesSkipped: 4, Unchanged: 2, Transcoded: 1, Deferred: 1,
				HandedOver: 1, NotMedia: 5, Parts: 1, Extras: 2, Samples: 3, Unreadable: 1,
				Resume: "/data/media/movies/Heat (1995)/Heat (1995).mkv",
			},
		},
		{
			name: "failed walk",
			in:   scanprogress.Progress{Done: true, Error: "walk /data/media/movies: permission denied"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.in.Encode()
			require.NoError(t, err)

			out, err := scanprogress.DecodeProgress(data)
			require.NoError(t, err)
			assert.Equal(t, tc.in, out)
		})
	}
}

func TestDecodeProgressRejectsGarbage(t *testing.T) {
	_, err := scanprogress.DecodeProgress([]byte("{"))
	assert.Error(t, err)
}

func TestProgressKeyIsNamespacedUnderScan(t *testing.T) {
	// "-" escapes to "--". A Kubernetes UID is already a legal NATS KV key,
	// so the doubling buys nothing on the normal path -- but routing every
	// key through the one escaper is the point. LeaseKey and PendingKey were
	// safe only by accident of what their caller happened to pass, and that
	// is the shape that produced two separate illegal-key defects this
	// phase. Keys are opaque; consistency is worth more than readability.
	assert.Equal(t, "scan.abc--123", scanprogress.ProgressKey("abc-123"))
	assert.True(t, events.ValidKVKey(scanprogress.ProgressKey("abc-123")))

	// The reason this matters at all: the worker falls back to an empty UID,
	// and "scan." is a trailing dot, which nats.go rejects on Put and on
	// Delete alike -- so the checkpoint would fail and, for anything with a
	// finalizer, could not be cleaned up either.
	assert.True(t, events.ValidKVKey(scanprogress.ProgressKey("")),
		"an empty UID must still produce a legal key, not a trailing dot")
	assert.NotEqual(t, scanprogress.ProgressKey(""), scanprogress.ProgressKey("\x00"))
}

// Summary is the Ready condition's message: the counters the status has,
// then the breakdown it has no field for, zero clauses left out.
func TestProgressSummary(t *testing.T) {
	assert.Equal(t, "0 files seen, 0 matched, 0 unmatched", scanprogress.Progress{}.Summary())
	assert.Equal(t,
		"12 files seen, 7 matched, 5 skipped (2 unchanged, 1 transcoded, left to catalogarr, "+
			"1 transcode outputs, left to catalogarr, 1 changed during the scan, left to the next), "+
			"1 unmatched; 1 transcoded files changed on disk, handed to catalogarr; 2 could not be read; "+
			"9 other files not considered (5 not media, 3 samples, 1 partial downloads)",
		scanprogress.Progress{
			FilesSeen: 12, FilesMatched: 7, FilesSkipped: 5, Unchanged: 2, Transcoded: 1, TranscodeOutputs: 1, Deferred: 1,
			HandedOver: 1, Unreadable: 2, NotMedia: 5, Samples: 3, Parts: 1,
			Unmatched: []scanprogress.UnmatchedFile{{Path: "x.mkv"}},
		}.Summary())
}

// MergeRenamed replaces an entry for a file already listed where it stands,
// appends any other, and keeps the newest 200 -- the CRD's MaxItems.
func TestMergeRenamed(t *testing.T) {
	list := scanprogress.MergeRenamed(nil,
		scanprogress.RenamedFile{From: "a", To: "A", Reason: mediafilespec.RenameDryRun},
		scanprogress.RenamedFile{From: "b", To: "B", Reason: mediafilespec.RenameCollision})
	list = scanprogress.MergeRenamed(list, scanprogress.RenamedFile{From: "a", To: "A"}, scanprogress.RenamedFile{From: "c", To: "C"})
	assert.Equal(t, []scanprogress.RenamedFile{
		{From: "a", To: "A"}, {From: "b", To: "B", Reason: mediafilespec.RenameCollision}, {From: "c", To: "C"},
	}, list)

	for i := range 250 {
		list = scanprogress.MergeRenamed(list, scanprogress.RenamedFile{From: fmt.Sprintf("f%03d", i)})
	}
	require.Len(t, list, 200)
	assert.Equal(t, "f050", list[0].From, "the oldest entries are dropped")
	assert.Equal(t, "f249", list[199].From)
}
