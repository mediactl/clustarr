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

package album_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func TestPathJoinsOnlyTheAlbumSegmentOntoTheArtistsOwnPath(t *testing.T) {
	root := &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{Path: "/music", Kind: "music", Naming: catalogv1alpha1.NamingSpec{Dialect: "plex"}}}
	artist := &catalogv1alpha1.Artist{Spec: catalogv1alpha1.ArtistSpec{Folder: new("Radiohead (custom folder)")}, Status: catalogv1alpha1.ArtistStatus{Path: "/music/Radiohead (custom folder)"}}
	ctx := naming.Context{
		Kind:       commonv1.MediaKindAlbum,
		ArtistName: "Radiohead",
		AlbumTitle: "OK Computer",
		Year:       1997,
	}
	got, err := album.Path(root, artist, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/music/Radiohead (custom folder)/OK Computer (1997)", got,
		"the artist segment is the Artist's own pinned folder, never re-rendered from ctx.ArtistName")
}

// TestPathAgreesWithTheImport holds the Album controller's status.path to
// the folder the import places the album's tracks in (catalogctx.TrackFilePath
// with no status.path yet, the import's shape for a new album), over a plain
// title, every illegal character, a trailing dot and a long title
// (2026-11-10 non-video design §5.5; the F1 review's finding).
func TestPathAgreesWithTheImport(t *testing.T) {
	root := &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{Path: "/music", Kind: "music", Naming: catalogv1alpha1.NamingSpec{Dialect: "plex"}}}
	artist := &catalogv1alpha1.Artist{ObjectMeta: metav1.ObjectMeta{Name: "radiohead"}, Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mbid-artist"}, Status: catalogv1alpha1.ArtistStatus{Path: "/music/Radiohead", Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Radiohead"}}}
	for _, title := range []string{"OK Computer", "What If...?", `A:B*C"D<E>F|G`, "Trailing dot.", strings.Repeat("Long ", 78)} {
		alb := &catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Name: "ok-computer"}, Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg"},
			Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{Title: title}}}
		track := &catalogv1alpha1.Track{RecordingID: "rec-1", Medium: 1, Number: 1, Title: "Airbag"}
		c, ok := catalogctx.Album(artist, alb, track)
		require.True(t, ok, title)
		c.Year = 1997
		folder, err := album.Path(root, artist, c)
		require.NoError(t, err, title)
		file, err := catalogctx.TrackFilePath(root, artist, alb, c, ".flac")
		require.NoError(t, err, title)
		assert.Equal(t, folder, filepath.Dir(file), "%q: the controller's folder is the import's", title)
	}
}

// TestReleaseYearUsesUTCNotTheReconcilingReplicasLocalZone pins the
// timezone bug this package's ReleaseYear exists to avoid:
// metav1.Time.UnmarshalJSON calls .Local() on every value that has
// round-tripped through the apiserver, so a release date the provider sent
// as midnight UTC comes back out of a live client.Get in whatever zone the
// reconciling replica's host happens to be in. Los Angeles has no January
// DST, so 00:30 UTC on 1997-01-01 is 16:30 on 1996-12-31 in that zone --
// one calendar year earlier if .Year() is read without forcing .UTC()
// first. This constructs exactly that already-Localized value (rather than
// mutating the process' $TZ, which Go's time.Local caches at program start
// and does not observe a later os.Setenv), the same technique
// audiobook.namingContext's own test uses for the identical bug.
func TestReleaseYearUsesUTCNotTheReconcilingReplicasLocalZone(t *testing.T) {
	losAngeles := time.FixedZone("PST", -8*60*60) // UTC-8, no January DST
	releasedAtUTC := time.Date(1997, 1, 1, 0, 30, 0, 0, time.UTC)
	afterReplicaLocalized := metav1.NewTime(releasedAtUTC.In(losAngeles))

	require.Equal(t, 1996, afterReplicaLocalized.Year(),
		"sanity check: plain .Year() on the localized value really does read the wrong calendar year")
	assert.Equal(t, 1997, album.ReleaseYear(&afterReplicaLocalized),
		"ReleaseYear must call .UTC() before .Year(), or a January release lands in the wrong-year folder")
}

func TestReleaseYearNilReleaseDate(t *testing.T) {
	assert.Zero(t, album.ReleaseYear(nil))
}
