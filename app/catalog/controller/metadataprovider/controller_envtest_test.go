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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/metadataprovider"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestReconcileReachableProviderIsReadyAndAuthenticated(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "mdp-reachable"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 603, "title": "The Matrix"})
	}))
	defer srv.Close()
	if err := c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: ns}, Data: map[string][]byte{"apiKey": []byte("k")},
	}); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	mp := &catalogv1alpha1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: ns},
		Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderTMDB, BaseURL: &srv.URL,
			SecretRef: &corev1.LocalObjectReference{Name: "creds"},
		},
	}
	if err := c.Create(ctx, mp); err != nil {
		t.Fatalf("create MetadataProvider: %v", err)
	}

	r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), srv.Client())
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "tmdb"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got catalogv1alpha1.MetadataProvider
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tmdb"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady) {
		t.Errorf("Ready is not True: %+v", got.Status.Conditions)
	}
	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated) {
		t.Error("Authenticated is not True")
	}
	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionThrottled) {
		t.Error("Throttled is True for a healthy server")
	}
}

func TestReconcileDisabledProviderSkipsProbe(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "mdp-disabled"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	disabled := false
	mp := &catalogv1alpha1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: ns},
		Spec:       catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: &disabled},
	}
	if err := c.Create(ctx, mp); err != nil {
		t.Fatalf("create MetadataProvider: %v", err)
	}

	r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), http.DefaultClient)
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "tmdb"}}); err != nil {
		t.Fatalf("Reconcile: %v", err) // must not attempt a real network call against a fake key
	}

	var got catalogv1alpha1.MetadataProvider
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tmdb"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
	if cond == nil || cond.Reason != k8s.ReasonDisabled {
		t.Errorf("Ready reason = %+v, want %s", cond, k8s.ReasonDisabled)
	}
}

// fixtures is test/data/metadata, relative to this package.
const fixtures = "../../../../test/data/metadata/"

// addedTypeServer answers the one probe a supplementary provider's Ping
// makes with that client's own recorded fixture, checks the credential
// the provider type authenticates with, and fails anything else -- so a
// Ready=True proves the probe asked the right question with the right
// credential, not merely that a server answered.
type addedType struct {
	typ     catalogv1alpha1.MetadataProviderType
	path    string // request path the probe must use
	fixture string // body to answer with; "" answers an empty 200
	authOK  func(*http.Request) bool
}

var addedTypes = []addedType{
	{catalogv1alpha1.MetadataProviderCoverArt, "/release-group/1b022e01-4da6-387b-8658-8678046e4cef", "coverart/release-group_1b022e01-4da6-387b-8658-8678046e4cef.json", nil},
	{catalogv1alpha1.MetadataProviderFanart, "/v3.2/movies/603", "fanart/movie_603.json", func(r *http.Request) bool { return r.URL.Query().Get("api_key") == "k" }},
	{catalogv1alpha1.MetadataProviderHardcover, "/", "hardcover/search_out_of_my_mind.json", func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer t" }},
	{catalogv1alpha1.MetadataProviderMetron, "/series/", "metron/series_list_empty.json", func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer t" }},
	{catalogv1alpha1.MetadataProviderMangaDex, "/manga", "mangadex/search_berserk.json", nil},
	{catalogv1alpha1.MetadataProviderAniList, "/", "anilist/ids_anime_mal_1735.json", nil},
	{catalogv1alpha1.MetadataProviderKitsu, "/mappings", "kitsu/mappings_empty.json", nil},
	{catalogv1alpha1.MetadataProviderAnimeLists, "/anime-list-full.json", "", nil},
}

func (a addedType) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != a.path {
			t.Errorf("%s probe asked for %s, want %s", a.typ, r.URL.Path, a.path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if a.authOK != nil && !a.authOK(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if a.fixture == "" {
			return
		}
		body, err := os.ReadFile(fixtures + a.fixture)
		if err != nil {
			t.Errorf("read fixture: %v", err)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func reconcileAddedType(t *testing.T, ctx context.Context, c client.Client, ns string, a addedType, secret map[string][]byte) catalogv1alpha1.MetadataProvider {
	t.Helper()
	srv := a.server(t)
	base := srv.URL
	if a.typ == catalogv1alpha1.MetadataProviderAnimeLists {
		base += a.path // animelists' base URL is the dataset file itself
	}
	name := string(a.typ)
	if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: secret}); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := c.Create(ctx, &catalogv1alpha1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: a.typ, BaseURL: &base, SecretRef: &corev1.LocalObjectReference{Name: name},
		},
	}); err != nil {
		t.Fatalf("create MetadataProvider: %v", err)
	}
	r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), srv.Client())
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
		t.Fatalf("Reconcile %s: %v", a.typ, err)
	}
	var got catalogv1alpha1.MetadataProvider
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	return got
}

// TestReconcileEveryAddedProviderTypeBecomesReady replaces the test that
// pinned coverart at Ready=Unknown/ProviderNotImplemented: each of the
// eight types task X6b gave a client probes its own recorded fixture
// through the real Reconciler and a real apiserver, and reports Ready and
// Authenticated.
func TestReconcileEveryAddedProviderTypeBecomesReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "mdp-added"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	secret := map[string][]byte{"apiKey": []byte("k"), "bearer": []byte("t")}

	for _, a := range addedTypes {
		t.Run(string(a.typ), func(t *testing.T) {
			got := reconcileAddedType(t, ctx, c, ns, a, secret)
			ready := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
			if ready == nil || ready.Status != metav1.ConditionTrue {
				t.Errorf("Ready = %+v, want True", ready)
			}
			if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated) {
				t.Errorf("Authenticated is not True: %+v", got.Status.Conditions)
			}
		})
	}
}

// TestReconcileAnAddedTypeWithARejectedCredentialIsNotReady proves the
// supplementary probers carry a provider's 401 through to the
// Authenticated condition, as the Phase B probers do.
func TestReconcileAnAddedTypeWithARejectedCredentialIsNotReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "mdp-added-rejected"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	got := reconcileAddedType(t, ctx, c, ns, addedTypes[2], map[string][]byte{"bearer": []byte("revoked")}) // hardcover
	ready := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "CredentialsRejected" {
		t.Errorf("Ready = %+v, want False/CredentialsRejected", ready)
	}
	auth := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated)
	if auth == nil || auth.Status != metav1.ConditionFalse {
		t.Errorf("Authenticated = %+v, want False", auth)
	}
}

// TestReconcileMDBListAndOMDbAreNotReadyUnderR5 is ruling R5's CR-level
// contract (spec §C.3): with neither client written (no recorded response
// shape; MDBLIST_API_KEY/OMDB_API_KEY unset at task C1's dispatch), a
// MetadataProvider naming either type must not read as merely unimplemented
// (Ready=Unknown/ProviderNotImplemented, the fate of a type this package
// has genuinely never heard of) -- it is a real MetadataProviderType the
// CRD enum and this Reconciler both know, so it reports a definite
// Ready=False/InvalidSpec, with the message naming exactly what blocks it,
// through the real Reconciler and a real apiserver.
func TestReconcileMDBListAndOMDbAreNotReadyUnderR5(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "mdp-ratings-blocked"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	for _, typ := range []catalogv1alpha1.MetadataProviderType{
		catalogv1alpha1.MetadataProviderMDBList, catalogv1alpha1.MetadataProviderOMDb,
	} {
		t.Run(string(typ), func(t *testing.T) {
			name := string(typ)
			secret := map[string][]byte{"apiKey": []byte("would-be-a-real-key")}
			if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: secret}); err != nil {
				t.Fatalf("create secret: %v", err)
			}
			if err := c.Create(ctx, &catalogv1alpha1.MetadataProvider{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: catalogv1alpha1.MetadataProviderSpec{
					Type: typ, SecretRef: &corev1.LocalObjectReference{Name: name},
				},
			}); err != nil {
				t.Fatalf("create MetadataProvider: %v", err)
			}

			r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), http.DefaultClient)
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
				t.Fatalf("Reconcile %s: %v", typ, err)
			}
			var got catalogv1alpha1.MetadataProvider
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
				t.Fatalf("get: %v", err)
			}

			ready := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
			if ready == nil || ready.Status != metav1.ConditionFalse {
				t.Fatalf("Ready = %+v, want False", ready)
			}
			if ready.Reason != k8s.ReasonInvalidSpec {
				t.Errorf("Ready.Reason = %q, want %q -- not ProviderNotImplemented, the type is known", ready.Reason, k8s.ReasonInvalidSpec)
			}
			if !strings.Contains(ready.Message, "not implemented: awaiting recorded fixtures (C1 follow-up)") {
				t.Errorf("Ready.Message = %q, want it to name the R5 block", ready.Message)
			}
		})
	}
}
