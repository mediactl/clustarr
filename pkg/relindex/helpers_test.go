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

package relindex_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
)

// Fixed instants. These are deliberately not time.Now(): a time.Time from
// time.Now() carries a monotonic reading, require.Equal compares it, and a
// value that has round-tripped through an int64 of Unix nanoseconds has lost
// it. Nanosecond components are non-zero so a truncating storage bug shows up.
var (
	fetchedAt   = time.Date(2026, 9, 19, 12, 0, 0, 123456789, time.UTC)
	publishedAt = time.Date(2026, 9, 18, 3, 30, 0, 987654321, time.UTC)
)

// newStore opens a store in a fresh temp directory and closes it on cleanup.
func newStore(t *testing.T) relindex.Store {
	t.Helper()
	s, closer, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })
	return s
}

// newStoreAt opens a store at an explicit path and closes it on cleanup.
func newStoreAt(t *testing.T, path string) relindex.Store {
	t.Helper()
	s, closer, err := relindex.Open(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })
	return s
}

// rel builds a minimally valid Release.
func rel(indexer, guid, title string) relindex.Release {
	return relindex.Release{
		Indexer:     indexer,
		GUID:        guid,
		Title:       title,
		TitleNorm:   title,
		Group:       "NTb",
		Protocol:    "torrent",
		Categories:  []int{2000, 2040},
		SizeBytes:   1 << 30,
		PublishedAt: &publishedAt,
		FetchedAt:   fetchedAt,
		InfoJSON:    []byte(`{"info":{"guid":"` + guid + `"}}`),
	}
}

// mustUpsert upserts and asserts the inserted count.
func mustUpsert(t *testing.T, ctx context.Context, s relindex.Store, want int, rels ...relindex.Release) {
	t.Helper()
	got, err := s.Upsert(ctx, rels)
	require.NoError(t, err)
	require.Equal(t, want, got)
}
