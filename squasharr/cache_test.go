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

package squasharr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/mediactl/clustarr/squasharr/controller/pool"
)

// R21: squasharr's manager caches the pool Jobs -- in the namespace the pools
// are created in, labelled managed-by=squasharr -- and no other Job in the
// cluster.
func TestTheManagerCachesOnlyThePoolJobs(t *testing.T) {
	o := DefaultOptions()
	o.Namespace = "rel-ns"
	o.WatchNamespaces = []string{"media"}
	opts := o.ManagerOptions()

	var found bool
	for obj, by := range opts.Cache.ByObject {
		if _, ok := obj.(*batchv1.Job); !ok {
			continue
		}
		found = true
		assert.Equal(t, []string{poolConfig(o).Namespace}, keys(by.Namespaces), "only the pools' namespace")
		require.NotNil(t, by.Label)
		assert.True(t, by.Label.Matches(labels.Set{pool.LabelManagedBy: pool.ManagedByValue, pool.LabelProfile: "hevc"}),
			"a pool Job is cached")
		assert.False(t, by.Label.Matches(labels.Set{"app": "backup"}), "another application's Job is not")
	}
	assert.True(t, found, "no cache restriction for batch/v1 Jobs")
	assert.Contains(t, opts.Cache.DefaultNamespaces, "media", "the rest of the cache keeps --watch-namespaces")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
