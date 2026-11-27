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

package itempass_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
)

// newer is a cache that has moved on: every Get answers the item at a
// resourceVersion after the pass's view.
type newer struct{ client.Reader }

func (newer) Get(_ context.Context, key client.ObjectKey, out client.Object, _ ...client.GetOption) error {
	out.(*catalogv1alpha1.Series).ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, ResourceVersion: "8"}
	return nil
}

// Inside a staged pass the kind reads the object its stages planned on, a
// copy of it, whatever the cache holds by then; another item, and any read
// outside a pass, is the reader's.
func TestTheKindReadsTheObjectItsStagesPlannedOn(t *testing.T) {
	view := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "show", ResourceVersion: "7"}}
	view.Status.Downloads = []catalogv1alpha1.DownloadEntry{{ID: "e1"}}
	ctx := itempass.With(context.Background(), &itempass.Pass{View: view})
	nn := types.NamespacedName{Namespace: "media", Name: "show"}

	var got catalogv1alpha1.Series
	require.NoError(t, itempass.Read(ctx, newer{}, nn, &got))
	assert.Equal(t, "7", got.ResourceVersion, "the view's resourceVersion is the apply's precondition")
	require.Len(t, got.Status.Downloads, 1)
	got.Status.Downloads[0].ID = "changed"
	assert.Equal(t, "e1", view.Status.Downloads[0].ID, "a copy: the kind's pass never writes into the view")

	var other catalogv1alpha1.Series
	require.NoError(t, itempass.Read(ctx, newer{}, types.NamespacedName{Namespace: "media", Name: "other"}, &other))
	assert.Equal(t, "8", other.ResourceVersion, "another item is read")

	var outside catalogv1alpha1.Series
	require.NoError(t, itempass.Read(context.Background(), newer{}, nn, &outside))
	assert.Equal(t, "8", outside.ResourceVersion, "outside a staged pass the item is read")
}

// Every item kind's own pass starts from itempass.Read. One that read its
// item from the cache again would apply the stages' plan at whatever
// resourceVersion the cache had reached (Read's record: the imports that
// ran twice on 2026-11-27), and no test of the staged pass sees a real
// kind's reconciler, so the source is held to it here.
func TestEveryItemKindStartsFromThePassesView(t *testing.T) {
	files, err := filepath.Glob("../*/reconciler.go")
	require.NoError(t, err)
	kinds := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		_, body, ok := strings.Cut(string(src), ") ReconcileItem(ctx context.Context, nn types.NamespacedName) (")
		if !ok {
			continue
		}
		kinds++
		body, _, _ = strings.Cut(body, "\n}\n")
		assert.Contains(t, body, "itempass.Read(ctx, r.Client, nn, &", "%s: ReconcileItem reads its item through the pass", f)
		assert.NotContains(t, body, "r.Get(ctx, nn, &", "%s: ReconcileItem reads its item from the cache", f)
	}
	assert.Equal(t, 10, kinds, "the item kinds with a pass of their own: a new one is held to this too")
}
