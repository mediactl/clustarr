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

package plex_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRootMusic is design §7.1's GET /plex/music: the MediaProvider
// declaring Plex's music types 8, 9 and 10 under the music identifier,
// with the match and metadata Features and no collection.
func TestRootMusic(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture)
	rec := getJSON(t, h, "/plex/music")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	requireGolden(t, rec, "root_music")
}

// TestRootAudiobooks is GET /plex/audiobooks: the same three types under
// the audiobooks identifier, since Plex has no audiobook library type.
func TestRootAudiobooks(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture)
	rec := getJSON(t, h, "/plex/audiobooks")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "root_audiobooks")
}

// TestEveryRootWithoutExternalURL is spec §D.1's 503 over all four roots:
// the music roots gate on --external-url as the video roots do.
func TestEveryRootWithoutExternalURL(t *testing.T) {
	h := newMusicHandler(t, "")
	for _, path := range []string{"/plex/movies", "/plex/tv", "/plex/music", "/plex/audiobooks"} {
		rec := getJSON(t, h, path)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "path %s", path)
		require.JSONEq(t, `{"error":"--external-url is required for the Plex provider"}`, rec.Body.String())
	}
}

// TestMusicRootsRegisterOnlyUnderTheFlag: without Options.MusicProvider
// (--plex-music-provider, off by default while no PMS build accepts a
// provider declaring 8, 9 or 10, design §7.6) nothing is mounted under
// /plex/music or /plex/audiobooks -- a plain 404 on every route, never a
// 503 -- while the video roots serve as before; with it, both roots answer.
func TestMusicRootsRegisterOnlyUnderTheFlag(t *testing.T) {
	off := newTestHandler(t, externalURLFixture, musicObjects()...)
	for _, path := range []string{
		"/plex/music", "/plex/audiobooks",
		"/plex/music/library/metadata/" + string(artistUID),
		"/plex/music/library/metadata/" + string(artistUID) + "/children",
		"/plex/music/library/metadata/" + string(artistUID) + "/images",
		"/plex/music/library/metadata/" + string(artistUID) + "/extras",
		"/plex/audiobooks/library/metadata/" + string(audiobookUID),
	} {
		require.Equal(t, http.StatusNotFound, getJSON(t, off, path).Code, "GET %s with the flag off", path)
	}
	rec := postJSON(t, off, "/plex/music/library/metadata/matches", map[string]any{"type": 8, "title": "The Harbour Lights"})
	require.Equal(t, http.StatusNotFound, rec.Code, "POST matches with the flag off")
	require.Equal(t, http.StatusOK, getJSON(t, off, "/plex/movies").Code, "the movies root serves as before")
	require.Equal(t, http.StatusOK, getJSON(t, off, "/plex/tv").Code, "the tv root serves as before")

	on := newMusicHandler(t, externalURLFixture, musicObjects()...)
	requireMusicRootsOn(t, on)
	require.Equal(t, http.StatusOK, getJSON(t, on, "/plex/music/library/metadata/"+string(artistUID)).Code)
	require.Equal(t, http.StatusOK, getJSON(t, on, "/plex/audiobooks/library/metadata/"+string(audiobookUID)).Code)
}
