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

package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/mediactl/clustarr/app/transcode/controller/pool"
)

// R21: the manager caches the pool Jobs -- in the namespace the pools are
// created in, labelled managed-by=transcode -- and no other Job in the
// cluster. internal/cli/manager installs pool.PoolJobCache(o.Namespace) as
// the batch/v1 Job cache, the namespace poolConfig renders the pools into
// (W5.17: app/transcode's cache_test moved here; the manager's own cache
// options are internal/cli/manager's).
func TestCacheSelectsOnlyThePoolJobs(t *testing.T) {
	o := Options{}
	o.Namespace = "rel-ns"
	o.WatchNamespaces = []string{"media"}
	by := pool.PoolJobCache(o.Namespace)

	assert.Equal(t, []string{poolConfig(o).Namespace}, keys(by.Namespaces), "only the pools' namespace")
	require.NotNil(t, by.Label)
	assert.True(t, by.Label.Matches(labels.Set{pool.LabelManagedBy: pool.ManagedByValue, pool.LabelProfile: "hevc"}),
		"a pool Job is cached")
	assert.False(t, by.Label.Matches(labels.Set{"app": "backup"}), "another application's Job is not")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
