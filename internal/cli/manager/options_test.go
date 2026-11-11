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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestManagerCacheOptions is §5.6's one merged cache: the lease
// manager.clustarr.io, the pinned default field owner, Secret and ConfigMap
// never cached, only the transcode Jobs (pools and grafts, both labelled
// managed-by) selected, and nothing stripping MediaFile status.mediaInfo.
func TestManagerCacheOptions(t *testing.T) {
	o := DefaultOptions()
	o.Namespace = "clustarr-system"
	opts := managerOptions(o)

	assert.True(t, opts.LeaderElection)
	assert.Equal(t, LeaderElectionID, opts.LeaderElectionID)
	assert.Equal(t, "clustarr-system", opts.LeaderElectionNamespace)
	assert.Equal(t, string(k8s.DefaultFieldOwner), opts.Client.FieldOwner)
	assert.Equal(t, "clustarr", opts.Client.FieldOwner)
	require.NotNil(t, opts.Client.Cache)
	var disabled []string
	for _, obj := range opts.Client.Cache.DisableFor {
		disabled = append(disabled, fmt.Sprintf("%T", obj))
	}
	assert.ElementsMatch(t, []string{"*v1.Secret", "*v1.ConfigMap"}, disabled)

	require.Len(t, opts.Cache.ByObject, 1, "only the transcode Jobs are selected; no apps/v1 or MediaFile entry")
	for obj, by := range opts.Cache.ByObject {
		require.IsType(t, &batchv1.Job{}, obj)
		require.NotNil(t, by.Label)
		assert.Equal(t, "app.kubernetes.io/managed-by=transcode", by.Label.String())
		assert.Contains(t, by.Namespaces, "clustarr-system")
		assert.Nil(t, by.Transform, "the manager reads status.mediaInfo; nothing strips it (§5.6)")
	}
	assert.NotNil(t, opts.Cache.DefaultTransform, "managedFields stay stripped")
	assert.NotNil(t, opts.NewCache, "the large kinds are held as compressed rows (pkg/k8s/rowcache)")
	var rowKinds []string
	for _, obj := range rowCacheKinds {
		rowKinds = append(rowKinds, fmt.Sprintf("%T", obj))
	}
	assert.ElementsMatch(t, []string{"*v1alpha1.Episode", "*v1alpha1.MediaFile", "*v1alpha1.Movie", "*v1alpha1.Series"}, rowKinds)
	assert.Equal(t, k8s.CacheSyncTimeout, opts.Controller.CacheSyncTimeout)
	assert.Nil(t, opts.Controller.SkipNameValidation, "production validates controller names")

	o.LeaderElect = false
	assert.False(t, managerOptions(o).LeaderElection, "--leader-elect=false runs without the lease")
	o.SkipNameValidation = true
	require.NotNil(t, managerOptions(o).Controller.SkipNameValidation)
	assert.True(t, *managerOptions(o).Controller.SkipNameValidation)
}

// The intel-work Lease is written through the leader-election Role, which
// grants Leases in the manager's own namespace alone.
func TestTheIntelWorkLeaseMustBeInTheManagersNamespace(t *testing.T) {
	ref := types.NamespacedName{Namespace: "clustarr-system", Name: "clustarr-intel-work"}
	assert.NoError(t, checkIntelWorkLease(ref, "clustarr-system"))
	assert.NoError(t, checkIntelWorkLease(types.NamespacedName{}, "clustarr-system"), "none configured")
	assert.NoError(t, checkIntelWorkLease(ref, ""), "outside a pod the namespace is unknown")
	err := checkIntelWorkLease(ref, "other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--intel-work-lease")
	assert.Contains(t, err.Error(), "must be in the manager's namespace other")
}
