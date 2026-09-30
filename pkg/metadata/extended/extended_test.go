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
