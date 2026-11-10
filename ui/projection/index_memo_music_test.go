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

package projection_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestNewIndexMemoWithMusicListsTheArtists: the memo ui.NewServer builds
// under PlexOptions.MusicProvider carries the music lookups (2026-11-10
// design §7.5), and the plain memo does not, so a music root over the
// plain memo would answer "not found" for every artist.
func TestNewIndexMemoWithMusicListsTheArtists(t *testing.T) {
	const mbid = "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
		&catalogv1.Artist{ObjectMeta: metav1.ObjectMeta{Name: "rh", Namespace: "media", UID: "rh-uid"}, Spec: catalogv1.ArtistSpec{MusicBrainzID: mbid}},
	).Build()

	withMusic, err := projection.NewIndexMemoWith(c, projection.IndexOptions{Music: true}).Get(t.Context())
	require.NoError(t, err)
	got, ok := withMusic.ByMBID(commonv1.MediaKindArtist, mbid)
	require.True(t, ok)
	require.Equal(t, "rh", got.GetName())

	plain, err := projection.NewIndexMemo(c).Get(t.Context())
	require.NoError(t, err)
	_, ok = plain.ByMBID(commonv1.MediaKindArtist, mbid)
	require.False(t, ok, "the plain memo lists no music")
}
