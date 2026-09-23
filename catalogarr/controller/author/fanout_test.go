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

package author_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/author"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func resourceQuantity(t *testing.T, s string) *resource.Quantity {
	t.Helper()
	q, err := resource.ParseQuantity(s)
	require.NoError(t, err)
	return &q
}

func TestBookName(t *testing.T) {
	name := author.BookName("tolkien", "OL45883W")
	require.True(t, len(name) > len("tolkien-"), "name should have a suffix: %q", name)
	require.Equal(t, "tolkien-", name[:len("tolkien-")])
	suffix := name[len("tolkien-"):]
	require.Len(t, suffix, 8)

	// Deterministic: the same inputs always produce the same name.
	assert.Equal(t, name, author.BookName("tolkien", "OL45883W"))
	// Different work ids produce different names.
	assert.NotEqual(t, name, author.BookName("tolkien", "OL27482W"))
}

func TestInitialBookMonitored(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)

	tests := []struct {
		name string
		mode catalogv1alpha1.AuthorMonitorMode
		date *time.Time
		want bool
	}{
		{"all/no date", catalogv1alpha1.AuthorMonitorAll, nil, true},
		{"all/past date", catalogv1alpha1.AuthorMonitorAll, &past, true},
		{"future/no date reads as not yet released", catalogv1alpha1.AuthorMonitorFuture, nil, true},
		{"future/future date", catalogv1alpha1.AuthorMonitorFuture, &future, true},
		{"future/past date", catalogv1alpha1.AuthorMonitorFuture, &past, false},
		{"missing/no date", catalogv1alpha1.AuthorMonitorMissing, nil, false},
		{"missing/past date", catalogv1alpha1.AuthorMonitorMissing, &past, true},
		{"missing/future date", catalogv1alpha1.AuthorMonitorMissing, &future, false},
		{"existing/past date", catalogv1alpha1.AuthorMonitorExisting, &past, false},
		{"none/past date", catalogv1alpha1.AuthorMonitorNone, &past, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := metadata.Book{FirstPublished: tt.date}
			assert.Equal(t, tt.want, author.InitialBookMonitored(tt.mode, b, now))
		})
	}
}

func TestMatchesProfile(t *testing.T) {
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("no filters set matches anything", func(t *testing.T) {
		assert.True(t, author.MatchesProfile(catalogv1alpha1.BookMetadataProfile{}, metadata.Book{}))
	})

	t.Run("SkipMissingDate drops a work with no date", func(t *testing.T) {
		p := catalogv1alpha1.BookMetadataProfile{SkipMissingDate: true}
		assert.False(t, author.MatchesProfile(p, metadata.Book{}))
		assert.True(t, author.MatchesProfile(p, metadata.Book{FirstPublished: &past}))
	})

	t.Run("SkipMissingISBN drops a work with no edition carrying an ISBN", func(t *testing.T) {
		p := catalogv1alpha1.BookMetadataProfile{SkipMissingISBN: true}
		assert.False(t, author.MatchesProfile(p, metadata.Book{}))
		assert.False(t, author.MatchesProfile(p, metadata.Book{Editions: []metadata.Edition{{IDs: metadata.ExternalIDs{}}}}))
		assert.True(t, author.MatchesProfile(p, metadata.Book{
			Editions: []metadata.Edition{{IDs: metadata.ExternalIDs{metadata.KeyISBN13: "9780000000000"}}},
		}))
	})

	t.Run("SkipPartsAndSets drops a boxed set", func(t *testing.T) {
		p := catalogv1alpha1.BookMetadataProfile{SkipPartsAndSets: true}
		assert.True(t, author.MatchesProfile(p, metadata.Book{}))
		assert.False(t, author.MatchesProfile(p, metadata.Book{Subjects: []string{"Boxed sets"}}))
	})

	t.Run("SkipSeriesSecondary drops a work only ever secondary in its series", func(t *testing.T) {
		p := catalogv1alpha1.BookMetadataProfile{SkipSeriesSecondary: true}
		assert.True(t, author.MatchesProfile(p, metadata.Book{}), "no series links at all is not secondary")
		assert.False(t, author.MatchesProfile(p, metadata.Book{Series: []metadata.SeriesLink{{Series: "LOTR", Primary: false}}}))
		assert.True(t, author.MatchesProfile(p, metadata.Book{Series: []metadata.SeriesLink{{Series: "LOTR", Primary: true}}}))
	})

	t.Run("AllowedLanguages restricts to a matching edition", func(t *testing.T) {
		p := catalogv1alpha1.BookMetadataProfile{AllowedLanguages: []string{"eng"}}
		assert.False(t, author.MatchesProfile(p, metadata.Book{}))
		assert.False(t, author.MatchesProfile(p, metadata.Book{Editions: []metadata.Edition{{Language: "fre"}}}))
		assert.True(t, author.MatchesProfile(p, metadata.Book{Editions: []metadata.Edition{{Language: "fre"}, {Language: "eng"}}}))
	})

	t.Run("MinPages requires an edition meeting the page count", func(t *testing.T) {
		p := catalogv1alpha1.BookMetadataProfile{MinPages: 200}
		assert.False(t, author.MatchesProfile(p, metadata.Book{}))
		assert.False(t, author.MatchesProfile(p, metadata.Book{Editions: []metadata.Edition{{PageCount: 100}}}))
		assert.True(t, author.MatchesProfile(p, metadata.Book{Editions: []metadata.Edition{{PageCount: 250}}}))
	})

	t.Run("MinPopularity is a documented no-op: never disqualifies", func(t *testing.T) {
		v := resourceQuantity(t, "0.9")
		p := catalogv1alpha1.BookMetadataProfile{MinPopularity: v}
		assert.True(t, author.MatchesProfile(p, metadata.Book{}))
	})
}

func TestDesiredBooks(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "tolkien"},
		Spec: catalogv1alpha1.AuthorSpec{
			AddOptions: catalogv1alpha1.AuthorAddOptions{Monitor: catalogv1alpha1.AuthorMonitorAll},
		},
	}

	t.Run("dedups by work id, drops entries with no work id", func(t *testing.T) {
		books := []metadata.Book{
			{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL1W"}, Title: "First"},
			{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL1W"}, Title: "Duplicate"},
			{IDs: metadata.ExternalIDs{}, Title: "No work id"},
		}
		desired := author.DesiredBooks(a, false, nil, books, now)
		require.Len(t, desired, 1)
		assert.Equal(t, "OL1W", desired[0].WorkID)
	})

	t.Run("applies the metadata profile", func(t *testing.T) {
		a2 := *a
		a2.Spec.MetadataProfile = catalogv1alpha1.BookMetadataProfile{SkipMissingDate: true}
		past := now.Add(-24 * time.Hour)
		books := []metadata.Book{
			{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL1W"}, FirstPublished: &past},
			{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL2W"}}, // no date, dropped
		}
		desired := author.DesiredBooks(&a2, false, nil, books, now)
		require.Len(t, desired, 1)
		assert.Equal(t, "OL1W", desired[0].WorkID)
	})

	t.Run("first fan-out sets Monitored from AddOptions", func(t *testing.T) {
		books := []metadata.Book{{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL1W"}}}
		desired := author.DesiredBooks(a, false, nil, books, now)
		require.Len(t, desired, 1)
		require.NotNil(t, desired[0].Monitored)
		assert.True(t, *desired[0].Monitored)
	})

	t.Run("steady state leaves an already-existing book's Monitored untouched", func(t *testing.T) {
		books := []metadata.Book{{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL1W"}}}
		name := author.BookName("tolkien", "OL1W")
		desired := author.DesiredBooks(a, true, map[string]bool{name: true}, books, now)
		require.Len(t, desired, 1)
		assert.Nil(t, desired[0].Monitored)
	})

	t.Run("steady state applies MonitorNewItems to a brand-new book", func(t *testing.T) {
		a2 := *a
		a2.Spec.MonitorNewItems = catalogv1alpha1.MonitorNewChildrenNone
		books := []metadata.Book{{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL1W"}}}
		desired := author.DesiredBooks(&a2, true, nil, books, now)
		require.Len(t, desired, 1)
		require.NotNil(t, desired[0].Monitored)
		assert.False(t, *desired[0].Monitored)
	})
}
