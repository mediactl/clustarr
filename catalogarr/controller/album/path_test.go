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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestPathJoinsOnlyTheAlbumSegmentOntoTheArtistsOwnPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectPlex})
	ctx := naming.Context{
		Kind:       commonv1.MediaKindAlbum,
		ArtistName: "Radiohead",
		AlbumTitle: "OK Computer",
		Year:       1997,
	}
	got, err := album.Path("/music/Radiohead (custom folder)", eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/music/Radiohead (custom folder)/OK Computer (1997)", got,
		"the artist segment must come from artistPath, never re-rendered from ctx.ArtistName")
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

	require.Equal(t, 1996, afterReplicaLocalized.Time.Year(),
		"sanity check: plain .Year() on the localized value really does read the wrong calendar year")
	assert.Equal(t, 1997, album.ReleaseYear(&afterReplicaLocalized),
		"ReleaseYear must call .UTC() before .Year(), or a January release lands in the wrong-year folder")
}

func TestReleaseYearNilReleaseDate(t *testing.T) {
	assert.Zero(t, album.ReleaseYear(nil))
}
