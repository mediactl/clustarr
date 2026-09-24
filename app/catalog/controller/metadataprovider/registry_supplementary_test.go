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

package metadataprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

var creds = map[string][]byte{
	catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("k"),
	catalogv1alpha1.MetadataSecretKeyBearer: []byte("t"),
}

// TestEveryProviderTypeBuildsAClient is the X6b guard: each of the eight
// types that used to be ErrProviderNotImplemented, and mdblist since its
// shapes were recorded, builds a client and fills exactly the Registry
// slots its client serves.
func TestEveryProviderTypeBuildsAClient(t *testing.T) {
	type slots struct{ artwork, books, comics, resolvers, ratings int }
	tests := []struct {
		typ  catalogv1alpha1.MetadataProviderType
		want slots
	}{
		{catalogv1alpha1.MetadataProviderCoverArt, slots{artwork: 1}},
		{catalogv1alpha1.MetadataProviderFanart, slots{artwork: 1}},
		{catalogv1alpha1.MetadataProviderHardcover, slots{books: 1}},
		{catalogv1alpha1.MetadataProviderMetron, slots{comics: 1, resolvers: 1}},
		{catalogv1alpha1.MetadataProviderMangaDex, slots{comics: 1, resolvers: 1}},
		{catalogv1alpha1.MetadataProviderAniList, slots{resolvers: 1}},
		{catalogv1alpha1.MetadataProviderKitsu, slots{resolvers: 1}},
		{catalogv1alpha1.MetadataProviderAnimeLists, slots{resolvers: 1}},
		{catalogv1alpha1.MetadataProviderMDBList, slots{ratings: 1}},
	}
	for _, tt := range tests {
		t.Run(string(tt.typ), func(t *testing.T) {
			reg := &metadata.Registry{}
			require.NoError(t, addToRegistry(reg, catalogv1alpha1.MetadataProviderSpec{Type: tt.typ}, creds, http.DefaultClient))
			require.Equal(t, tt.want, slots{len(reg.Artwork), len(reg.Books), len(reg.Comics), len(reg.Resolvers), len(reg.Ratings)})
			require.Empty(t, reg.Movies)
			require.Empty(t, reg.Series)
			require.Empty(t, reg.Artists)
			require.Empty(t, reg.Audiobooks)

			p, err := newSupplementaryProber(catalogv1alpha1.MetadataProviderSpec{Type: tt.typ}, creds, http.DefaultClient)
			require.NoError(t, err)
			require.NotNil(t, p)
		})
	}
}

func TestSupplementaryProvidersRefuseAMissingCredential(t *testing.T) {
	for typ, key := range map[catalogv1alpha1.MetadataProviderType]string{
		catalogv1alpha1.MetadataProviderFanart:    catalogv1alpha1.MetadataSecretKeyAPIKey,
		catalogv1alpha1.MetadataProviderMDBList:   catalogv1alpha1.MetadataSecretKeyAPIKey,
		catalogv1alpha1.MetadataProviderHardcover: catalogv1alpha1.MetadataSecretKeyBearer,
		catalogv1alpha1.MetadataProviderMetron:    catalogv1alpha1.MetadataSecretKeyBearer,
	} {
		t.Run(string(typ), func(t *testing.T) {
			wrong := map[string][]byte{}
			for k, v := range creds {
				if k != key {
					wrong[k] = v
				}
			}
			err := addToRegistry(&metadata.Registry{}, catalogv1alpha1.MetadataProviderSpec{Type: typ}, wrong, http.DefaultClient)
			require.ErrorContains(t, err, key)
			require.NotErrorIs(t, err, ErrProviderNotImplemented, "a missing credential is a spec error, not an unknown type")
		})
	}
}

func TestAnUnknownTypeIsStillNotImplemented(t *testing.T) {
	err := addToRegistry(&metadata.Registry{}, catalogv1alpha1.MetadataProviderSpec{Type: "gcd"}, creds, http.DefaultClient)
	require.ErrorIs(t, err, ErrProviderNotImplemented)
	_, err = newSupplementaryProber(catalogv1alpha1.MetadataProviderSpec{Type: "gcd"}, creds, http.DefaultClient)
	require.ErrorIs(t, err, ErrProviderNotImplemented)
}

// TestOMDbIsRecognisedButBlockedUnderR5 proves the omdb
// MetadataProviderType enum member is accepted (not
// ErrProviderNotImplemented -- this package knows the type) but refuses
// client construction with ErrProviderAwaitingFixtures, per ruling R5
// (spec §C.3): no client is written without a recorded response shape,
// and OMDB_API_KEY was unset at task C1's dispatch (mdblist's shapes were
// recorded on 2026-09-24; see TestEveryProviderTypeBuildsAClient). Both
// addToRegistry and newSupplementaryProber (the two construction paths,
// registry-build and CR-probe) must agree.
func TestOMDbIsRecognisedButBlockedUnderR5(t *testing.T) {
	for _, typ := range []catalogv1alpha1.MetadataProviderType{
		catalogv1alpha1.MetadataProviderOMDb,
	} {
		t.Run(string(typ), func(t *testing.T) {
			err := addToRegistry(&metadata.Registry{}, catalogv1alpha1.MetadataProviderSpec{Type: typ}, creds, http.DefaultClient)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrProviderAwaitingFixtures)
			require.NotErrorIs(t, err, ErrProviderNotImplemented, "the type is known, not unimplemented -- construction is refused, not missing")
			require.ErrorContains(t, err, "not implemented: awaiting recorded fixtures (C1 follow-up)")

			_, err = newSupplementaryProber(catalogv1alpha1.MetadataProviderSpec{Type: typ}, creds, http.DefaultClient)
			require.ErrorIs(t, err, ErrProviderAwaitingFixtures)
		})
	}
}

func TestSupplementaryProberReportsReachabilityAndRejectedCredentials(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	p, err := newSupplementaryProber(catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderCoverArt, BaseURL: &notFound.URL}, nil, notFound.Client())
	require.NoError(t, err)
	_, err = p.Probe(context.Background())
	require.NoError(t, err, "the Cover Art Archive answering 404 is the Cover Art Archive answering")

	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid API key"}`))
	}))
	defer rejected.Close()
	p, err = newSupplementaryProber(catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderFanart, BaseURL: &rejected.URL}, creds, rejected.Client())
	require.NoError(t, err)
	_, err = p.Probe(context.Background())
	require.True(t, isAuthError(err), "a rejected key must read as CredentialsRejected, got %v", err)

	// mdblist probes GET /user with every key, the call that reports the
	// quota without spending it; a rejected second key fails the probe too.
	var probed []string
	mdbl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed = append(probed, r.URL.Path+"?"+r.URL.Query().Get("apikey"))
		if r.URL.Query().Get("apikey") == "bad" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"api_requests":1000,"api_requests_count":4}`))
	}))
	defer mdbl.Close()
	two := map[string][]byte{catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("k1"), catalogv1alpha1.MetadataSecretKeyAPIKeySecondary: []byte("k2")}
	p, err = newSupplementaryProber(catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderMDBList, BaseURL: &mdbl.URL}, two, mdbl.Client())
	require.NoError(t, err)
	_, err = p.Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"/user?k1", "/user?k2"}, probed)

	two[catalogv1alpha1.MetadataSecretKeyAPIKeySecondary] = []byte("bad")
	p, err = newSupplementaryProber(catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderMDBList, BaseURL: &mdbl.URL}, two, mdbl.Client())
	require.NoError(t, err)
	_, err = p.Probe(context.Background())
	require.True(t, isAuthError(err), "a rejected second key must read as CredentialsRejected, got %v", err)
}

func TestSupplementaryProvidersLoseAPriorityTieToPrimaryOnes(t *testing.T) {
	ns := "clustarr"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: ns}, Data: creds}
	mp := func(name string, typ catalogv1alpha1.MetadataProviderType, priority int32) *catalogv1alpha1.MetadataProvider {
		return &catalogv1alpha1.MetadataProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: typ, Priority: priority, ContactUserAgent: "clustarr-test (test@example.com)",
				SecretRef: &corev1.LocalObjectReference{Name: "creds"},
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		secret,
		mp("a-hardcover", catalogv1alpha1.MetadataProviderHardcover, 50),
		mp("b-openlibrary", catalogv1alpha1.MetadataProviderOpenLibrary, 50),
		mp("a-metron", catalogv1alpha1.MetadataProviderMetron, 50),
		mp("b-comicvine", catalogv1alpha1.MetadataProviderComicVine, 50),
		mp("c-mangadex", catalogv1alpha1.MetadataProviderMangaDex, 40),
	).Build()

	reg, err := BuildRegistry(context.Background(), c, ns, http.DefaultClient)
	require.NoError(t, err)

	names := func(n int, name func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = name(i)
		}
		return out
	}
	require.Equal(t, []string{"openlibrary", "hardcover"}, names(len(reg.Books), func(i int) string { return reg.Books[i].Name() }))
	require.Equal(t, []string{"mangadex", "comicvine", "metron"}, names(len(reg.Comics), func(i int) string { return reg.Comics[i].Name() }),
		"priority first; at a tie the provider a Comic can be keyed by answers before Metron")
}
