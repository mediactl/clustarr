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

	"github.com/mediactl/clustarr/ui/plex"
)

// TestMetadataArtist is GET /plex/music/library/metadata/{artist}
// ?includeChildren=1 (design §7.4, "artist"): the inferred artist shape,
// the thumb standing in for the poster, and Children of its albums oldest
// first.
func TestMetadataArtist(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := getJSON(t, h, "/plex/music/library/metadata/"+string(artistUID)+"?includeChildren=1")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_artist")
}

// TestMetadataAlbum is the album shape with Children of every track, a file
// or not (Q4), by disc and number.
func TestMetadataAlbum(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := getJSON(t, h, "/plex/music/library/metadata/"+string(albumUID)+"?includeChildren=1")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_album")
}

// TestMetadataTrack is GET .../{albumUID}-t{recording}: the track shape,
// parent album and grandparent artist resolved.
func TestMetadataTrack(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := getJSON(t, h, "/plex/music/library/metadata/"+plex.TrackKey(albumUID, recording1))
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_track")

	rec = getJSON(t, h, "/plex/music/library/metadata/"+plex.TrackKey(albumUID, "00000000-0000-0000-0000-000000000000"))
	require.Equal(t, http.StatusNotFound, rec.Code, "a recording the album does not list")
	rec = getJSON(t, h, "/plex/music/library/metadata/"+plex.TrackKey(album2UID, recording1))
	require.Equal(t, http.StatusNotFound, rec.Code, "a recording on another album")
}

// TestMetadataAuthor is the audiobooks root's artist level: an Author as
// an artist, with Children of its audiobooks.
func TestMetadataAuthor(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := getJSON(t, h, "/plex/audiobooks/library/metadata/"+string(authorUID)+"?includeChildren=1")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_author")
}

// TestMetadataAudiobook is an audiobook as an album: publisher as studio,
// narrators as Role, authors as Writer, asin and isbn guids, and Children
// of its parts titled by their chapters.
func TestMetadataAudiobook(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := getJSON(t, h, "/plex/audiobooks/library/metadata/"+string(audiobookUID)+"?includeChildren=1")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_audiobook")
}

// TestMetadataPart is GET .../{audiobookUID}-p01: a part as a track, its
// duration from its file's probe.
func TestMetadataPart(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	rec := getJSON(t, h, "/plex/audiobooks/library/metadata/"+plex.PartKey(audiobookUID, 1))
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_part")

	rec = getJSON(t, h, "/plex/audiobooks/library/metadata/"+plex.PartKey(audiobookUID, 3))
	require.Equal(t, http.StatusNotFound, rec.Code, "a part past the audiobook's files")
}

// TestMetadataSyntheticArtist is an Audiobook with no authorRef: its artist
// is the synthetic one keyed by the author's name, with no summary and no
// artwork, and the audiobook's parent fields point at it.
func TestMetadataSyntheticArtist(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	key := plex.SyntheticArtistKey("Ines Varga")
	rec := getJSON(t, h, "/plex/audiobooks/library/metadata/"+key+"?includeChildren=1")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_synthetic_artist")

	md := metadataOf(t, h, "/plex/audiobooks/library/metadata/"+string(unlinkedABUID))
	require.Equal(t, key, md["parentRatingKey"])
	require.Equal(t, "Ines Varga", md["parentTitle"])
	require.Equal(t, "tv.plex.agents.custom.clustarr.audiobooks://artist/"+key, md["parentGuid"])

	rec = getJSON(t, h, "/plex/audiobooks/library/metadata/"+plex.SyntheticArtistKey("Nobody"))
	require.Equal(t, http.StatusNotFound, rec.Code, "a name no unlinked audiobook credits")
}

// TestMusicChildrenAndGrandchildren is design §7.1's container routes: an
// artist's children are its albums oldest first, an album's its tracks,
// an artist's grandchildren every track across its albums -- paged lazily
// (§7.4), here page 2 of 3 with the true total -- and an album's
// grandchildren a 404, as a season's are.
func TestMusicChildrenAndGrandchildren(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)

	rec := getJSON(t, h, "/plex/music/library/metadata/"+string(artistUID)+"/children")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{string(album2UID), string(albumUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "albums oldest first")

	rec = getJSON(t, h, "/plex/music/library/metadata/"+string(albumUID)+"/children")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{plex.TrackKey(albumUID, recording1), plex.TrackKey(albumUID, recording2)}, matchedRatingKeys(t, rec.Body.Bytes()), "tracks by number")

	rec = getJSON(t, h, "/plex/music/library/metadata/"+string(artistUID)+"/grandchildren",
		"X-Plex-Container-Size", "1", "X-Plex-Container-Start", "1")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "1", rec.Header().Get("X-Plex-Container-Start"))
	require.Equal(t, "3", rec.Header().Get("X-Plex-Container-Total-Size"))
	require.Equal(t, []string{plex.TrackKey(albumUID, recording1)}, matchedRatingKeys(t, rec.Body.Bytes()), "page 2 of 3: the first track of the second album")
	requireGolden(t, rec, "grandchildren_page2")

	rec = getJSON(t, h, "/plex/music/library/metadata/"+string(artistUID)+"/grandchildren", "X-Plex-Container-Start", "3")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t,
		`{"MediaContainer":{"offset":3,"totalSize":3,"identifier":"tv.plex.agents.custom.clustarr.music","size":0,"Metadata":[]}}`,
		rec.Body.String(), "a start at the total is empty with the true total")

	require.Equal(t, http.StatusNotFound, getJSON(t, h, "/plex/music/library/metadata/"+string(albumUID)+"/grandchildren").Code)
	require.Equal(t, http.StatusNotFound, getJSON(t, h, "/plex/music/library/metadata/"+plex.TrackKey(albumUID, recording1)+"/children").Code, "a track has no children")

	rec = getJSON(t, h, "/plex/audiobooks/library/metadata/"+string(authorUID)+"/grandchildren")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{plex.PartKey(audiobookUID, 1), plex.PartKey(audiobookUID, 2)}, matchedRatingKeys(t, rec.Body.Bytes()), "an author's parts")
	rec = getJSON(t, h, "/plex/audiobooks/library/metadata/"+plex.SyntheticArtistKey("Ines Varga")+"/children")
	require.Equal(t, []string{string(unlinkedABUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "a synthetic artist's audiobooks")
}

// TestMusicImages is /images for the four kinds (design §7.4): an artist's
// thumb as its coverPoster beside its background and clearLogo, an album's
// cover, an author's photo, an audiobook's cover; a track's are its album's
// and a part's its audiobook's; a synthetic artist's are none.
func TestMusicImages(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	type gallery struct {
		MediaContainer struct {
			Image []struct{ Type, URL string } `json:"Image"`
		} `json:"MediaContainer"`
	}
	images := func(path string) map[string]string {
		rec := getJSON(t, h, path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		var g gallery
		require.NoError(t, decodeJSON(rec.Body.Bytes(), &g))
		out := map[string]string{}
		for _, img := range g.MediaContainer.Image {
			out[img.Type] = img.URL
		}
		return out
	}
	artist := images("/plex/music/library/metadata/" + string(artistUID) + "/images")
	require.Equal(t, map[string]string{
		"coverPoster": externalURLFixture + "/art/artist/" + string(artistUID) + "/thumb?v=artistthumbdigest1",
		"background":  externalURLFixture + "/art/artist/" + string(artistUID) + "/fanart?v=artistfanartdigest1",
		"clearLogo":   externalURLFixture + "/art/artist/" + string(artistUID) + "/logo?v=artistlogodigest1",
	}, artist)
	album := images("/plex/music/library/metadata/" + string(albumUID) + "/images")
	require.Equal(t, map[string]string{"coverPoster": externalURLFixture + "/art/album/" + string(albumUID) + "/poster?v=albumcoverdigest1"}, album)
	require.Equal(t, album, images("/plex/music/library/metadata/"+plex.TrackKey(albumUID, recording1)+"/images"), "a track's are its album's")
	require.Equal(t, map[string]string{"coverPoster": externalURLFixture + "/art/author/" + string(authorUID) + "/poster?v=authorphotodigest1"},
		images("/plex/audiobooks/library/metadata/"+string(authorUID)+"/images"))
	book := images("/plex/audiobooks/library/metadata/" + string(audiobookUID) + "/images")
	require.Equal(t, map[string]string{"coverPoster": externalURLFixture + "/art/audiobook/" + string(audiobookUID) + "/poster?v=audiobookcoverdigest1"}, book)
	require.Equal(t, book, images("/plex/audiobooks/library/metadata/"+plex.PartKey(audiobookUID, 2)+"/images"), "a part's are its audiobook's")
	require.Empty(t, images("/plex/audiobooks/library/metadata/"+plex.SyntheticArtistKey("Ines Varga")+"/images"))
}

// TestMusicExtrasAreEmpty is §7.4's "/extras answers an empty list for
// every music key": a music key has no Plex id, so the route answers [] and
// never asks the gateway or 404s, which PMS would read as "none" anyway.
func TestMusicExtrasAreEmpty(t *testing.T) {
	h := newMusicHandler(t, externalURLFixture, musicObjects()...)
	for _, path := range []string{
		"/plex/music/library/metadata/" + string(artistUID) + "/extras",
		"/plex/music/library/metadata/" + string(albumUID) + "/extras",
		"/plex/music/library/metadata/" + plex.TrackKey(albumUID, recording2) + "/extras",
		"/plex/audiobooks/library/metadata/" + string(authorUID) + "/extras",
		"/plex/audiobooks/library/metadata/" + string(audiobookUID) + "/extras",
		"/plex/audiobooks/library/metadata/" + plex.PartKey(audiobookUID, 1) + "/extras",
		"/plex/audiobooks/library/metadata/" + plex.SyntheticArtistKey("Ines Varga") + "/extras",
	} {
		rec := getJSON(t, h, path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		var body struct {
			MediaContainer struct {
				TotalSize int   `json:"totalSize"`
				Metadata  []any `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		require.NoError(t, decodeJSON(rec.Body.Bytes(), &body))
		require.Zero(t, body.MediaContainer.TotalSize, path)
		require.NotNil(t, body.MediaContainer.Metadata, path)
		require.Empty(t, body.MediaContainer.Metadata, path)
	}
	require.Equal(t, http.StatusNotFound, getJSON(t, h, "/plex/music/library/metadata/"+string(audiobookUID)+"/extras").Code, "a key the root does not serve")
}
