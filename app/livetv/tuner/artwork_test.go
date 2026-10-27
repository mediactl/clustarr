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

package tuner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// A guide build takes what the cache has and never waits: an unknown title
// is queued for the lookup pass and answers nothing, a hit answers its
// poster until it ages out, a miss answers nothing until it ages out.
func TestProgrammeArtResolvesFromTheCacheAndQueuesTheRest(t *testing.T) {
	now := time.Date(2026, 10, 27, 20, 0, 0, 0, time.UTC)
	a := &programmeArt{entries: map[string]artEntry{
		artKey(xmltv.ArtQuery{Title: "Friends"}):          {URL: "https://img/friends.jpg", Kind: "series", At: now.Add(-time.Hour)},
		artKey(xmltv.ArtQuery{Title: "Paid Programming"}): {At: now.Add(-time.Hour)},
		artKey(xmltv.ArtQuery{Title: "Old Hit"}):          {URL: "https://img/old.jpg", Kind: "movie", At: now.Add(-artHitTTL - time.Hour)},
		artKey(xmltv.ArtQuery{Title: "Old Miss"}):         {At: now.Add(-artMissTTL - time.Hour)},
	}}
	art, ok := a.resolve(now, xmltv.ArtQuery{Title: "Friends"})
	require.True(t, ok)
	assert.Equal(t, xmltv.Art{URL: "https://img/friends.jpg", Kind: xmltv.ArtSeries}, art)
	_, ok = a.resolve(now, xmltv.ArtQuery{Title: "Paid Programming"})
	assert.False(t, ok, "a fresh miss is a miss")
	assert.Empty(t, a.pending, "nothing is queued for what the cache answers")

	for _, title := range []string{"Old Hit", "Old Miss", "Brand New"} {
		_, ok = a.resolve(now, xmltv.ArtQuery{Title: title})
		assert.False(t, ok, title)
	}
	assert.Len(t, a.pending, 3, "aged entries and unknown titles are queued")
	assert.Len(t, a.take(10), 3, "a pass takes them all")
}

// take hands a pass its batch once; a second pass waits for the first.
func TestProgrammeArtTakesOnePassAtATime(t *testing.T) {
	a := &programmeArt{}
	for _, title := range []string{"A", "B", "C"} {
		a.resolve(time.Now(), xmltv.ArtQuery{Title: title})
	}
	batch := a.take(2)
	require.Len(t, batch, 2)
	assert.Nil(t, a.take(2), "a pass is running")
	a.mu.Lock()
	a.running = false
	a.mu.Unlock()
	assert.Len(t, a.take(2), 1, "the rest come with the next pass")
}

// searchArt asks for a series first and a movie second, a movie first on a
// movie channel or for a film-length programme, and takes only a hit whose
// title is the programme's, within a year of its year when it has one.
func TestSearchArtPrefersTheLikelyKindAndMatchesTitlesExactly(t *testing.T) {
	hit := func(title string, year int32, poster string) []byte {
		b, _ := json.Marshal(map[string]any{"ids": map[string]string{"tmdb": "1"}, "title": title, "year": year, "poster": poster})
		return b
	}
	var asked []commonv1.MediaKind
	search := func(_ context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error) {
		asked = append(asked, req.Kind)
		switch req.Kind {
		case commonv1.MediaKindMovie:
			return schema.MetadataResponse{Results: [][]byte{hit("Heat", 1995, "https://img/heat95.jpg"), hit("Heat", 1986, "https://img/heat86.jpg")}}, nil
		default:
			return schema.MetadataResponse{Results: [][]byte{hit("Friends", 1994, "https://img/friends.jpg"), hit("Friends: The Reunion", 2021, "https://img/reunion.jpg")}}, nil
		}
	}
	tn := &Tuner{o: Options{Search: search, Now: time.Now}, base: context.Background()}

	e, err := tn.searchArt(xmltv.ArtQuery{Title: "Friends", Channel: xmltv.LineupChannel{Group: "Entertainment"}, Duration: 30 * time.Minute})
	require.NoError(t, err)
	assert.Equal(t, "https://img/friends.jpg", e.URL)
	assert.Equal(t, "series", e.Kind)
	assert.Equal(t, []commonv1.MediaKind{commonv1.MediaKindSeries}, asked, "the series answered, the movie was never asked")

	asked = nil
	e, err = tn.searchArt(xmltv.ArtQuery{Title: "Heat", Year: 1995, Channel: xmltv.LineupChannel{Group: "Movies"}, Duration: 3 * time.Hour})
	require.NoError(t, err)
	assert.Equal(t, "https://img/heat95.jpg", e.URL, "the year picks the 1995 film over the 1986 one")
	assert.Equal(t, "movie", e.Kind)
	assert.Equal(t, commonv1.MediaKindMovie, asked[0], "a movie channel asks for a movie first")

	e, err = tn.searchArt(xmltv.ArtQuery{Title: "Something Else", Channel: xmltv.LineupChannel{Group: "Entertainment"}})
	require.NoError(t, err)
	assert.Empty(t, e.URL, "no exact title is a miss")
	assert.False(t, e.At.IsZero(), "a miss is dated, so it ages out")
}
