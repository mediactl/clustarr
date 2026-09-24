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

// Internal (package book) so it can call the unexported map functions.
package book

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestMapQualityProfileReachesOverridesAndInheritors pins mapQualityProfile's
// scope: an edited profile wakes the Books that name it through their own
// override and the Books with no override whose Author names it, and no
// Book ranked against anything else -- a standalone Book without an
// override is ranked against nothing.
func TestMapQualityProfileReachesOverridesAndInheritors(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.Book{}, bookByQualityProfileIndexKey, func(o client.Object) []string {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Spec.QualityProfileRef == nil || *bk.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{*bk.Spec.QualityProfileRef}
		}).
		Build()

	profileA, profileB := "profile-a", "profile-b"
	leGuin, pratchett := "le-guin", "pratchett"
	for _, o := range []client.Object{
		&catalogv1alpha1.Author{
			ObjectMeta: metav1.ObjectMeta{Name: leGuin, Namespace: "media"},
			Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", QualityProfileRef: profileA, RootFolderRef: "books"},
		},
		&catalogv1alpha1.Author{
			ObjectMeta: metav1.ObjectMeta{Name: pratchett, Namespace: "media"},
			Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL2A", QualityProfileRef: profileB, RootFolderRef: "books"},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: "overriding", Namespace: "media"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL1W", AuthorRef: &pratchett, QualityProfileRef: &profileA},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: "inheriting", Namespace: "media"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL2W", AuthorRef: &leGuin},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: "overridden-elsewhere", Namespace: "media"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL3W", AuthorRef: &leGuin, QualityProfileRef: &profileB},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: "other-author", Namespace: "media"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL4W", AuthorRef: &pratchett},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: "standalone", Namespace: "media"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL5W"},
		},
	} {
		require.NoError(t, c.Create(t.Context(), o))
	}

	r := &Reconciler{Client: c}
	reqs := r.mapQualityProfile(t.Context(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: profileA}})
	got := map[string]bool{}
	for _, req := range reqs {
		got[req.Name] = true
	}
	assert.Equal(t, map[string]bool{"overriding": true, "inheriting": true}, got)
}
