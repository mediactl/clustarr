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

package extended_test

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
)

func TestFromPeopleOrdersAndCaps(t *testing.T) {
	var people []metadata.Person
	for i := range 300 {
		people = append(people, metadata.Person{Kind: metadata.PersonCast, Name: "actor", Order: int32(i)})
	}
	rand.Shuffle(len(people), func(i, j int) { people[i], people[j] = people[j], people[i] })
	for range 30 {
		people = append(people, metadata.Person{Kind: metadata.PersonDirector, Name: "d", Job: "Director"})
	}
	people = append(people, metadata.Person{Kind: metadata.PersonWriter, Name: "w", Job: "Screenplay"},
		metadata.Person{Kind: metadata.PersonProducer, Name: "p", Job: "Producer"})
	similar := []metadata.SimilarRef{{Title: "Badlands", Year: 1973, IDs: metadata.ExternalIDs{"tmdb": "3133"}}}

	d := extended.FromPeople(people, similar)
	require.Len(t, d.Role, extended.MaxRole)
	for i, p := range d.Role {
		assert.EqualValues(t, i, p.Order, "billing order")
	}
	assert.Len(t, d.Director, extended.MaxCrew)
	assert.Equal(t, "Screenplay", d.Writer[0].Job)
	assert.Equal(t, "Producer", d.Producer[0].Job)
	assert.Equal(t, []extended.Similar{{Title: "Badlands", Year: 1973, TmdbID: 3133}}, d.Similar)
}

func TestEncodeStaysUnderTheDocumentCap(t *testing.T) {
	var people []metadata.Person
	for i := range 50 {
		people = append(people, metadata.Person{Kind: metadata.PersonCast, Name: strings.Repeat("n", 4096), Order: int32(i)})
	}
	b, err := extended.Encode(extended.FromPeople(people, nil))
	require.NoError(t, err)
	assert.LessOrEqual(t, len(b), extended.MaxDocBytes)
	d, err := extended.Decode(b)
	require.NoError(t, err)
	assert.NotEmpty(t, d.Role, "trimmed, not emptied")
	assert.EqualValues(t, 0, d.Role[0].Order, "the top of the bill survives")
}

func TestKeyIsAValidKVKey(t *testing.T) {
	for _, k := range []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindEpisode} {
		key := extended.Key(k, "95b3d1b1-5840-47c6-9385-3513f35e1f56")
		assert.True(t, events.ValidKVKey(key), key)
	}
}

// A movie's document carries its collection's summary and artwork, which
// the Plex provider shows on the collection; no CRD carries them.
func TestFromCollectionKeepsTheSummaryAndArtwork(t *testing.T) {
	got := extended.FromCollection(&metadata.Collection{
		Title:    "Back to the Future Collection",
		Overview: "Marty McFly and Doc Brown travel in time.",
		Images: []metadata.Image{
			{Type: metadata.ImageTypePoster, URL: "https://image.tmdb.org/t/p/w500/poster.jpg"},
			{Type: metadata.ImageTypeFanart, URL: "https://image.tmdb.org/t/p/original/backdrop.jpg"},
		},
	})
	require.Equal(t, &extended.Collection{
		Summary: "Marty McFly and Doc Brown travel in time.",
		Poster:  "https://image.tmdb.org/t/p/w500/poster.jpg",
		Art:     "https://image.tmdb.org/t/p/original/backdrop.jpg",
	}, got)
	require.Nil(t, extended.FromCollection(nil))

	b, err := extended.Encode(extended.Doc{Collection: got})
	require.NoError(t, err)
	back, err := extended.Decode(b)
	require.NoError(t, err)
	require.Equal(t, got, back.Collection)
}

// An artist's document (2026-11-10 design §5.4): the members under Role
// with their instrument as the role, the related artists under Similar by
// MusicBrainz id, each list at its cap; a provider carrying neither gives
// an empty document rather than none.
func TestFromArtistFilesMembersAndSimilarArtists(t *testing.T) {
	a := &metadata.Artist{
		Name: "Radiohead",
		Members: []metadata.Person{
			{Kind: metadata.PersonMember, Name: "Thom Yorke", Job: "lead vocals", Order: 0},
			{Kind: metadata.PersonMember, Name: "Jonny Greenwood", Job: "guitar", Order: 1},
			{Kind: metadata.PersonCast, Name: "not a member"},
			{Kind: metadata.PersonMember, Name: ""},
		},
		Similar: []metadata.SimilarRef{{Title: "Portishead", IDs: metadata.ExternalIDs{metadata.KeyMBArtist: "8f6bd1e4-fbe1-4f50-aa9b-94c450ec0f11"}}},
	}
	d := extended.FromArtist(a)
	require.Equal(t, []extended.Person{
		{Name: "Thom Yorke", Character: "lead vocals", Job: "lead vocals", Order: 0},
		{Name: "Jonny Greenwood", Character: "guitar", Job: "guitar", Order: 1},
	}, d.Role, "a credit of another kind, or without a name, is no member")
	require.Equal(t, []extended.Similar{{Title: "Portishead", MBArtistID: "8f6bd1e4-fbe1-4f50-aa9b-94c450ec0f11"}}, d.Similar)
	require.Empty(t, d.Writer)

	for i := range 100 {
		a.Members = append(a.Members, metadata.Person{Kind: metadata.PersonMember, Name: "m", Order: int32(i)})
		a.Similar = append(a.Similar, metadata.SimilarRef{Title: "s"})
	}
	d = extended.FromArtist(a)
	assert.Len(t, d.Role, extended.MaxRole)
	assert.Len(t, d.Similar, extended.MaxSimilar)

	empty := extended.FromArtist(&metadata.Artist{Name: "No relations"})
	assert.Equal(t, extended.Doc{}, empty, "no members and no similar artists is an empty document, still written")
	assert.Equal(t, extended.Doc{}, extended.FromArtist(nil))
}

// An audiobook's document: the narrators as its Role list (Plex shows a
// Role's Character, so each reads "Narrator"), the authors as its Writer
// list, in the recording's own order and at the caps.
func TestFromAudiobookFilesNarratorsAsRoleAndAuthorsAsWriter(t *testing.T) {
	a := &metadata.Audiobook{
		Title:     "Good Omens",
		Authors:   []metadata.NamedRef{{Name: "Terry Pratchett", ASIN: "B000AP9A6K"}, {Name: "Neil Gaiman"}, {Name: ""}},
		Narrators: []string{"Martin Jarvis", ""},
	}
	d := extended.FromAudiobook(a)
	require.Equal(t, []extended.Person{{Name: "Martin Jarvis", Character: "Narrator", Job: "Narrator", Order: 0}}, d.Role)
	require.Equal(t, []extended.Person{
		{Name: "Terry Pratchett", Job: "Author", Order: 0},
		{Name: "Neil Gaiman", Job: "Author", Order: 1},
	}, d.Writer)
	require.Empty(t, d.Director)
	require.Empty(t, d.Similar)

	for range 100 {
		a.Narrators = append(a.Narrators, "n")
		a.Authors = append(a.Authors, metadata.NamedRef{Name: "a"})
	}
	d = extended.FromAudiobook(a)
	assert.Len(t, d.Role, extended.MaxRole)
	assert.Len(t, d.Writer, extended.MaxCrew)
	assert.Equal(t, extended.Doc{}, extended.FromAudiobook(nil))

	b, err := extended.Encode(extended.FromAudiobook(&metadata.Audiobook{Narrators: []string{"Rob Inglis"}, Authors: []metadata.NamedRef{{Name: "J.R.R. Tolkien"}}}))
	require.NoError(t, err)
	back, err := extended.Decode(b)
	require.NoError(t, err)
	assert.Equal(t, "Rob Inglis", back.Role[0].Name)
	assert.Equal(t, "J.R.R. Tolkien", back.Writer[0].Name)
}

func TestKeyIsAValidKVKeyForTheNonVideoKinds(t *testing.T) {
	for _, k := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAudiobook} {
		key := extended.Key(k, "95b3d1b1-5840-47c6-9385-3513f35e1f56")
		assert.True(t, events.ValidKVKey(key), key)
	}
}
