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

package scanapply

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A scan that attributes a recorded album file to another track of the same
// Album corrects the record's track (2026-11-16: 3,664 of talos's album
// files were attributed by number against the wrong edition, and a scan's
// corrected attribution of an unchanged file was dropped, the record's own
// track written back); the Album it backs never changes here.
func TestAScanCorrectsARecordedAlbumFilesTrack(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)
	music := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: "music"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/music", Kind: catalogv1alpha1.RootFolderKindMusic, Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "music-standard"}},
	}
	old := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: "old"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "danny-brown", ReleaseGroupID: "old-group"},
	}
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: "danny-brown"},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "db-mbid", QualityProfileRef: "music-standard", RootFolderRef: "music"},
	}
	path := "/data/media/music/Danny Brown/18_danny_brown-kush_coma-fih.mp3"
	recorded := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: k8s.ChildName("old", "mediafile", path)},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "old", Track: "kush-coma-instrumental"},
			Path:     path, SizeBytes: 1, ModTime: metav1.NewTime(now),
		},
	}
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(music, old, artist, recorded).Build()
	a := &Applier{Client: c, Reader: c, APIReader: c, Bus: bus, Now: func() time.Time { return now }}
	observe := func(track string) {
		t.Helper()
		require.NoError(t, a.Apply(ctx, schema.ScanObservation{
			Scan:       schema.Ref{Namespace: "music", Name: "scan", UID: "scan-uid"},
			RootFolder: schema.Ref{Namespace: "music", Name: "music"},
			Kind:       schema.ScanObservationPresent, Path: path, SizeBytes: 1, ModTime: now,
			Attribution: &schema.ScanAttribution{Kind: string(commonv1.MediaKindAlbum), Name: "old", Track: track, Evidence: "tags"},
			Frozen:      &schema.FrozenFields{}, SeenAt: now,
		}))
	}

	observe("kush-coma")
	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "music", Name: recorded.Name}, &got))
	assert.Equal(t, "kush-coma", got.Spec.MediaRef.Track, "the scan's track replaces the recorded one")
	assert.Equal(t, "old", got.Spec.MediaRef.Name)
	assert.Equal(t, path, got.Spec.Path)

	observe("")
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "music", Name: recorded.Name}, &got))
	assert.Equal(t, "kush-coma", got.Spec.MediaRef.Track, "an attribution naming no track leaves the record's")
}
