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

package metadata

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func enabled() *bool { b := true; return &b }

func TestBuildRegistryWiresOnlyTheImplementedTypesInPriorityOrder(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb-key", Namespace: "clustarr"},
		Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("test-key")},
	}
	providers := []catalogv1alpha1.MetadataProvider{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tmdb-backup", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(), Priority: 90,
				SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tmdb-primary", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(), Priority: 10,
				SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "off", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderTVDB, Enabled: func() *bool { b := false; return &b }(),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "fanart", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderFanart, Enabled: enabled(),
				SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()
	reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.NoError(t, err)

	require.Len(t, reg.Movies, 2, "both tmdb providers wire a MovieProvider; the disabled tvdb and the artwork-only fanart do not")
	require.Equal(t, "tmdb", reg.Movies[0].Name())
	require.Empty(t, reg.Series, "tvdb was disabled; tmdb implements MovieProvider only (see Judgment call 3)")
	require.Len(t, reg.Artwork, 1, "fanart is an ArtworkProvider")
}

// TestBuildRegistryWiresEveryProviderType is the X6b guard: every
// MetadataProviderType value lands in the Registry slots its client fills,
// so none is silently skipped the way the eight client-less types were.
func TestBuildRegistryWiresEveryProviderType(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "clustarr"},
		Data: map[string][]byte{
			catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("k"),
			catalogv1alpha1.MetadataSecretKeyBearer: []byte("t"),
		},
	}
	types := []catalogv1alpha1.MetadataProviderType{
		catalogv1alpha1.MetadataProviderTMDB, catalogv1alpha1.MetadataProviderTVDB,
		catalogv1alpha1.MetadataProviderMusicBrainz, catalogv1alpha1.MetadataProviderCoverArt,
		catalogv1alpha1.MetadataProviderFanart, catalogv1alpha1.MetadataProviderOpenLibrary,
		catalogv1alpha1.MetadataProviderHardcover, catalogv1alpha1.MetadataProviderAudnexus,
		catalogv1alpha1.MetadataProviderComicVine, catalogv1alpha1.MetadataProviderMetron,
		catalogv1alpha1.MetadataProviderMangaDex, catalogv1alpha1.MetadataProviderAniList,
		catalogv1alpha1.MetadataProviderKitsu, catalogv1alpha1.MetadataProviderAnimeLists,
	}
	providers := make([]catalogv1alpha1.MetadataProvider, 0, len(types))
	for _, typ := range types {
		providers = append(providers, catalogv1alpha1.MetadataProvider{
			ObjectMeta: metav1.ObjectMeta{Name: string(typ), Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: typ, Enabled: enabled(), Priority: 50, ContactUserAgent: "clustarr-test (test@example.com)",
				SecretRef: &corev1.LocalObjectReference{Name: "creds"},
			},
		})
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()

	reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.NoError(t, err)

	var got struct{ movies, series, artists, books, audiobooks, comics, artwork, resolvers []string }
	for _, p := range reg.Movies {
		got.movies = append(got.movies, p.Name())
	}
	for _, p := range reg.Series {
		got.series = append(got.series, p.Name())
	}
	for _, p := range reg.Artists {
		got.artists = append(got.artists, p.Name())
	}
	for _, p := range reg.Books {
		got.books = append(got.books, p.Name())
	}
	for _, p := range reg.Audiobooks {
		got.audiobooks = append(got.audiobooks, p.Name())
	}
	for _, p := range reg.Comics {
		got.comics = append(got.comics, p.Name())
	}
	for _, p := range reg.Artwork {
		got.artwork = append(got.artwork, p.Name())
	}
	for _, p := range reg.Resolvers {
		got.resolvers = append(got.resolvers, p.Name())
	}
	require.Equal(t, []string{"tmdb"}, got.movies)
	require.Equal(t, []string{"tvdb"}, got.series)
	require.Equal(t, []string{"musicbrainz"}, got.artists)
	require.Equal(t, []string{"openlibrary", "hardcover"}, got.books, "at equal priority the provider a Book is keyed by answers first")
	require.Equal(t, []string{"audnexus"}, got.audiobooks)
	require.Equal(t, []string{"comicvine", "mangadex", "metron"}, got.comics, "AniList is never a comic source")
	require.Equal(t, []string{"coverart", "fanart"}, got.artwork)
	require.ElementsMatch(t, []string{"metron", "mangadex", "anilist", "kitsu", "animelists"}, got.resolvers)
}

// TestBuildRegistryWiresTMDBAsARatingsProviderToo proves the same *tmdb.Client
// lands in both reg.Movies and reg.Ratings (spec §C.2's table: tmdb
// declares its own source "from the fetch it already performs").
func TestBuildRegistryWiresTMDBAsARatingsProviderToo(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb-key", Namespace: "clustarr"},
		Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("test-key")},
	}
	providers := []catalogv1alpha1.MetadataProvider{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(),
				SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()
	reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.NoError(t, err)

	require.Len(t, reg.Ratings, 1)
	require.Equal(t, "tmdb", reg.Ratings[0].Name())
	require.Equal(t, reg.Movies[0], reg.Ratings[0], "the same client instance fills both slots")
}

func TestBuildRegistryAnExplicitPriorityBeatsTheTieBreak(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "clustarr"},
		Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyBearer: []byte("t")},
	}
	providers := []catalogv1alpha1.MetadataProvider{
		{ObjectMeta: metav1.ObjectMeta{Name: "openlibrary", Namespace: "clustarr"}, Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderOpenLibrary, Enabled: enabled(), Priority: 50, ContactUserAgent: "x (y@z)",
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "hardcover", Namespace: "clustarr"}, Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderHardcover, Enabled: enabled(), Priority: 10,
			SecretRef: &corev1.LocalObjectReference{Name: "creds"},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()

	reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)

	require.NoError(t, err)
	require.Equal(t, "hardcover", reg.Books[0].Name(), "the operator ranked it first")
}

func TestBuildRegistryErrorsOnASupplementaryProviderWithoutItsCredential(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "clustarr"},
		Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("k")},
	}
	providers := []catalogv1alpha1.MetadataProvider{{
		ObjectMeta: metav1.ObjectMeta{Name: "metron", Namespace: "clustarr"},
		Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderMetron, Enabled: enabled(),
			SecretRef: &corev1.LocalObjectReference{Name: "creds"},
		},
	}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()

	_, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)

	require.ErrorContains(t, err, `"bearer"`, "metron authenticates with a Bearer token, not an api key")
}

func TestBuildRegistryErrorsOnAMissingSecret(t *testing.T) {
	providers := []catalogv1alpha1.MetadataProvider{{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: "clustarr"},
		Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(),
			SecretRef: &corev1.LocalObjectReference{Name: "does-not-exist"},
		},
	}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	_, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.Error(t, err)
}
