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

package k8s_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// A write through a manager built from ManagerOptions that names no owner
// records "clustarr"; one that names an owner keeps it (an explicit
// client.FieldOwner option wins over the client's default). The entries are
// read through the API reader: every cache strips managedFields.
func TestAnUntaggedWriteRecordsTheDefaultFieldOwner(t *testing.T) {
	cfg := startEnvtest(t)
	opts := k8s.DefaultOptions().ManagerOptions("fieldowner.clustarr.io", false)
	opts.Metrics.BindAddress, opts.HealthProbeBindAddress = "0", "0"
	mgr, err := ctrl.NewManager(cfg, opts)
	require.NoError(t, err)
	ctx := context.Background()
	c := mgr.GetClient()
	// Each carries a field: the apiserver records no managedFields entry
	// for a write that sets none, which would make both reads below empty.
	cm := func(name string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string]string{"k": "v"}}
	}
	require.NoError(t, c.Create(ctx, cm("untagged")))
	require.NoError(t, c.Create(ctx, cm("tagged"), client.FieldOwner(string(k8s.ManagerImport))))

	managers := func(name string) []string {
		var cm corev1.ConfigMap
		require.NoError(t, mgr.GetAPIReader().Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &cm))
		var out []string
		for _, mf := range cm.ManagedFields {
			out = append(out, mf.Manager)
		}
		return out
	}
	assert.Equal(t, []string{"clustarr"}, managers("untagged"))
	assert.Equal(t, []string{string(k8s.ManagerImport)}, managers("tagged"))
}
