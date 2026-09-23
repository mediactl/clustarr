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

// This file is an internal (package album, not album_test) test, like
// audiobook/bookref_test.go, so it can exercise mapQualityProfile directly
// -- it is unexported.
package album

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newIndexedFakeClient(t *testing.T) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.Album{}, albumByQualityProfileIndexKey, func(o client.Object) []string {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok || alb.Spec.QualityProfileRef == nil || *alb.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{*alb.Spec.QualityProfileRef}
		}).
		Build()
}

func TestMapQualityProfileWrongTypeReturnsNil(t *testing.T) {
	r := &Reconciler{Client: newIndexedFakeClient(t)}
	other := &subtitlev1alpha1.SubtitleRequest{}
	assert.Nil(t, r.mapQualityProfile(t.Context(), other), "wrong type returns nil, never panics")
}

// TestMapQualityProfileReachesOverridesAndInheritors pins mapQualityProfile's
// scope: an edited profile wakes the Albums that name it through their own
// override and the Albums with no override whose Artist names it, and no
// Album ranked against anything else.
func TestMapQualityProfileReachesOverridesAndInheritors(t *testing.T) {
	c := newIndexedFakeClient(t)

	profileA := "profile-a"
	profileB := "profile-b"
	objs := []client.Object{
		&catalogv1alpha1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "media"},
			Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb-1", QualityProfileRef: profileA, RootFolderRef: "music"},
		},
		&catalogv1alpha1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: "blur", Namespace: "media"},
			Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb-2", QualityProfileRef: profileB, RootFolderRef: "music"},
		},
		// Same Artist name in another namespace, naming another profile.
		&catalogv1alpha1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "other"},
			Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb-1", QualityProfileRef: profileB, RootFolderRef: "music"},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "overriding", Namespace: "media"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "blur", ReleaseGroupID: "rg-1", QualityProfileRef: &profileA},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "inheriting", Namespace: "media"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-2"},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "overridden-elsewhere", Namespace: "media"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-3", QualityProfileRef: &profileB},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "other-artist", Namespace: "media"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "blur", ReleaseGroupID: "rg-4"},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "inheriting", Namespace: "other"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-2"},
		},
	}
	for _, o := range objs {
		require.NoError(t, c.Create(t.Context(), o))
	}

	r := &Reconciler{Client: c}
	reqs := r.mapQualityProfile(t.Context(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile-a"}})
	got := map[string]bool{}
	for _, req := range reqs {
		got[req.Namespace+"/"+req.Name] = true
	}
	assert.Equal(t, map[string]bool{"media/overriding": true, "media/inheriting": true}, got)
}

func TestMapArtistWakesItsAlbums(t *testing.T) {
	c := newIndexedFakeClient(t)
	for _, o := range []client.Object{
		&catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "media"}, Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-1"}},
		&catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Name: "theirs", Namespace: "media"}, Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "blur", ReleaseGroupID: "rg-2"}},
		&catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "other"}, Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-3"}},
	} {
		require.NoError(t, c.Create(t.Context(), o))
	}
	r := &Reconciler{Client: c}
	reqs := r.mapArtist(t.Context(), &catalogv1alpha1.Artist{ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "media"}})
	require.Len(t, reqs, 1)
	assert.Equal(t, "mine", reqs[0].Name)
}

func TestMapQualityProfileNoMatchesReturnsEmpty(t *testing.T) {
	r := &Reconciler{Client: newIndexedFakeClient(t)}
	reqs := r.mapQualityProfile(t.Context(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "unused-profile"}})
	assert.Empty(t, reqs)
}
