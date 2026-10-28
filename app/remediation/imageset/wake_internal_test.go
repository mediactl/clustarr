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

package imageset

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
)

// A mediux provider turning up (or Ready, or edited) wakes every Movie and
// Series of its namespace: an item is planned only when its key wakes, and
// one with no image set and no provider is due never, so on talos
// (2026-10-28) the provider went Ready and nothing was asked until each
// item woke for some other reason. Another type of provider wakes nothing.
func TestAMediuxProviderWakesItsNamespacesItems(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat"}},
		&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "alien"}},
		&catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "lost"}},
	).Build()

	mediux := &catalogv1alpha1.MetadataProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "mediux"}, Spec: catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderMediux}}
	tmdb := &catalogv1alpha1.MetadataProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tmdb"}, Spec: catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB}}
	assert.True(t, isMediux(mediux))
	assert.False(t, isMediux(tmdb))
	assert.False(t, isMediux(&catalogv1alpha1.Movie{}), "not a provider at all")

	keys := itemsOf(context.Background(), c, "media")
	assert.ElementsMatch(t, []remediation.Key{
		{Kind: remediation.KindMovie, Namespace: "media", Name: "heat"},
		{Kind: remediation.KindSeries, Namespace: "media", Name: "lost"},
	}, keys)
	assert.Empty(t, itemsOf(context.Background(), c, "empty"))
	var _ client.Reader = c
}
