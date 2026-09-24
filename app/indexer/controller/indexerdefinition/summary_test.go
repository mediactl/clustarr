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

package indexerdefinition

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// Cardigann and Torznab disagree on three of the five mode names, and nothing
// downstream reports a mismatch: torznab.Caps.Supports compares against the
// wire values, so a status carrying "tv-search" produces a caps gate that
// never matches and an indexer that is never queried.
func TestTorznabModesRenamesToTheWireVocabulary(t *testing.T) {
	got := torznabModes(map[string][]string{
		"search":       {"q"},
		"tv-search":    {"q", "season", "ep"},
		"movie-search": {"q", "imdbid"},
		"music-search": {"q", "artist"},
		"book-search":  {"q", "author"},
	})
	assert.Equal(t, map[string][]string{
		"search":   {"q"},
		"tvsearch": {"q", "season", "ep"},
		"movie":    {"q", "imdbid"},
		"music":    {"q", "artist"},
		"book":     {"q", "author"},
	}, got)

	// Every key produced must be one torznab itself recognises.
	for key := range got {
		assert.True(t, torznab.SearchMode(key) == torznab.ModeSearch ||
			torznab.SearchMode(key) == torznab.ModeTVSearch ||
			torznab.SearchMode(key) == torznab.ModeMovieSearch ||
			torznab.SearchMode(key) == torznab.ModeMusicSearch ||
			torznab.SearchMode(key) == torznab.ModeAudioSearch ||
			torznab.SearchMode(key) == torznab.ModeBookSearch,
			"%q is not a torznab.SearchMode", key)
	}
}

func TestTorznabModesDropsAnUnknownMode(t *testing.T) {
	// Unreachable through cardigann.Validate (the schema's Modes object is
	// additionalProperties: false), but passing an unknown key through would
	// be the silent never-matches failure, so it is dropped rather than kept.
	assert.Equal(t, map[string][]string{"search": {"q"}},
		torznabModes(map[string][]string{"search": {"q"}, "invented-search": {"q"}}))
	assert.Nil(t, torznabModes(nil))
}

func TestCategoryIDsAreSortedDedupedAndCapped(t *testing.T) {
	assert.Equal(t, []int32{2000, 5000},
		categoryIDs([]newznab.CategoryID{newznab.CatTV, newznab.CatMovies}),
		"sorted so a reordering inside the definition does not churn status")

	assert.Equal(t, []int32{2000}, categoryIDs([]newznab.CategoryID{2000, 2000}))
	assert.Nil(t, categoryIDs(nil))

	many := make([]newznab.CategoryID, 0, maxCategories+50)
	for i := range cap(many) {
		many = append(many, newznab.CategoryID(1000+i))
	}
	capped := categoryIDs(many)
	require.Len(t, capped, maxCategories,
		"status.caps.categories carries MaxItems=%d; exceeding it rejects the whole apply", maxCategories)
	assert.EqualValues(t, 1000, capped[0])
}

func TestSummarisePrivacyAndProtocol(t *testing.T) {
	for _, tc := range []struct {
		in   cardigann.DefinitionType
		want indexv1alpha1.DefinitionType
	}{
		{"public", indexv1alpha1.DefinitionTypePublic},
		{"semi-private", indexv1alpha1.DefinitionTypeSemiPrivate},
		{"private", indexv1alpha1.DefinitionTypePrivate},
		{"invented", ""}, // omitted rather than sent: status.type carries an enum
	} {
		t.Run(string(tc.in), func(t *testing.T) {
			got := summarise(&cardigann.Definition{ID: "x", Type: tc.in}, "yaml")
			assert.Equal(t, tc.want, got.Type)
		})
	}
}

func TestDigestIsStableHex(t *testing.T) {
	a := digest("id: x\n")
	assert.Len(t, a, 64)
	assert.Equal(t, a, digest("id: x\n"))
	assert.NotEqual(t, a, digest("id: y\n"))
}

func TestTruncateKeepsRuneBoundaries(t *testing.T) {
	assert.Equal(t, "short", truncate("short", 800))
	long := strings.Repeat("é", 100) // two bytes per rune
	got := truncate(long, 9)
	assert.True(t, strings.HasSuffix(got, "..."))
	assert.Equal(t, strings.Repeat("é", 4), strings.TrimSuffix(got, "..."),
		"a multi-byte rune was cut in half; invalid UTF-8 in a condition message is a rejected apply")
}

// summaryFrom is what makes the complete-declaration rule possible on a path
// that cannot recompute the summary: it re-sends what is already there.
func TestSummaryFromSeedsEveryOwnedField(t *testing.T) {
	st := indexv1alpha1.IndexerDefinitionStatus{
		ID: "id", Replaces: []string{"old-id"}, Name: "name", Language: "en-US",
		Type: indexv1alpha1.DefinitionTypePrivate, Protocol: "torrent", Sha256: "deadbeef",
		Caps: indexv1alpha1.CapsSummary{
			Modes:      map[string][]string{"search": {"q"}},
			Categories: []int32{2000},
		},
	}
	got := summaryFrom(st)
	assert.Equal(t, summary{
		ID: "id", Replaces: []string{"old-id"}, Name: "name", Language: "en-US",
		Type: indexv1alpha1.DefinitionTypePrivate, Protocol: "torrent", Sha256: "deadbeef",
		Modes:      map[string][]string{"search": {"q"}},
		Categories: []int32{2000},
	}, got, "a field missing here is a field the failure path releases")
}

// status.replaces keeps the declared order and drops what would say nothing
// (a blank, a repeat, the definition's own id), and is capped at the CRD's
// MaxItems, which would otherwise reject the whole status apply.
func TestReplacedIDs(t *testing.T) {
	assert.Nil(t, replacedIDs("x", nil))
	assert.Equal(t, []string{"b", "a"}, replacedIDs("x", []string{"b", "", "x", "a", "b"}))

	many := make([]string, 0, maxReplaces+5)
	for i := range maxReplaces + 5 {
		many = append(many, fmt.Sprintf("old-%d", i))
	}
	got := replacedIDs("x", many)
	assert.Len(t, got, maxReplaces)
	assert.Equal(t, "old-0", got[0])
}
