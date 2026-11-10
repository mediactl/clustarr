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

package ui

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// A reading order sorts by position as numbers when both read as one --
// so 10 follows 2, and 2.5 sits between 2 and 3 -- by text when one does
// not, then by release date and title.
func TestBookXBeforeOrdersAReadingOrder(t *testing.T) {
	row := func(name, released string) views.BookRow {
		return views.BookRow{Name: name, Title: name, ReleaseDate: released}
	}
	books := []bookXOrdered{
		{row("ten", "1990-01-01"), "10"},
		{row("two-and-a-half", "1980-01-01"), "2.5"},
		{row("two", "1971-01-01"), "2"},
		{row("one", "1968-01-01"), "1"},
		{row("prequel", "2018-01-01"), "0.5"},
		{row("one-to-three", "2000-01-01"), "1-3"},
		{row("omnibus-b", "2001-01-01"), "omnibus"},
		{row("omnibus-a", "1999-01-01"), "omnibus"},
	}
	sort.SliceStable(books, func(i, j int) bool { return bookXBefore(books[i], books[j]) })
	var got []string
	for _, b := range books {
		got = append(got, b.row.Name)
	}
	// "1-3" reads as 1 and ties with "one" by number, so the earlier
	// release wins; "omnibus" reads as no number and sorts as text after
	// every number's text, the two omnibuses by date.
	assert.Equal(t, []string{"prequel", "one", "one-to-three", "two", "two-and-a-half", "ten", "omnibus-a", "omnibus-b"}, got)
}

// The Editions table lists the pinned edition first and marked, carries
// each edition's spec.editions monitoring override, and reads nothing from
// a work without editions.
func TestBookXEditionsPinAndMonitor(t *testing.T) {
	released := metav1.NewTime(time.Date(1974, time.May, 1, 0, 0, 0, 0, time.UTC))
	b := &catalogv1.Book{
		Spec: catalogv1.BookSpec{EditionID: new("ebook"), Editions: []catalogv1.EditionSpec{{ID: "hardcover", Monitored: new(false)}}},
		Status: catalogv1.BookStatus{Metadata: &catalogv1.BookMetadata{Editions: []catalogv1.Edition{
			{ID: "hardcover", Format: "Hardcover", Language: "en", PageCount: 341, ReleaseDate: &released, ISBN13: "9780060125639"},
			{ID: "ebook", Format: "EPUB", Language: "de", IsEbook: true},
		}}},
	}
	rows := bookXEditions(b)
	require.Len(t, rows, 2)
	assert.Equal(t, "ebook", rows[0].ID)
	assert.True(t, rows[0].Pinned)
	assert.True(t, rows[0].Monitored, "no override keeps the CRD default")
	assert.True(t, rows[0].Ebook)
	assert.Equal(t, "German", rows[0].Language)
	assert.Equal(t, "hardcover", rows[1].ID)
	assert.False(t, rows[1].Pinned)
	assert.False(t, rows[1].Monitored)
	assert.Equal(t, "1974-05-01", rows[1].Release)
	assert.Equal(t, "English", rows[1].Language)

	assert.Equal(t, "9780060125639", bookXISBN(b), "the pinned edition has no ISBN, so the first that has one")
	b.Status.Metadata.ExternalIDs = map[string]string{"isbn13": "9780000000000"}
	assert.Equal(t, "9780000000000", bookXISBN(b), "the work's own id first")

	assert.Nil(t, bookXEditions(&catalogv1.Book{}))
	assert.Nil(t, bookXEditions(&catalogv1.Book{Status: catalogv1.BookStatus{Metadata: &catalogv1.BookMetadata{}}}))
}

// The logos a book links: its Open Library work always, the metadata's
// Hardcover, Goodreads and Wikipedia links when it has them, a Goodreads
// page built from the id otherwise, and nothing from a bare Hardcover id.
func TestBookXLinksBuildTheLogos(t *testing.T) {
	b := &catalogv1.Book{Spec: catalogv1.BookSpec{WorkID: "OL45883W"}}
	assert.Equal(t, []views.Link{{Label: "Open Library", Href: "https://openlibrary.org/works/OL45883W", Provider: views.ProviderOpenLibrary}}, bookXLinks(b))

	b.Status.Metadata = &catalogv1.BookMetadata{
		ExternalIDs: map[string]string{"goodreads": "13651", "hardcover": "4242"},
		Links:       []catalogv1.Link{{Type: "wikidata", URL: "https://www.wikidata.org/wiki/Q1"}, {Type: "hardcover", URL: "https://hardcover.app/books/the-dispossessed"}},
	}
	assert.Equal(t, []views.Link{
		{Label: "Open Library", Href: "https://openlibrary.org/works/OL45883W", Provider: views.ProviderOpenLibrary},
		{Label: "Hardcover", Href: "https://hardcover.app/books/the-dispossessed", Provider: views.ProviderHardcover},
		{Label: "Goodreads", Href: "https://www.goodreads.com/book/show/13651", Provider: views.ProviderGoodreads},
	}, bookXLinks(b))

	b.Status.Metadata.Links = append(b.Status.Metadata.Links, catalogv1.Link{Type: "goodreads", URL: "https://www.goodreads.com/book/show/13651-the-dispossessed"})
	links := bookXLinks(b)
	require.Len(t, links, 3)
	assert.Equal(t, "https://www.goodreads.com/book/show/13651-the-dispossessed", links[2].Href, "the metadata's own page over one built from the id")
}
