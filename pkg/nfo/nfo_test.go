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

package nfo_test

import (
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/nfo"
)

var update = flag.Bool("update", false, "rewrite the goldens under testdata")

// A release dated 1 January 00:30 UTC: a west-of-UTC replica reads the
// previous year from it unless every calendar field is taken in UTC.
var newYear = metav1.NewTime(time.Date(2004, 1, 1, 0, 30, 0, 0, time.UTC))

func artist() *catalogv1.Artist {
	return &catalogv1.Artist{
		Spec: catalogv1.ArtistSpec{MusicBrainzID: "9c9f1380-2516-4fc9-a3e6-f9f61941d090"},
		Status: catalogv1.ArtistStatus{Metadata: &catalogv1.ArtistMetadata{
			Name: "Muse", SortName: "Muse", Type: "Group", Disambiguation: "UK rock band",
			Overview: "Muse are an English rock band from Teignmouth, Devon.",
			Genres:   []string{"alternative rock", "progressive rock"},
			Begin:    "1994", Country: "GB",
		}},
	}
}

func album() *catalogv1.Album {
	return &catalogv1.Album{
		Spec: catalogv1.AlbumSpec{ArtistRef: "muse", ReleaseGroupID: "1a3d9f5c-rg"},
		Status: catalogv1.AlbumStatus{
			Metadata: &catalogv1.AlbumMetadata{
				Title: "Absolution", Overview: "The third studio album.", AlbumType: "Album",
				SecondaryTypes: nil, ReleaseDate: &newYear, Label: "Taste Media",
				Genres:            []string{"alternative rock"},
				SelectedReleaseID: "rel-1",
				Releases: []catalogv1.ReleaseSummary{
					{ID: "rel-1", Status: "Official", Country: "GB", ReleaseDate: ptrTime(time.Date(2004, 3, 15, 0, 0, 0, 0, time.UTC))},
					{ID: "rel-2", Status: "Official", Country: "US"},
				},
				Ratings: []catalogv1.Rating{{Source: catalogv1.RatingSourceMusicBrainz, ValueCentis: 450, Votes: 23}},
			},
			Tracks: []catalogv1.Track{
				{RecordingID: "rec-1", Medium: 1, Number: 1, Title: "Intro", DurationMs: 22000},
				{RecordingID: "rec-2", Medium: 1, Number: 2, Title: "Apocalypse Please", DurationMs: 252000},
				{RecordingID: "rec-3", Medium: 2, Number: 1, Title: "Time Is Running Out <live>", DurationMs: 237000},
			},
		},
	}
}

func ptrTime(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }

func author() *catalogv1.Author {
	return &catalogv1.Author{Status: catalogv1.AuthorStatus{Metadata: &catalogv1.AuthorMetadata{
		Name: "Ursula K. Le Guin", SortName: "Le Guin, Ursula K.", Overview: "American author.",
		Born: "1929-10-21", Died: "2018-01-22", Genres: []string{"science fiction"},
	}}}
}

func audiobook() *catalogv1.Audiobook {
	return &catalogv1.Audiobook{
		Spec: catalogv1.AudiobookSpec{ASIN: "B002V0QK4C"},
		Status: catalogv1.AudiobookStatus{Metadata: &catalogv1.AudiobookMetadata{
			Title: "The Left Hand of Darkness", Subtitle: "", Publisher: "Blackstone",
			Authors:   []catalogv1.NamedRef{{Name: "Ursula K. Le Guin", ASIN: "B000AP8NK6"}},
			Narrators: []string{"George Guidall"}, ReleaseDate: &newYear, Overview: "A classic.",
			Genres:  []string{"Science Fiction & Fantasy"},
			Ratings: []catalogv1.Rating{{Source: catalogv1.RatingSourceAudnexus, ValueCentis: 470, Votes: 1200}},
		}},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run with -update to write the golden")
	assert.Equal(t, string(want), string(got))
	// Every document is well-formed XML with Kodi's declaration.
	assert.True(t, strings.HasPrefix(string(got), nfo.Header))
	var any struct{}
	assert.NoError(t, xml.Unmarshal(got, &any))
}

func TestGoldens(t *testing.T) {
	golden(t, "artist.nfo", nfo.Artist(artist(), []catalogv1.Album{*album()}, "https://assets.fanart.tv/fanart/music/muse/artistthumb/1.jpg"))
	golden(t, "album.nfo", nfo.Album(album(), artist(), "https://coverartarchive.org/release/rel-1/front"))
	golden(t, "author.nfo", nfo.AuthorAsArtist(author(), []catalogv1.Audiobook{*audiobook()}, ""))
	golden(t, "audiobook.nfo", nfo.AudiobookAsAlbum(audiobook(), "", []nfo.Part{
		{Index: 1, Title: "Chapter 1: A Parade in Erhenrang", DurationMs: 3_600_000},
		{Index: 2, DurationMs: 61_000},
	}, "https://m.media-amazon.com/images/I/cover.jpg"))
}

// The calendar fields come from UTC: a 1 January 00:30 UTC release reads
// 2004 on any replica.
func TestDatesAreReadInUTC(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	al := album()
	al.Status.Metadata.ReleaseDate = ptrTime(newYear.Time.In(loc))
	al.Status.Metadata.Releases = nil
	doc := string(nfo.Album(al, nil, ""))
	assert.Contains(t, doc, "<year>2004</year>")
	assert.Contains(t, doc, "<releasedate>2004-01-01</releasedate>")
}

// An element whose value is empty is left out, as Kodi's exporter leaves
// it out; an artist with no metadata still names its MusicBrainz id.
func TestEmptyValuesWriteNoElement(t *testing.T) {
	a := &catalogv1.Artist{Spec: catalogv1.ArtistSpec{MusicBrainzID: "mbid"}}
	doc := string(nfo.Artist(a, nil, ""))
	assert.Equal(t, nfo.Header+"<artist>\n  <musicBrainzArtistID>mbid</musicBrainzArtistID>\n</artist>\n", doc)
	al := &catalogv1.Album{Spec: catalogv1.AlbumSpec{ReleaseGroupID: "rg"}}
	doc = string(nfo.Album(al, nil, ""))
	assert.NotContains(t, doc, "<artist>")
	assert.NotContains(t, doc, "<thumb")
	assert.NotContains(t, doc, "<rating")
	assert.NotContains(t, doc, "<track>")
}

// The fallbacks the Plex developer promised to read are written beside
// Kodi's albumArtistCredits, and a title's markup is escaped.
func TestAlbumCarriesTheArtistFallbacksAndEscapes(t *testing.T) {
	doc := string(nfo.Album(album(), artist(), ""))
	assert.Contains(t, doc, "<albumArtistCredits>\n    <artist>Muse</artist>\n    <musicBrainzArtistID>9c9f1380-2516-4fc9-a3e6-f9f61941d090</musicBrainzArtistID>\n  </albumArtistCredits>")
	assert.Contains(t, doc, "<artist>Muse</artist>\n  <albumartist>Muse</albumartist>")
	assert.Contains(t, doc, "Time Is Running Out &lt;live&gt;")
	assert.Contains(t, doc, "<releasedate>2004-03-15</releasedate>")
	assert.Contains(t, doc, "<originalreleasedate>2004-01-01</originalreleasedate>")
	assert.Contains(t, doc, `<rating max="10">4.5</rating>`)
	assert.Contains(t, doc, "<duration>04:12</duration>")
}

// The hash moves with every document byte and every image digest, and
// not otherwise.
func TestHashMovesWithDocumentsAndImages(t *testing.T) {
	a := nfo.Artist(artist(), nil, "")
	b := nfo.Album(album(), artist(), "")
	h := nfo.Hash([][]byte{a, b}, []string{"d1"})
	assert.Equal(t, h, nfo.Hash([][]byte{a, b}, []string{"d1"}))
	assert.NotEqual(t, h, nfo.Hash([][]byte{a}, []string{"d1"}))
	assert.NotEqual(t, h, nfo.Hash([][]byte{a, b}, []string{"d2"}))
	assert.NotEqual(t, h, nfo.Hash([][]byte{a, b}, nil))
	ar := artist()
	ar.Status.Metadata.Overview += "."
	assert.NotEqual(t, h, nfo.Hash([][]byte{nfo.Artist(ar, nil, ""), b}, []string{"d1"}))
	assert.Len(t, h, 64)
}
