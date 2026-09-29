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

package metadataprovider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/metadataprovider"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func providersIn(t *testing.T, ctx context.Context, c client.Client, ns string) map[catalogv1alpha1.MetadataProviderType]catalogv1alpha1.MetadataProvider {
	t.Helper()
	var l catalogv1alpha1.MetadataProviderList
	require.NoError(t, c.List(ctx, &l, client.InNamespace(ns)))
	out := map[catalogv1alpha1.MetadataProviderType]catalogv1alpha1.MetadataProvider{}
	for _, p := range l.Items {
		out[p.Spec.Type] = p
	}
	return out
}

// Every provider that needs no credentials exists after a start, each with
// the contact User-Agent MusicBrainz and Open Library require.
func TestSeedDefaultsCreatesEveryKeylessProvider(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	require.NoError(t, metadataprovider.SeedDefaults(ctx, c, "media"))

	got := providersIn(t, ctx, c, "media")
	require.Len(t, got, 8)
	for _, typ := range []catalogv1alpha1.MetadataProviderType{
		catalogv1alpha1.MetadataProviderMusicBrainz, catalogv1alpha1.MetadataProviderOpenLibrary,
		catalogv1alpha1.MetadataProviderCoverArt, catalogv1alpha1.MetadataProviderAudnexus,
		catalogv1alpha1.MetadataProviderMangaDex, catalogv1alpha1.MetadataProviderAniList,
		catalogv1alpha1.MetadataProviderKitsu, catalogv1alpha1.MetadataProviderAnimeLists,
	} {
		p, ok := got[typ]
		require.Truef(t, ok, "%s seeded", typ)
		require.Equal(t, string(typ), p.Name)
		require.Nil(t, p.Spec.SecretRef, "keyless")
		require.NotEmpty(t, p.Spec.ContactUserAgent, typ)
	}
}

// A type the owner already configured, under any name, is left as it is,
// and a seeded provider the owner edited is never reset.
func TestSeedDefaultsLeavesTheOwnersProvidersAlone(t *testing.T) {
	ctx := context.Background()
	mine := &catalogv1alpha1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "mb-mirror"},
		Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderMusicBrainz, ContactUserAgent: "me",
			BaseURL: ptr.To("http://mb.local/ws/2"),
		},
	}
	off := &catalogv1alpha1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "kitsu"},
		Spec:       catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderKitsu, Enabled: ptr.To(false)},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(mine, off).Build()
	require.NoError(t, metadataprovider.SeedDefaults(ctx, c, "media"))
	require.NoError(t, metadataprovider.SeedDefaults(ctx, c, "media"), "a second start is a no-op")

	got := providersIn(t, ctx, c, "media")
	require.Len(t, got, 8)
	require.Equal(t, "mb-mirror", got[catalogv1alpha1.MetadataProviderMusicBrainz].Name, "no second musicbrainz")
	require.Equal(t, "me", got[catalogv1alpha1.MetadataProviderMusicBrainz].Spec.ContactUserAgent)
	require.False(t, *got[catalogv1alpha1.MetadataProviderKitsu].Spec.Enabled, "the owner's switch stays off")
}

// With no namespace to seed into (`clustarr all` from a dev shell), nothing
// is created and the start is not refused.
func TestSeedDefaultsWithoutANamespaceCreatesNothing(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	require.NoError(t, metadataprovider.SeedDefaults(ctx, c, ""))
	var l catalogv1alpha1.MetadataProviderList
	require.NoError(t, c.List(ctx, &l))
	require.Empty(t, l.Items)
}
