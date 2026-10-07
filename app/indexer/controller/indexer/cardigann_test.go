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

package indexer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func fakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, k8s.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func idxDefinition(name, yaml string, replaces *string, id string) *indexv1alpha1.IndexerDefinition {
	return &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: yaml, Replaces: replaces},
		Status:     indexv1alpha1.IndexerDefinitionStatus{ID: id},
	}
}

// The watch must carry an alias too: an Indexer naming a retired id is
// re-reconciled when the definition that replaces it changes, not only on
// the one-minute DefinitionNotFound retry.
func TestIndexersForDefinitionFollowsReplacedIDs(t *testing.T) {
	def := idxDefinition("renamed", "", nil, "new-tracker")
	def.Status.Replaces = []string{"old-tracker"}
	byID := func(name, id string) *indexv1alpha1.Indexer {
		return &indexv1alpha1.Indexer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       indexv1alpha1.IndexerSpec{Definition: ptr.To(id)},
		}
	}
	c := fakeClient(t, byID("by-old", "old-tracker"), byID("by-new", "new-tracker"), byID("unrelated", "other"))
	r := &Reconciler{Client: c}
	var names []string
	for _, req := range r.indexersForDefinition(context.Background(), def) {
		names = append(names, req.Name)
	}
	require.ElementsMatch(t, []string{"by-old", "by-new"}, names)

	// And the predicate sees a status.replaces change as a change.
	moved := def.DeepCopy()
	moved.Status.Replaces = []string{"old-tracker", "oldest-tracker"}
	require.NotEqual(t, definitionIDs(def), definitionIDs(moved))
}
