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

package search

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func rel(indexerRef, guid, hash string, seeders int32) schema.Release {
	return schema.Release{Info: commonv1.ReleaseInfo{
		IndexerRef: indexerRef,
		GUID:       guid,
		Title:      guid,
		InfoHash:   hash,
		Seeders:    ptr.To(seeders),
	}}
}

// Priority beats seeders: spec §6.2 keeps the best (priority, seeders), in
// that order. A public indexer with nine hundred seeders must not displace
// the private one the operator ranked first.
func TestMergeCollapsesOnInfoHashAndPriorityWins(t *testing.T) {
	got, truncated := mergeReleases([]indexerResult{
		{Name: "a", Priority: 10, Releases: []schema.Release{rel("a", "guid-a", "ABC123", 5)}},
		{Name: "b", Priority: 25, Releases: []schema.Release{rel("b", "guid-b", "abc123", 900)}},
	}, schema.MaxSearchReleases)

	require.False(t, truncated)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Info.IndexerRef)
}

func TestMergeBreaksAPriorityTieOnSeeders(t *testing.T) {
	got, _ := mergeReleases([]indexerResult{
		{Name: "a", Priority: 25, Releases: []schema.Release{rel("a", "guid-a", "abc123", 5)}},
		{Name: "b", Priority: 25, Releases: []schema.Release{rel("b", "guid-b", "abc123", 900)}},
	}, schema.MaxSearchReleases)

	require.Len(t, got, 1)
	require.Equal(t, "b", got[0].Info.IndexerRef)
}

// The second key: one indexer repeating a guid inside one page.
func TestMergeCollapsesARepeatedGUIDFromOneIndexer(t *testing.T) {
	got, _ := mergeReleases([]indexerResult{{
		Name: "a", Priority: 25,
		Releases: []schema.Release{rel("a", "dup", "", 1), rel("a", "dup", "", 1)},
	}}, schema.MaxSearchReleases)

	require.Len(t, got, 1)
}

// CARRIED ITEM, pinned executably: a usenet release offered by two indexers
// has no infohash and two distinct guids, so it is NOT collapsed. Inventing a
// title+size key was considered and rejected -- a wrong dedupe silently loses
// releases.
func TestMergeDoesNotCollapseUsenetAcrossIndexers(t *testing.T) {
	a := rel("a", "guid-a", "", 0)
	a.Info.Title = "Some.Movie.2010.1080p"
	b := rel("b", "guid-b", "", 0)
	b.Info.Title = "Some.Movie.2010.1080p"

	got, _ := mergeReleases([]indexerResult{
		{Name: "a", Priority: 10, Releases: []schema.Release{a}},
		{Name: "b", Priority: 25, Releases: []schema.Release{b}},
	}, schema.MaxSearchReleases)

	require.Len(t, got, 2)
}

func TestMergeCapsAndReportsTruncation(t *testing.T) {
	make501 := func(n int) []schema.Release {
		out := make([]schema.Release, 0, n)
		for i := range n {
			out = append(out, rel("a", strconv.Itoa(i), "", 0))
		}
		return out
	}

	got, truncated := mergeReleases([]indexerResult{{Name: "a", Releases: make501(501)}}, 500)
	require.Len(t, got, 500)
	require.True(t, truncated)

	got, truncated = mergeReleases([]indexerResult{{Name: "a", Releases: make501(500)}}, 500)
	require.Len(t, got, 500)
	require.False(t, truncated)
}

// The cap the merge is asked for and the cap the wire carries are one number.
func TestMergeCapIsTheSchemaCap(t *testing.T) {
	rels := make([]schema.Release, 0, schema.MaxSearchReleases+1)
	for i := range schema.MaxSearchReleases + 1 {
		rels = append(rels, schema.Release{Info: commonv1.ReleaseInfo{
			IndexerRef: "a", GUID: strconv.Itoa(i),
		}})
	}
	got, truncated := mergeReleases(
		[]indexerResult{{Name: "a", Releases: rels}}, schema.MaxSearchReleases)
	require.Len(t, got, schema.MaxSearchReleases)
	require.True(t, truncated)
}

// The order decides WHICH releases survive the cap, so it must be the same
// every time for the same inputs -- including when a later, better duplicate
// replaces an earlier one.
func TestMergeIsDeterministic(t *testing.T) {
	in := []indexerResult{
		{Name: "b", Priority: 25, Releases: []schema.Release{
			rel("b", "b1", "hash-1", 900), rel("b", "b2", "", 3),
		}},
		{Name: "a", Priority: 10, Releases: []schema.Release{
			rel("a", "a1", "hash-1", 1), rel("a", "a2", "", 2),
		}},
	}
	first, _ := mergeReleases(in, 10)
	for range 20 {
		again, _ := mergeReleases(in, 10)
		require.Equal(t, first, again)
	}
	// a's copy of hash-1 wins on priority even though b saw it first, and
	// the collapsed pair keeps b's arrival slot rather than jumping the
	// queue -- that stability is what makes the cap reproducible.
	require.Len(t, first, 3)
	guids := []string{first[0].Info.GUID, first[1].Info.GUID, first[2].Info.GUID}
	require.Contains(t, guids, "a1")
	require.NotContains(t, guids, "b1")
}
