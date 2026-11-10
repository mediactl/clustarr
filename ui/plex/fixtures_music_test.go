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
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

// The music fixtures (design §7): a fictional artist with two albums, one
// of them with two tracks and a file for one of them; a fictional author
// with one audiobook of two parts; and an audiobook with no authorRef,
// whose artist is synthetic. Nothing here names a real artist, record or
// MusicBrainz id.
const (
	artistUID      = types.UID("77777777-7777-7777-7777-777777777777")
	albumUID       = types.UID("88888888-8888-8888-8888-888888888888")
	album2UID      = types.UID("89898989-8989-8989-8989-898989898989")
	authorUID      = types.UID("99999999-9999-9999-9999-999999999999")
	audiobookUID   = types.UID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	unlinkedABUID  = types.UID("abababab-abab-abab-abab-abababababab")
	artistMBID     = "0f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8"
	releaseGroupID = "10203040-5060-7080-90a0-b0c0d0e0f001"
	release2Group  = "10203040-5060-7080-90a0-b0c0d0e0f002"
	recording1     = "aa11bb22-cc33-dd44-ee55-ff6677889900"
	recording2     = "aa11bb22-cc33-dd44-ee55-ff6677889901"
	audiobookASIN  = "B0FAKE0001"
	unlinkedASIN   = "B0FAKE0002"
)

// fixtureArtist is the golden artist: a MusicBrainz id, a biography, genres,
// a country and the thumb fanart.tv stores an artist image as.
func fixtureArtist() *catalogv1.Artist {
	return &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "the-harbour-lights", Namespace: "default", UID: artistUID},
		Spec: catalogv1.ArtistSpec{
			MusicBrainzID:     artistMBID,
			QualityProfileRef: "lossless",
			RootFolderRef:     "music",
		},
		Status: catalogv1.ArtistStatus{
			Metadata: &catalogv1.ArtistMetadata{
				Name:     "The Harbour Lights",
				SortName: "Harbour Lights, The",
				Type:     "Group",
				Overview: "A coastal four-piece.",
				Genres:   []string{"Indie Rock", "Folk"},
				Country:  "GB",
				Aliases:  []string{"Harbour Lights"},
				ExternalIDs: map[string]string{
					"mb-artist": artistMBID,
				},
			},
			Artwork: []catalogv1.ArtworkEntry{
				{Type: catalogv1.ImageTypeThumb, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://assets.fanart.tv/fanart/music/x/artistthumb/1.jpg", Digest: "artistthumbdigest1", SizeBytes: 1000, UpdatedAt: metav1.Now()},
				{Type: catalogv1.ImageTypeFanart, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://assets.fanart.tv/fanart/music/x/artistbackground/1.jpg", Digest: "artistfanartdigest1", SizeBytes: 2000, UpdatedAt: metav1.Now()},
				{Type: catalogv1.ImageTypeLogo, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://assets.fanart.tv/fanart/music/x/hdmusiclogo/1.png", Digest: "artistlogodigest1", SizeBytes: 500, UpdatedAt: metav1.Now()},
			},
		},
	}
}

// fixtureAlbums are the artist's two albums: the golden one (2016, two
// tracks on one disc, the first with a file) and an earlier one (2012, one
// track, no files), so a listing's order and an artist's lazily paged
// tracks are both exercised.
func fixtureAlbums() []*catalogv1.Album {
	return []*catalogv1.Album{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "the-harbour-lights-low-water", Namespace: "default", UID: albumUID},
			Spec: catalogv1.AlbumSpec{
				ArtistRef:      "the-harbour-lights",
				ReleaseGroupID: releaseGroupID,
			},
			Status: catalogv1.AlbumStatus{
				Metadata: &catalogv1.AlbumMetadata{
					Title:       "Low Water",
					Overview:    "The second record.",
					AlbumType:   "Album",
					ReleaseDate: &metav1.Time{Time: mustParseDate("2016-05-20")},
					Genres:      []string{"Indie Rock"},
					Label:       "Quayside Records",
					ExternalIDs: map[string]string{"mb-release-group": releaseGroupID},
					Ratings: []catalogv1.Rating{
						{Source: catalogv1.RatingSourceMusicBrainz, ValueCentis: 840, Votes: 12},
					},
				},
				Tracks: []catalogv1.Track{
					{RecordingID: recording2, Medium: 1, Number: 2, AbsoluteNumber: 2, Title: "Undertow", DurationMs: 215000},
					{RecordingID: recording1, Medium: 1, Number: 1, AbsoluteNumber: 1, Title: "The Tide Comes In", DurationMs: 184000, FileRef: ptr("low-water-01")},
				},
				Artwork: []catalogv1.ArtworkEntry{
					{Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://coverartarchive.org/release/x/1-500.jpg", Digest: "albumcoverdigest1", SizeBytes: 3000, UpdatedAt: metav1.Now()},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "the-harbour-lights-first-light", Namespace: "default", UID: album2UID},
			Spec: catalogv1.AlbumSpec{
				ArtistRef:      "the-harbour-lights",
				ReleaseGroupID: release2Group,
			},
			Status: catalogv1.AlbumStatus{
				Metadata: &catalogv1.AlbumMetadata{
					Title:       "First Light",
					ReleaseDate: &metav1.Time{Time: mustParseDate("2012-09-01")},
				},
				Tracks: []catalogv1.Track{
					{RecordingID: "aa11bb22-cc33-dd44-ee55-ff6677889902", Medium: 1, Number: 1, Title: "Dawn"},
				},
			},
		},
	}
}

// fixtureAlbumFile is the file of the golden album's first track, named
// as a music RootFolder holds it.
func fixtureAlbumFile() *catalogv1.MediaFile {
	return &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "low-water-01", Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "the-harbour-lights-low-water", Track: recording1},
			Path:     "/data/media/music/The Harbour Lights/Low Water (2016)/01 - The Tide Comes In.flac",
		},
	}
}

// fixtureAuthor is the golden author.
func fixtureAuthor() *catalogv1.Author {
	return &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "m-r-quayside", Namespace: "default", UID: authorUID},
		Spec: catalogv1.AuthorSpec{
			OpenLibraryID:     "OL9999999A",
			QualityProfileRef: "ebook",
			RootFolderRef:     "books",
		},
		Status: catalogv1.AuthorStatus{
			Metadata: &catalogv1.AuthorMetadata{
				Name:     "M. R. Quayside",
				SortName: "Quayside, M. R.",
				Overview: "Writes about the sea.",
				Genres:   []string{"Mystery"},
			},
			Artwork: []catalogv1.ArtworkEntry{
				{Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://covers.openlibrary.org/a/olid/OL9999999A-L.jpg", Digest: "authorphotodigest1", SizeBytes: 800, UpdatedAt: metav1.Now()},
			},
		},
	}
}

// fixtureAudiobook is the golden audiobook: an ASIN, an authorRef to the
// fixture author, a narrator, two chapters aligned with its two parts.
func fixtureAudiobook() *catalogv1.Audiobook {
	return &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "the-long-dock", Namespace: "default", UID: audiobookUID},
		Spec: catalogv1.AudiobookSpec{
			ASIN:              audiobookASIN,
			AuthorRef:         ptr("m-r-quayside"),
			QualityProfileRef: "audiobook",
			RootFolderRef:     "audiobooks",
		},
		Status: catalogv1.AudiobookStatus{
			Metadata: &catalogv1.AudiobookMetadata{
				Title:          "The Long Dock",
				Authors:        []catalogv1.NamedRef{{Name: "M. R. Quayside"}},
				Narrators:      []string{"Ada Marsh"},
				Publisher:      "Quayside Audio",
				ReleaseDate:    &metav1.Time{Time: mustParseDate("2020-11-03")},
				RuntimeMinutes: 420,
				ISBN:           "9780000000001",
				Overview:       "A dock, a tide, a secret.",
				Genres:         []string{"Mystery", "Fiction"},
				Chapters: []catalogv1.Chapter{
					{Title: "Opening", StartMs: 0},
					{Title: "Closing", StartMs: 12600000},
				},
				ExternalIDs: map[string]string{"asin": audiobookASIN},
				Ratings: []catalogv1.Rating{
					{Source: catalogv1.RatingSourceAudnexus, ValueCentis: 920, Votes: 300},
				},
			},
			HasFile:   true,
			FileRefs:  []string{"the-long-dock-part-01", "the-long-dock-part-02"},
			PartCount: 2,
			Artwork: []catalogv1.ArtworkEntry{
				{Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://m.media-amazon.com/images/I/fake.jpg", Digest: "audiobookcoverdigest1", SizeBytes: 4000, UpdatedAt: metav1.Now()},
			},
		},
	}
}

// fixtureAudiobookFiles are the golden audiobook's two parts, the first
// probed.
func fixtureAudiobookFiles() []*catalogv1.MediaFile {
	return []*catalogv1.MediaFile{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "the-long-dock-part-01", Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "the-long-dock"},
				Path:      "/data/media/audiobooks/M. R. Quayside/The Long Dock (2020)/The Long Dock - Part 01.m4b",
				PartIndex: 1,
			},
			Status: catalogv1.MediaFileStatus{
				MediaInfo: &commonv1.MediaInfo{RuntimeMillis: 12600000},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "the-long-dock-part-02", Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "the-long-dock"},
				Path:      "/data/media/audiobooks/M. R. Quayside/The Long Dock (2020)/The Long Dock - Part 02.m4b",
				PartIndex: 2,
			},
		},
	}
}

// fixtureUnlinkedAudiobook has no authorRef: its artist is the synthetic
// one keyed by its first credited author's name (design §7.2).
func fixtureUnlinkedAudiobook() *catalogv1.Audiobook {
	return &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "salt-and-bone", Namespace: "default", UID: unlinkedABUID},
		Spec: catalogv1.AudiobookSpec{
			ASIN:              unlinkedASIN,
			QualityProfileRef: "audiobook",
			RootFolderRef:     "audiobooks",
		},
		Status: catalogv1.AudiobookStatus{
			Metadata: &catalogv1.AudiobookMetadata{
				Title:       "Salt and Bone",
				Authors:     []catalogv1.NamedRef{{Name: "Ines Varga"}},
				ReleaseDate: &metav1.Time{Time: mustParseDate("2019-02-14")},
			},
			FileRefs: []string{"salt-and-bone-01"},
		},
	}
}

func ptr[T any](v T) *T { return &v }

// musicObjects is every music fixture: the artist, its albums and file,
// the author, its audiobook and files, and the unlinked audiobook.
func musicObjects() []client.Object {
	objs := []client.Object{fixtureArtist(), fixtureAlbumFile(), fixtureAuthor(), fixtureAudiobook(), fixtureUnlinkedAudiobook()}
	for _, al := range fixtureAlbums() {
		objs = append(objs, al)
	}
	for _, f := range fixtureAudiobookFiles() {
		objs = append(objs, f)
	}
	return objs
}

// newMusicHandler is [newTestHandler] with the music roots on: the handler
// built with MusicProvider and the index with IndexOptions.Music, as
// ui/routes.go wires them under --plex-music-provider.
func newMusicHandler(t *testing.T, externalURL string, objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL:   externalURL,
		MusicProvider: true,
		Index: func(ctx context.Context) (*projection.Index, error) {
			return projection.BuildIndexWith(ctx, c, projection.IndexOptions{Music: true})
		},
	})
}

// requireMusicRootsOn asserts both music roots answer their MediaProvider.
func requireMusicRootsOn(t *testing.T, h http.Handler) {
	t.Helper()
	for _, path := range []string{"/plex/music", "/plex/audiobooks"} {
		require.Equal(t, http.StatusOK, getJSON(t, h, path).Code, "GET %s", path)
	}
}
