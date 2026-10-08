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

package searchoutcome_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/catalog/searchoutcome"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestWithTruncationKeepsTheMarkerAtTheCap: status.indexerOutcomes caps at
// MaxIndexerOutcomes, and a truncation marker cut off by that cap would
// hide the very thing it reports. (Moved from the search worker with the
// helpers, A4.3: the worker writes the record; the Search controller maps
// it.)
func TestWithTruncationKeepsTheMarkerAtTheCap(t *testing.T) {
	full := make([]schema.SearchOutcome, 0, searchoutcome.MaxIndexerOutcomes)
	for i := range searchoutcome.MaxIndexerOutcomes {
		full = append(full, schema.SearchOutcome{IndexerName: fmt.Sprintf("idx-%03d", i)})
	}
	got := searchoutcome.Truncation(full, 500)
	require.Len(t, got, searchoutcome.MaxIndexerOutcomes)
	require.Equal(t, searchoutcome.TruncatedOutcomeName, got[len(got)-1].IndexerName)
	require.Equal(t, "idx-098", got[len(got)-2].IndexerName)

	few := searchoutcome.Truncation([]schema.SearchOutcome{{IndexerName: "idx"}}, 3)
	require.Len(t, few, 2)
	require.Equal(t, int32(3), few[1].Releases)
}

// TestMapOutcomesNamesTheNameless: a nameless outcome gets a reserved,
// numbered name instead of being dropped, and the reserved names can never
// be a real Indexer's (they are not DNS-1123 subdomains).
func TestMapOutcomesNamesTheNameless(t *testing.T) {
	got := searchoutcome.Map([]schema.SearchOutcome{
		{Status: schema.SearchOutcomeError, Error: "boom"},
		{IndexerName: "Display Only", Status: schema.SearchOutcomeOK},
		{Status: schema.SearchOutcomeSkipped},
	})
	require.Len(t, got, 3)
	require.Equal(t, searchoutcome.UnnamedOutcomeName(1), got[0].Name)
	require.Equal(t, "boom", got[0].Error)
	require.Equal(t, "Display Only", got[1].Name)
	require.Equal(t, searchoutcome.UnnamedOutcomeName(2), got[2].Name)
	require.Contains(t, got[0].Name, "/")
}
