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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// An agent never elects, declares its controllers non-leader instead of
// leaning on "not electing counts as elected", and caches no Secret or
// ConfigMap: every read is a by-name Get, which its roles allow (§3.5.2).
// Its client is the ReadOnly one (ADR-0019 §9.2): a write fails before any
// request leaves the process.
func TestAgentManagerOptionsNeverElect(t *testing.T) {
	o := k8s.DefaultOptions()
	o.LeaderElect = true // the agent ignores it
	opts := o.AgentManagerOptions()
	assert.False(t, opts.LeaderElection)
	assert.Empty(t, opts.LeaderElectionID)
	require.NotNil(t, opts.Controller.NeedLeaderElection)
	assert.False(t, *opts.Controller.NeedLeaderElection)
	assert.Equal(t, k8s.CacheSyncTimeout, opts.Controller.CacheSyncTimeout)
	assert.Equal(t, k8s.DefaultFieldOwner, opts.Client.FieldOwner)
	require.NotNil(t, opts.Client.Cache)
	var kinds []string
	for _, obj := range opts.Client.Cache.DisableFor {
		kinds = append(kinds, fmt.Sprintf("%T", obj))
	}
	assert.ElementsMatch(t, []string{"*v1.Secret", "*v1.ConfigMap"}, kinds)
	assert.NotNil(t, opts.Cache.DefaultTransform, "managedFields stay stripped")

	// The client the agent's manager builds refuses every write itself: the
	// server below is unreachable, so a write that got as far as a request
	// would fail with a connection error, not ErrAgentWrite.
	require.NotNil(t, opts.NewClient, "an agent's manager must build the ReadOnly client, not controller-runtime's default")
	c, err := opts.NewClient(&rest.Config{Host: "http://127.0.0.1:1"}, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	err = c.Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"}})
	require.ErrorIs(t, err, k8s.ErrAgentWrite)
}
