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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

var creds = map[string][]byte{
	catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("k"),
	catalogv1alpha1.MetadataSecretKeyBearer: []byte("t"),
}

// TestEveryProviderTypeBuildsAClient is the X6b guard: each of the eight
// types that used to be ErrProviderNotImplemented, and mdblist and
// theintrodb, builds a client that fills exactly the slots its client serves.
func TestEveryProviderTypeBuildsAClient(t *testing.T) {
	type slots struct{ artwork, books, comics, resolver, ratings, markers, plex bool }
	tests := []struct {
		typ  catalogv1alpha1.MetadataProviderType
		want slots
	}{
		{catalogv1alpha1.MetadataProviderCoverArt, slots{artwork: true}},
		{catalogv1alpha1.MetadataProviderFanart, slots{artwork: true}},
		{catalogv1alpha1.MetadataProviderHardcover, slots{books: true}},
		{catalogv1alpha1.MetadataProviderMetron, slots{comics: true, resolver: true}},
		{catalogv1alpha1.MetadataProviderMangaDex, slots{comics: true, resolver: true}},
		{catalogv1alpha1.MetadataProviderAniList, slots{resolver: true}},
		{catalogv1alpha1.MetadataProviderKitsu, slots{resolver: true}},
		{catalogv1alpha1.MetadataProviderAnimeLists, slots{resolver: true}},
		{catalogv1alpha1.MetadataProviderMDBList, slots{ratings: true}},
		{catalogv1alpha1.MetadataProviderTheIntroDB, slots{markers: true}},
	}
	for _, tt := range tests {
		t.Run(string(tt.typ), func(t *testing.T) {
			a, err := buildSupplementary(catalogv1alpha1.MetadataProviderSpec{Type: tt.typ}, creds, http.DefaultClient)
			require.NoError(t, err)
			require.Equal(t, tt.want, slots{
				a.artwork != nil, a.books != nil, a.comics != nil, a.resolver != nil,
				a.ratings != nil, a.markers != nil, a.plex != nil,
			})
			require.NotNil(t, a.ping)

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
			_, err := buildSupplementary(catalogv1alpha1.MetadataProviderSpec{Type: typ}, wrong, http.DefaultClient)
			require.ErrorContains(t, err, key)
			require.NotErrorIs(t, err, ErrProviderNotImplemented, "a missing credential is a spec error, not an unknown type")
		})
	}
}

func TestAnUnknownTypeIsStillNotImplemented(t *testing.T) {
	_, err := buildSupplementary(catalogv1alpha1.MetadataProviderSpec{Type: "gcd"}, creds, http.DefaultClient)
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
// recorded on 2026-09-24; see TestEveryProviderTypeBuildsAClient).
// buildSupplementary and newSupplementaryProber must agree.
func TestOMDbIsRecognisedButBlockedUnderR5(t *testing.T) {
	for _, typ := range []catalogv1alpha1.MetadataProviderType{
		catalogv1alpha1.MetadataProviderOMDb,
	} {
		t.Run(string(typ), func(t *testing.T) {
			_, err := buildSupplementary(catalogv1alpha1.MetadataProviderSpec{Type: typ}, creds, http.DefaultClient)
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
