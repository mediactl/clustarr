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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/metadataprovider"
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

func TestReconcileUnimplementedTypeIsUnknownNotError(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "mdp-unimplemented"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	mp := &catalogv1alpha1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "coverart", Namespace: ns},
		Spec:       catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderCoverArt},
	}
	if err := c.Create(ctx, mp); err != nil {
		t.Fatalf("create MetadataProvider: %v", err)
	}

	r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), http.DefaultClient)
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "coverart"}}); err != nil {
		t.Fatalf("Reconcile returned an error for an unimplemented type: %v", err)
	}

	var got catalogv1alpha1.MetadataProvider
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "coverart"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != "ProviderNotImplemented" {
		t.Errorf("Ready = %+v, want Unknown/ProviderNotImplemented", cond)
	}
}
