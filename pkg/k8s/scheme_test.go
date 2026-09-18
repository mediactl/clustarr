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

package k8s

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

func TestSchemeKnowsEveryGroupAServiceTouches(t *testing.T) {
	s := MustNewScheme()

	objects := map[string]runtime.Object{
		// The five Clustarr groups.
		"catalog":   &catalogv1alpha1.Movie{},
		"index":     &indexv1alpha1.Indexer{},
		"download":  &downloadv1alpha1.Download{},
		"transcode": &transcodev1alpha1.TranscodeJob{},
		"subtitle":  &subtitlev1alpha1.SubtitleRequest{},

		// What the managers create and consume.
		"core secret":      &corev1.Secret{},
		"core pvc":         &corev1.PersistentVolumeClaim{},
		"batch job":        &batchv1.Job{},
		"apps statefulset": &appsv1.StatefulSet{},
		"apps deployment":  &appsv1.Deployment{},
		"leader lease":     &coordinationv1.Lease{},
		"history event":    &eventsv1.Event{},
	}

	for label, obj := range objects {
		if !s.Recognizes(mustGVK(t, s, obj)) {
			t.Errorf("%s (%T) is not in the scheme", label, obj)
		}
	}
}

func TestSchemeCoversEveryClustarrGroup(t *testing.T) {
	s := MustNewScheme()
	want := map[string]bool{
		"catalog.clustarr.io":   false,
		"index.clustarr.io":     false,
		"download.clustarr.io":  false,
		"transcode.clustarr.io": false,
		"subtitle.clustarr.io":  false,
	}
	for gvk := range s.AllKnownTypes() {
		if _, ok := want[gvk.Group]; ok {
			want[gvk.Group] = true
		}
	}
	for group, seen := range want {
		if !seen {
			t.Errorf("group %s is missing from the scheme", group)
		}
	}
}

func TestAddToSchemeRejectsNil(t *testing.T) {
	if err := AddToScheme(nil); err == nil {
		t.Fatal("AddToScheme(nil) returned no error")
	}
}

func TestAddToSchemeIsIdempotent(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("first AddToScheme: %v", err)
	}
	if err := AddToScheme(s); err != nil {
		t.Fatalf("second AddToScheme: %v", err)
	}
}

func mustGVK(t *testing.T, s *runtime.Scheme, obj runtime.Object) schema.GroupVersionKind {
	t.Helper()
	gvks, _, err := s.ObjectKinds(obj)
	if err != nil {
		t.Fatalf("ObjectKinds(%T): %v", obj, err)
	}
	if len(gvks) == 0 {
		t.Fatalf("ObjectKinds(%T) returned nothing", obj)
	}
	return gvks[0]
}
