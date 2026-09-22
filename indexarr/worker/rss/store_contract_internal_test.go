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

package rss

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// torznabTitled is the smallest wire release that exercises the projection.
func torznabTitled(title, guid string) torznab.Release {
	return torznab.Release{Title: title, GUID: guid}
}

// TestRejectReasonAgreesWithTheRealStore is the reason rejectReason is allowed
// to mirror relindex's validate at all.
//
// A guard that restates another package's rules in prose drifts the moment
// that package gains a rule, and the failure mode here is expensive: Upsert
// validates the WHOLE batch before opening its transaction, so one row the
// filter missed loses the entire page and, because indexAndPublish returns
// before both the status write and ScheduleNext, ends that indexer's RSS
// chain for good with the Indexer still reading healthy.
//
// So the mirror is not checked against a comment or a copy of the regex --
// it is checked against a REAL sqlite store, row by row, in both directions.
// If pkg/relindex adds a validation rule, this fails here rather than in
// production.
func TestRejectReasonAgreesWithTheRealStore(t *testing.T) {
	store, closer, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	good := func() relindex.Release {
		return relindex.Release{
			Indexer: "idx", GUID: "g", Title: "The Matrix 1999",
			TitleNorm: "matrix 1999", Protocol: "torrent", FetchedAt: at,
		}
	}

	tests := []struct {
		name   string
		mangle func(*relindex.Release)
	}{
		{"a valid row", func(*relindex.Release) {}},
		{"no indexer", func(r *relindex.Release) { r.Indexer = "" }},
		{"no guid", func(r *relindex.Release) { r.GUID = "" }},
		{"a title that normalises to nothing", func(r *relindex.Release) { r.TitleNorm = "" }},
		{"no fetchedAt", func(r *relindex.Release) { r.FetchedAt = time.Time{} }},
		{"a zero publishedAt pointer", func(r *relindex.Release) { r.PublishedAt = ptr.To(time.Time{}) }},
		{"a real publishedAt", func(r *relindex.Release) { r.PublishedAt = ptr.To(at.Add(-time.Hour)) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := good()
			row.GUID = row.GUID + "-" + tt.name // distinct, so a good row is never a duplicate
			tt.mangle(&row)

			reason := rejectReason(row)
			_, upsertErr := store.Upsert(t.Context(), []relindex.Release{row})

			if reason == "" {
				require.NoError(t, upsertErr,
					"rejectReason passed a row the store refuses: ONE of these in a page loses the whole page")
				return
			}
			require.ErrorIs(t, upsertErr, relindex.ErrInvalidRelease,
				"rejectReason drops a row the store would have accepted: %s", reason)
		})
	}
}

// The two shapes an indexer can actually put on the wire. Neither is exotic:
// release.CleanTitle keeps only [a-z0-9 ], and pkg/torznab does not invent a
// GUID for an item that carries none.
func TestRejectReasonCatchesWhatAFeedCanActuallySend(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, title := range []string{"★★★", "???", "日本語のタイトル", "「」【】"} {
		t.Run(title, func(t *testing.T) {
			rel := ProjectRelease(torznabTitled(title, "g"), "idx", "torrent")
			rel.FetchedAt = at
			row, err := indexRow(rel, "idx", at)
			require.NoError(t, err)
			require.Empty(t, row.TitleNorm, "premise changed: CleanTitle now keeps something")
			require.NotEmpty(t, rejectReason(row))
		})
	}

	rel := ProjectRelease(torznabTitled("The Matrix 1999 1080p BluRay x264-GRP", ""), "idx", "torrent")
	rel.FetchedAt = at
	row, err := indexRow(rel, "idx", at)
	require.NoError(t, err)
	require.Equal(t, "the indexer reported no guid", rejectReason(row))
}
