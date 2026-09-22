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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
)

func TestUpsertReportsOnlyGenuineInserts(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	n, err := s.Upsert(ctx, []relindex.Release{
		rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb"),
		rel("nzbgeek", "g2", "The Matrix Reloaded 2003 1080p BluRay x264-NTb"),
	})
	require.NoError(t, err)
	require.Equal(t, 2, n)

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, st.Releases)
}

func TestUpsertOnAnEmptyBatchIsANoOp(t *testing.T) {
	s := newStore(t)
	n, err := s.Upsert(t.Context(), nil)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestUpsertScopesUniquenessToTheIndexer(t *testing.T) {
	// The key is (indexer, guid). Two indexers reusing the same guid string
	// -- which they do; "12345" is a popular guid -- are two distinct rows.
	s := newStore(t)
	mustUpsert(t, t.Context(), s, 2,
		rel("nzbgeek", "12345", "The Matrix 1999"),
		rel("drunkenslug", "12345", "Dune 2021"),
	)
}
