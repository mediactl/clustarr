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

package grabarr_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	grabarr "github.com/mediactl/clustarr/app/grab"
)

func TestRunRejectsAnUnknownRole(t *testing.T) {
	err := grabarr.Run(context.Background(), grabarr.Options{Role: "nonsense"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "role")
}

func TestManagerOptionsUseTheGrabarrLeaderElectionID(t *testing.T) {
	o := grabarr.DefaultOptions()
	o.Role = grabarr.RoleController
	o.LeaderElect = true
	mo := o.ManagerOptions()
	require.Equal(t, "grabarr.clustarr.io", mo.LeaderElectionID)
	require.True(t, mo.LeaderElection)
}

// TestEngineRoleDoesNotLeaderElect proves the doc comment on
// [grabarr.Options.ManagerOptions]: an engine embeds its own download.Client
// and knows only its own transfers, so there is nothing a lease would
// coordinate between replicas.
func TestEngineRoleDoesNotLeaderElect(t *testing.T) {
	o := grabarr.Options{Role: grabarr.RoleTorrentEngine, LeaderElect: true}
	require.False(t, o.ManagerOptions().LeaderElection)
}

func TestValidateRequiresEngineForAnEngineRole(t *testing.T) {
	o := grabarr.DefaultOptions()
	o.Role = grabarr.RoleTorrentEngine
	o.DataDir = t.TempDir()
	o.NATSURL = "nats://127.0.0.1:4222"
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--engine")
}

func TestValidateRejectsEngineOnAControllerRole(t *testing.T) {
	o := grabarr.DefaultOptions()
	o.EngineImage = "example/engine:dev"
	o.NATSURL = "nats://127.0.0.1:4222"
	o.Engine = "torrents-0"
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--engine")
}

// TestValidateRequiresEngineImageForTheControllerRole is the guard behind
// plan task D2-8's own note on grabarr/controller/downloadclient/doc.go:
// "There is no default -- guessing an image tag would silently run the
// wrong engine."
func TestValidateRequiresEngineImageForTheControllerRole(t *testing.T) {
	o := grabarr.DefaultOptions()
	o.NATSURL = "nats://127.0.0.1:4222"
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--engine-image")
}

// TestValidateDoesNotRequireEngineImageForAnEngineRole: only the
// DownloadClient controller stamps it onto a workload; an engine pod never
// reads it.
func TestValidateDoesNotRequireEngineImageForAnEngineRole(t *testing.T) {
	o := grabarr.DefaultOptions()
	o.Role = grabarr.RoleUsenetEngine
	o.Engine = "sabnzbd-0"
	o.DataDir = t.TempDir()
	o.ScratchDir = t.TempDir()
	o.NATSURL = "nats://127.0.0.1:4222"
	err := o.Validate()
	require.NoError(t, err)
}

func TestValidateRequiresTheBus(t *testing.T) {
	o := grabarr.DefaultOptions()
	o.EngineImage = "example/engine:dev"
	o.NATSURL = ""
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "nats-url")
}

// No grabarr role caches Secrets: every read is a by-name get, so none
// starts an informer, which would need list and watch on every Secret in
// scope (the controller's ruling on cdde136).
func TestManagerOptionsNeverCacheSecrets(t *testing.T) {
	for _, role := range grabarr.Roles() {
		o := grabarr.DefaultOptions()
		o.Role = role
		mo := o.ManagerOptions()
		require.NotNil(t, mo.Client.Cache, "%s caches Secrets", role)
		var disabled bool
		for _, obj := range mo.Client.Cache.DisableFor {
			_, ok := obj.(*corev1.Secret)
			disabled = disabled || ok
		}
		require.True(t, disabled, "%s caches Secrets", role)
		for obj := range mo.Cache.ByObject {
			_, ok := obj.(*corev1.Secret)
			require.False(t, ok, "%s configures a Secret informer", role)
		}
	}
}
