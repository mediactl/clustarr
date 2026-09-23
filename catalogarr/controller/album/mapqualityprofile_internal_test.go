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

// TestMapQualityProfileReachesOnlyAlbumsWithTheirOwnOverride pins
// mapQualityProfile's documented scope: it wakes an Album that pins the
// edited QualityProfile directly via its own spec.qualityProfileRef, and
// leaves an Album with no override (or an override pointing elsewhere)
// alone -- the indirect, inherited-from-Artist case is deliberately not
// covered (see SetupWithManager's own doc comment for why).
func TestMapQualityProfileReachesOnlyAlbumsWithTheirOwnOverride(t *testing.T) {
	c := newIndexedFakeClient(t)

	profileA := "profile-a"
	profileB := "profile-b"
	overriding := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "overriding", Namespace: "media"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-1", QualityProfileRef: &profileA},
	}
	notOverriding := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "not-overriding", Namespace: "media"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-2"},
	}
	elsewhere := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "media"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-3", QualityProfileRef: &profileB},
	}
	require.NoError(t, c.Create(t.Context(), overriding))
	require.NoError(t, c.Create(t.Context(), notOverriding))
	require.NoError(t, c.Create(t.Context(), elsewhere))

	r := &Reconciler{Client: c}
	reqs := r.mapQualityProfile(t.Context(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile-a"}})
	require.Len(t, reqs, 1)
	assert.Equal(t, "overriding", reqs[0].Name)
}

func TestMapQualityProfileNoMatchesReturnsEmpty(t *testing.T) {
	r := &Reconciler{Client: newIndexedFakeClient(t)}
	reqs := r.mapQualityProfile(t.Context(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "unused-profile"}})
	assert.Empty(t, reqs)
}
