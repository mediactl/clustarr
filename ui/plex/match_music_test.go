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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
)

// TestMatchArtistByMBID is design §7.3 type 8, the guid rule: "mbid://" of
// the artist resolves straight to the Artist.
func TestMatchArtistByMBID(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 8, "guid": "mbid://" + artistMBID, "title": "Not The Name",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "match_artist")
}

// TestMatchArtistByTitle is the title rule: the name, its sort name
// ("Harbour Lights, The") and an alias all normalise to the artist, with
// or without a disambiguation; an unknown name answers the empty
// container, never 404.
func TestMatchArtistByTitle(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	for _, title := range []string{"The Harbour Lights", "Harbour Lights, The", "harbour lights", "The Harbour Lights (UK band)"} {
		rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{"type": 8, "title": title})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, []string{string(artistUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "title %q", title)
	}
	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{"type": 8, "title": "Nobody Here"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t,
		`{"MediaContainer":{"offset":0,"totalSize":0,"identifier":"tv.plex.agents.custom.clustarr.music","size":0,"Metadata":[]}}`,
		rec.Body.String())
}

// TestMatchArtistNeverGuessesBetweenNamesakes is §7.3's "several equal
// matches answer every candidate ranked (manual) or none (automatic)": two
// Artists of one name answer nothing to an automatic match and both to a
// manual one.
func TestMatchArtistNeverGuessesBetweenNamesakes(t *testing.T) {
	other := fixtureArtist()
	other.Name, other.UID = "the-harbour-lights-2", "70707070-7070-7070-7070-707070707070"
	other.Spec.MusicBrainzID = "0f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f9"
	other.Status.Metadata.Disambiguation = "Australian duo"
	objs := append(musicObjects(), other)
	h := newMusicHandler(t, externalURLFixture, objs...)

	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{"type": 8, "title": "The Harbour Lights"})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()), "automatic: two equal matches are no match")

	rec = postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{"type": 8, "title": "The Harbour Lights", "manual": 1})
	require.ElementsMatch(t, []string{string(artistUID), string(other.UID)}, matchedRatingKeys(t, rec.Body.Bytes()), "manual: both, ranked")
}

// TestMatchArtistByFile is rule 0 for type 8: the file's album's artist,
// whatever the title says.
func TestMatchArtistByFile(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 8, "title": "Not The Name", "filename": "The Harbour Lights/Low Water (2016)/01 - The Tide Comes In.flac",
	})
	require.Equal(t, []string{string(artistUID)}, matchedRatingKeys(t, rec.Body.Bytes()))
}

// TestMatchAlbumByArtistTitleYear is type 9's title rule: parentTitle (the
// artist) narrows the albums, the title picks one, and the year must fall
// within one of the release date for an automatic match.
func TestMatchAlbumByArtistTitleYear(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 9, "parentTitle": "The Harbour Lights", "title": "Low Water", "year": 2016,
	})
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "match_album")

	rec = postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 9, "parentTitle": "The Harbour Lights", "title": "Low Water", "year": 2017,
	})
	require.Equal(t, []string{string(albumUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "a year within one")

	rec = postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 9, "parentTitle": "The Harbour Lights", "title": "Low Water", "year": 1999,
	})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()), "a year off by more is refused automatically")

	rec = postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 9, "parentTitle": "The Harbour Lights", "title": "Low Water", "year": 1999, "manual": 1,
	})
	require.Equal(t, []string{string(albumUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "a manual match lists any year")

	rec = postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 9, "guid": "mbid://" + release2Group, "title": "Low Water",
	})
	require.Equal(t, []string{string(album2UID)}, matchedRatingKeys(t, rec.Body.Bytes()), "the release-group guid decides")
}

// TestMatchTrackByFile is type 10's one certain rule: the file names the
// track through its mediaRef.track.
func TestMatchTrackByFile(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 10, "title": "Wrong Title", "filename": "Low Water (2016)/01 - The Tide Comes In.flac",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "match_track")
}

// TestMatchTrackByNumberAndTitle: without a file, grandparentTitle and
// parentTitle resolve the album and parentIndex/index pick the track, else
// its title does.
func TestMatchTrackByNumberAndTitle(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 10, "grandparentTitle": "The Harbour Lights", "parentTitle": "Low Water", "parentIndex": 1, "index": 2,
	})
	require.Equal(t, []string{plex.TrackKey(albumUID, recording2)}, matchedRatingKeys(t, rec.Body.Bytes()), "by disc and number")

	rec = postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 10, "grandparentTitle": "The Harbour Lights", "parentTitle": "Low Water", "title": "the tide comes in",
	})
	require.Equal(t, []string{plex.TrackKey(albumUID, recording1)}, matchedRatingKeys(t, rec.Body.Bytes()), "by title")

	rec = postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 10, "grandparentTitle": "The Harbour Lights", "parentTitle": "Low Water", "index": 9,
	})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()), "a number the album lacks")
}

// TestMatchAudiobookRoot is the audiobooks root's three types: an author by
// the audiobook's asin:// guid and by name, an audiobook by asin and by
// author and title, a part by file and by index.
func TestMatchAudiobookRoot(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)

	rec := postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{"type": 8, "guid": "asin://" + audiobookASIN})
	require.Equal(t, []string{string(authorUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "the asin's author")
	rec = postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{"type": 8, "title": "Quayside, M. R."})
	require.Equal(t, []string{string(authorUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "the author by sort name")
	rec = postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{"type": 8, "title": "Ines Varga"})
	require.Equal(t, []string{plex.SyntheticArtistKey("Ines Varga")}, matchedRatingKeys(t, rec.Body.Bytes()), "a synthetic artist by name")

	rec = postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{"type": 9, "guid": "asin://" + audiobookASIN})
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "match_audiobook")
	rec = postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{
		"type": 9, "parentTitle": "M. R. Quayside", "title": "The Long Dock", "year": 2020,
	})
	require.Equal(t, []string{string(audiobookUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "by author, title and year")

	rec = postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{
		"type": 10, "filename": "The Long Dock (2020)/The Long Dock - Part 02.m4b",
	})
	require.Equal(t, []string{plex.PartKey(audiobookUID, 2)}, matchedRatingKeys(t, rec.Body.Bytes()), "a part by file")
	rec = postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{
		"type": 10, "grandparentTitle": "M. R. Quayside", "parentTitle": "The Long Dock", "index": 1,
	})
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "match_part")
	rec = postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{
		"type": 10, "grandparentTitle": "M. R. Quayside", "parentTitle": "The Long Dock", "title": "Closing",
	})
	require.Equal(t, []string{plex.PartKey(audiobookUID, 2)}, matchedRatingKeys(t, rec.Body.Bytes()), "a part by its chapter's title")
}

// TestMusicRootsServeOnlyTheirOwnKinds: both music roots declare 8, 9 and
// 10, so the type alone cannot tell an Artist from an Author; each root
// still answers only its own kinds, on every route, and neither answers a
// movie or a show. The video roots answer no music key.
func TestMusicRootsServeOnlyTheirOwnKinds(t *testing.T) {
	objs := append(musicObjects(), fixtureMovie())
	h := newMusicHandler(t, externalURLFixture, objs...)
	track := plex.TrackKey(albumUID, recording1)
	part := plex.PartKey(audiobookUID, 1)
	synthetic := plex.SyntheticArtistKey("Ines Varga")

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/plex/music/library/metadata/" + string(artistUID), http.StatusOK},
		{"/plex/music/library/metadata/" + string(albumUID), http.StatusOK},
		{"/plex/music/library/metadata/" + track, http.StatusOK},
		{"/plex/music/library/metadata/" + string(authorUID), http.StatusNotFound},
		{"/plex/music/library/metadata/" + string(audiobookUID), http.StatusNotFound},
		{"/plex/music/library/metadata/" + part, http.StatusNotFound},
		{"/plex/music/library/metadata/" + synthetic, http.StatusNotFound},
		{"/plex/music/library/metadata/" + string(movieUID), http.StatusNotFound},
		{"/plex/music/library/metadata/" + string(audiobookUID) + "/images", http.StatusNotFound},
		{"/plex/music/library/metadata/" + string(audiobookUID) + "/extras", http.StatusNotFound},
		{"/plex/music/library/metadata/" + string(audiobookUID) + "/children", http.StatusNotFound},
		{"/plex/audiobooks/library/metadata/" + string(authorUID), http.StatusOK},
		{"/plex/audiobooks/library/metadata/" + string(audiobookUID), http.StatusOK},
		{"/plex/audiobooks/library/metadata/" + part, http.StatusOK},
		{"/plex/audiobooks/library/metadata/" + synthetic, http.StatusOK},
		{"/plex/audiobooks/library/metadata/" + string(artistUID), http.StatusNotFound},
		{"/plex/audiobooks/library/metadata/" + string(albumUID), http.StatusNotFound},
		{"/plex/audiobooks/library/metadata/" + track, http.StatusNotFound},
		{"/plex/audiobooks/library/metadata/" + string(movieUID), http.StatusNotFound},
		{"/plex/audiobooks/library/metadata/" + string(albumUID) + "/images", http.StatusNotFound},
		{"/plex/movies/library/metadata/" + string(artistUID), http.StatusNotFound},
		{"/plex/movies/library/metadata/" + track, http.StatusNotFound},
		{"/plex/tv/library/metadata/" + string(audiobookUID), http.StatusNotFound},
		{"/plex/tv/library/metadata/" + part, http.StatusNotFound},
		{"/plex/tv/library/metadata/" + synthetic, http.StatusNotFound},
		{"/plex/tv/library/metadata/" + part + "/children", http.StatusNotFound},
	} {
		require.Equal(t, tc.want, getJSON(t, h, tc.path).Code, "GET %s", tc.path)
	}

	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{"/plex/music/library/metadata/matches", map[string]any{"type": 8, "title": "M. R. Quayside"}},
		{"/plex/music/library/metadata/matches", map[string]any{"type": 9, "guid": "asin://" + audiobookASIN}},
		{"/plex/audiobooks/library/metadata/matches", map[string]any{"type": 8, "title": "The Harbour Lights"}},
		{"/plex/audiobooks/library/metadata/matches", map[string]any{"type": 9, "guid": "mbid://" + releaseGroupID}},
		{"/plex/music/library/metadata/matches", map[string]any{"type": 1, "title": "Skyfall Protocol"}},
		{"/plex/movies/library/metadata/matches", map[string]any{"type": 8, "title": "The Harbour Lights"}},
	} {
		rec := postJSON(t, h, tc.path, tc.body)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()), "POST %s %v", tc.path, tc.body)
	}
}

// TestAnAlbumFileNeverAnswersAnAudiobookRequest: the file rules filter by
// kind, so an album's file resolves nothing on the audiobooks root.
func TestAnAlbumFileNeverAnswersAnAudiobookRequest(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := postJSON(t, h, "/plex/audiobooks/library/metadata/matches", map[string]any{
		"type": 9, "title": "Nothing Like This", "filename": "Low Water (2016)/01 - The Tide Comes In.flac",
	})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()))
}

// TestMatchAlbumByFileNeverPicksBetweenTwoAlbums: the same relative path
// under two RootFolders backing two Albums answers nothing by file.
func TestMatchAlbumByFileNeverPicksBetweenTwoAlbums(t *testing.T) {
	second := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "first-light-01", Namespace: "default"},
		Spec:       fixtureAlbumFile().Spec,
	}
	second.Spec.MediaRef.Name = "the-harbour-lights-first-light"
	second.Spec.MediaRef.Track = "aa11bb22-cc33-dd44-ee55-ff6677889902"
	second.Spec.Path = "/data/media/music-flac/The Harbour Lights/Low Water (2016)/01 - The Tide Comes In.flac"
	objs := append(musicObjects(), client.Object(second))
	h := newMusicHandler(t, externalURLFixture, objs...)
	rec := postJSON(t, h, "/plex/music/library/metadata/matches", map[string]any{
		"type": 10, "filename": "Low Water (2016)/01 - The Tide Comes In.flac",
	})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()))
}
