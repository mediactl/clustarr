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
package rssmatcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestNameAndCreditKeys(t *testing.T) {
	assert.Equal(t, []string{"frank herbert"}, nameKeys("Frank Herbert"))
	assert.Equal(t, []string{"herbert frank", "frank herbert"}, nameKeys("Herbert, Frank"),
		"a catalogue's \"Last, First\" is keyed both ways round")
	assert.Equal(t, []string{"beatles"}, nameKeys("Beatles, The"), "the article goes either way round")
	assert.Nil(t, nameKeys(""))

	keys := creditKeys("Stephen King & Peter Straub")
	assert.Contains(t, keys, "stephen king", "a co-written book is found through either author")
	assert.Contains(t, keys, "peter straub")
	assert.Contains(t, creditKeys("Artist feat. Guest"), "artist")
}

func TestNonVideoIndexKeys(t *testing.T) {
	assert.Equal(t, []string{"radiohead"}, artistNameKeys(&catalogv1alpha1.Artist{Status: catalogv1alpha1.ArtistStatus{
		Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Radiohead", SortName: "Radiohead"},
	}}))
	assert.Nil(t, artistNameKeys(&catalogv1alpha1.Artist{}), "no metadata yet: not matchable by name")

	assert.Equal(t, []string{"radiohead|" + release.CleanTitle("Kid A")}, albumKeys(&catalogv1alpha1.Album{
		Spec:   catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead"},
		Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{Title: "Kid A"}},
	}))

	book := &catalogv1alpha1.Book{
		Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("frank-herbert")},
		Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{
			Title: "Dune", Subtitle: "Deluxe Edition", Editions: []catalogv1alpha1.Edition{{Title: "Duna"}},
		}},
	}
	assert.Equal(t, []string{"frank-herbert|dune", "frank-herbert|dune deluxe edition", "frank-herbert|duna"}, bookKeys(book),
		"every title the book's identity knows")
	book.Spec.AuthorRef = nil
	assert.Nil(t, bookKeys(book), "a standalone book fails closed on its author, so it is not indexed")

	assert.ElementsMatch(t, []string{"stephen king|talisman", "peter straub|talisman"}, audiobookKeys(&catalogv1alpha1.Audiobook{
		Status: catalogv1alpha1.AudiobookStatus{Metadata: &catalogv1alpha1.AudiobookMetadata{
			Title: "The Talisman", Authors: []catalogv1alpha1.NamedRef{{Name: "Stephen King"}, {Name: "Peter Straub"}},
		}},
	}))

	assert.Equal(t, []string{"batman"}, comicTitleKeys(&catalogv1alpha1.Comic{Status: catalogv1alpha1.ComicStatus{
		Metadata: &catalogv1alpha1.ComicMetadata{Title: "Batman"},
	}}))
	assert.Equal(t, []string{"batman-2016#50"}, issueKeys(&catalogv1alpha1.Issue{
		Spec: catalogv1alpha1.IssueSpec{ComicRef: "batman-2016", Number: "050"},
	}))
}
