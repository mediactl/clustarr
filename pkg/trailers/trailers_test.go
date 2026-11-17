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

package trailers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/trailers"
)

func at(day int) time.Time { return time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC) }

func TestPickTakesTheOfficialTrailerInTheLanguage(t *testing.T) {
	yt := func(key, typ string, official bool, lang string, height, day int) trailers.Video {
		return trailers.Video{Site: "YouTube", Key: key, Type: typ, Official: official, Language: lang, Height: height, PublishedAt: at(day)}
	}
	cases := []struct {
		name   string
		videos []trailers.Video
		lang   string
		want   string
	}{
		{"a trailer before a teaser, however new the teaser", []trailers.Video{yt("teaser01", "Teaser", true, "en", 1080, 9), yt("trailer1", "Trailer", true, "en", 1080, 1)}, "en", "trailer1"},
		{"the rights holder's before a fan's", []trailers.Video{yt("fan00001", "Trailer", false, "en", 2160, 9), yt("official", "Trailer", true, "en", 720, 1)}, "en", "official"},
		{"the language asked for, then English, then any", []trailers.Video{yt("german01", "Trailer", true, "de", 1080, 9), yt("english1", "Trailer", true, "en", 1080, 1), yt("japanese", "Trailer", true, "ja", 1080, 5)}, "ja", "japanese"},
		{"English when the language has none", []trailers.Video{yt("german01", "Trailer", true, "de", 1080, 9), yt("english1", "Trailer", true, "en", 1080, 1)}, "ja", "english1"},
		{"the sharpest, then the newest", []trailers.Video{yt("old1080p", "Trailer", true, "en", 1080, 1), yt("new1080p", "Trailer", true, "en", 1080, 9), yt("new720p0", "Trailer", true, "en", 720, 20)}, "en", "new1080p"},
		{"the provider's order when nothing tells two apart", []trailers.Video{yt("first000", "Trailer", true, "en", 1080, 1), yt("second00", "Trailer", true, "en", 1080, 1)}, "en", "first000"},
		{"a teaser when there is no trailer", []trailers.Video{yt("featuret", "Featurette", true, "en", 1080, 9), yt("teaser01", "Teaser", true, "en", 1080, 1)}, "en", "teaser01"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := trailers.Pick(c.videos, c.lang)
			require.True(t, ok)
			assert.Equal(t, c.want, got.Key)
		})
	}
}

// Nothing is picked that is not a trailer or a teaser, that is hosted
// where nothing is fetched from, or whose id could be read as anything but
// an id.
func TestPickTakesNothingItCannotFetch(t *testing.T) {
	for _, v := range []trailers.Video{
		{Site: "YouTube", Key: "clip0001", Type: "Clip"},
		{Site: "YouTube", Key: "bts00001", Type: "Behind the Scenes"},
		{Site: "Dailymotion", Key: "x8abcd12", Type: "Trailer"},
		{Site: "YouTube", Key: "--exec=touch", Type: "Trailer"},
		{Site: "YouTube", Key: "../../etc", Type: "Trailer"},
		{Site: "YouTube", Key: "", Type: "Trailer"},
		{Site: "YouTube", Key: "a b", Type: "Trailer"},
	} {
		_, ok := trailers.Pick([]trailers.Video{v}, "en")
		assert.False(t, ok, "%+v", v)
	}
	_, ok := trailers.Pick(nil, "en")
	assert.False(t, ok)
}

func TestAVideosPageIsBuiltFromItsSiteAndID(t *testing.T) {
	assert.Equal(t, "https://www.youtube.com/watch?v=hnEwJKVWjFM", trailers.Video{Site: "YouTube", Key: "hnEwJKVWjFM"}.URL())
	assert.Equal(t, "https://www.youtube.com/watch?v=a_b-c123456", trailers.Video{Site: "youtube", Key: "a_b-c123456"}.URL())
	assert.Equal(t, "https://vimeo.com/123456789", trailers.Video{Site: "Vimeo", Key: "123456789"}.URL())
	assert.Empty(t, trailers.Video{Site: "YouTube", Key: "-leadingdash"}.URL())
}

// A decision stands for as long as its kind says, a failed fetch backs
// off, and a ready entry never lapses.
func TestAnEntryIsDueWhenItsDecisionHasLapsed(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	cases := []struct {
		name string
		e    trailers.Entry
		due  bool
	}{
		{"no entry", trailers.Entry{}, true},
		{"ready, years old", trailers.Entry{State: trailers.StateReady, CheckedAt: ago(1000 * 24 * time.Hour)}, false},
		{"plex, yesterday", trailers.Entry{State: trailers.StatePlex, CheckedAt: ago(24 * time.Hour)}, false},
		{"plex, a month ago", trailers.Entry{State: trailers.StatePlex, CheckedAt: ago(trailers.PlexRecheck)}, true},
		{"none, a week ago", trailers.Entry{State: trailers.StateNone, CheckedAt: ago(7 * 24 * time.Hour)}, false},
		{"none, two weeks ago", trailers.Entry{State: trailers.StateNone, CheckedAt: ago(trailers.NoneRecheck)}, true},
		{"failed, retry not yet", trailers.Entry{State: trailers.StateFailed, Attempts: 1, CheckedAt: ago(time.Minute), RetryAt: now.Add(time.Minute)}, false},
		{"failed, retry passed", trailers.Entry{State: trailers.StateFailed, Attempts: 1, CheckedAt: ago(2 * time.Hour), RetryAt: ago(time.Hour)}, true},
		{"failed for good, retry passed", trailers.Entry{State: trailers.StateFailed, Attempts: trailers.MaxAttempts, CheckedAt: ago(24 * time.Hour), RetryAt: ago(time.Hour)}, false},
		{"failed for good, two weeks on", trailers.Entry{State: trailers.StateFailed, Attempts: trailers.MaxAttempts, CheckedAt: ago(trailers.NoneRecheck), RetryAt: ago(time.Hour)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.due, c.e.Due(now)) })
	}
	assert.Equal(t, time.Hour, trailers.RetryAfter(1))
	assert.Equal(t, 4*time.Hour, trailers.RetryAfter(2))
	assert.Equal(t, 16*time.Hour, trailers.RetryAfter(3))
	assert.Equal(t, 7*24*time.Hour, trailers.RetryAfter(9))
}

func TestAnItemsTrailerLiesWhereNoLibraryWalkEnters(t *testing.T) {
	assert.Equal(t, "/data/media/movies/.clustarr/trailers/movie-11111111-2222.mp4", trailers.File("/data/media/movies", trailers.KindMovie, "11111111-2222"))
	e := trailers.Entry{State: trailers.StateReady, CheckedAt: at(1), Path: "/data/x.mp4", Video: &trailers.Video{Site: "YouTube", Key: "hnEwJKVWjFM"}}
	b, err := trailers.Encode(e)
	require.NoError(t, err)
	got, err := trailers.Decode(b)
	require.NoError(t, err)
	assert.Equal(t, e, got)
	assert.Equal(t, "movie.11111111-2222", trailers.Key(trailers.KindMovie, "11111111-2222"))
}

func TestAPlexExtraIsATrailerByItsTypeOrSubtype(t *testing.T) {
	assert.True(t, trailers.IsPlexTrailer(json.RawMessage(`{"type":"clip","subtype":"trailer","extraType":1}`)))
	assert.True(t, trailers.IsPlexTrailer(json.RawMessage(`{"type":"clip","extraType":1}`)))
	assert.True(t, trailers.IsPlexTrailer(json.RawMessage(`{"type":"clip","subtype":"Trailer"}`)))
	assert.False(t, trailers.IsPlexTrailer(json.RawMessage(`{"type":"clip","subtype":"behindTheScenes","extraType":5}`)))
	assert.False(t, trailers.IsPlexTrailer(json.RawMessage(`not json`)))
}

// A file is told to Plex by the path Plex mounts it at: under the longest
// mapping that holds it, on a directory's boundary; one no mapping holds
// cannot be told at all.
func TestAPathIsMappedToWherePlexMountsIt(t *testing.T) {
	maps := []trailers.PathMapping{{Clustarr: "/data/media", Plex: "/elsewhere"}, {Clustarr: "/data/media/movies", Plex: "/media/movies"}}
	got, ok := trailers.PlexPath(maps, "/data/media/movies/.clustarr/trailers/movie-1.mp4")
	require.True(t, ok)
	assert.Equal(t, "/media/movies/.clustarr/trailers/movie-1.mp4", got)
	got, ok = trailers.PlexPath(maps, "/data/media/tv/.clustarr/trailers/series-1.mp4")
	require.True(t, ok)
	assert.Equal(t, "/elsewhere/tv/.clustarr/trailers/series-1.mp4", got)
	_, ok = trailers.PlexPath(maps[1:], "/data/media/movies-4k/x.mp4")
	assert.False(t, ok, "a mapping holds a path only on a directory's boundary")
	_, ok = trailers.PlexPath(nil, "/data/media/movies/x.mp4")
	assert.False(t, ok)

	m, err := trailers.ParsePathMapping(" /data/media/movies/ = /media/movies ")
	require.NoError(t, err)
	assert.Equal(t, trailers.PathMapping{Clustarr: "/data/media/movies", Plex: "/media/movies"}, m)
	for _, bad := range []string{"", "/data/media", "data=/media", "/data=media", "=/media"} {
		_, err := trailers.ParsePathMapping(bad)
		assert.Error(t, err, bad)
	}
}

// Rank lists every video that may be fetched, best first and each page
// once, so one whose page has no video gives way to the next.
func TestRankListsEveryFetchableVideoBestFirstAndOnce(t *testing.T) {
	videos := []trailers.Video{
		{Site: "YouTube", Key: "teaser00001", Type: "Teaser", Official: true},
		{Site: "YouTube", Key: "fantrailer1", Type: "Trailer"},
		{Site: "YouTube", Key: "clip0000001", Type: "Clip", Official: true},
		{Site: "YouTube", Key: "official001", Type: "Trailer", Official: true},
		{Site: "YouTube", Key: "official001", Type: "Trailer", Official: true, Provider: "tvdb"},
	}
	var keys []string
	for _, v := range trailers.Rank(videos, "en") {
		keys = append(keys, v.Key)
	}
	assert.Equal(t, []string{"official001", "fantrailer1", "teaser00001"}, keys)
	assert.Empty(t, trailers.Rank(nil, "en"))
}
