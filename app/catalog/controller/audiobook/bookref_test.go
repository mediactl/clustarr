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

package audiobook

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// This file is an internal (package audiobook) test, like
// mediafile/watch_test.go, so it can exercise resolveBookRef and mapBookRef
// directly -- both unexported.

func TestResolveBookRefUnset(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	r := &Reconciler{Client: c}

	a := &catalogv1alpha1.Audiobook{ObjectMeta: metav1.ObjectMeta{Namespace: "media"}}
	resolved, err := r.resolveBookRef(t.Context(), a)
	require.NoError(t, err)
	assert.True(t, resolved, "no bookRef set resolves trivially")

	empty := ""
	a.Spec.BookRef = &empty
	resolved, err = r.resolveBookRef(t.Context(), a)
	require.NoError(t, err)
	assert.True(t, resolved, "an empty-string bookRef is treated the same as unset")
}

func TestResolveBookRefFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithObjects(&catalogv1alpha1.Book{ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "media"}}).
		Build()
	r := &Reconciler{Client: c}

	ref := "guards-guards"
	a := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media"},
		Spec:       catalogv1alpha1.AudiobookSpec{BookRef: &ref},
	}
	resolved, err := r.resolveBookRef(t.Context(), a)
	require.NoError(t, err)
	assert.True(t, resolved)
}

func TestResolveBookRefDangling(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	r := &Reconciler{Client: c}

	ref := "no-such-book"
	a := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media"},
		Spec:       catalogv1alpha1.AudiobookSpec{BookRef: &ref},
	}
	resolved, err := r.resolveBookRef(t.Context(), a)
	require.NoError(t, err, "a dangling bookRef is never a fatal error")
	assert.False(t, resolved)
}

func TestMapBookRefWrongTypeReturnsNil(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	r := &Reconciler{Client: c}
	other := &subtitlev1alpha1.SubtitleRequest{}
	assert.Nil(t, r.mapBookRef(t.Context(), other), "wrong type returns nil, never panics")
}

func TestMapBookRefFindsEveryLinkingAudiobook(t *testing.T) {
	scheme := k8s.MustNewScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&catalogv1alpha1.Audiobook{}, audiobookByBookRefIndexKey, func(o client.Object) []string {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Spec.BookRef == nil || *a.Spec.BookRef == "" {
				return nil
			}
			return []string{*a.Spec.BookRef}
		}).
		Build()

	ref := "guards-guards"
	other := "different-book"
	linked := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "linked", Namespace: "media"},
		Spec:       catalogv1alpha1.AudiobookSpec{BookRef: &ref},
	}
	unlinked := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "unlinked", Namespace: "media"},
		Spec:       catalogv1alpha1.AudiobookSpec{BookRef: &other},
	}
	require.NoError(t, c.Create(t.Context(), linked))
	require.NoError(t, c.Create(t.Context(), unlinked))

	r := &Reconciler{Client: c}
	book := &catalogv1alpha1.Book{ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "media"}}
	reqs := r.mapBookRef(t.Context(), book)

	want := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "media", Name: "linked"}}}
	assert.Equal(t, want, reqs)
}
