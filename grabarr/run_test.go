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
	"k8s.io/apimachinery/pkg/labels"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr"
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

// Every grabarr role caches only the Secrets labelled for a watch: the
// DownloadClient controller watches those for rotated usenet credentials and
// reads the rest by name, so no role ever caches the cluster's other Secrets.
func TestManagerOptionsCacheOnlyWatchLabelledSecrets(t *testing.T) {
	for _, role := range grabarr.Roles() {
		o := grabarr.DefaultOptions()
		o.Role = role
		var found bool
		for obj, by := range o.ManagerOptions().Cache.ByObject {
			if _, ok := obj.(*corev1.Secret); !ok {
				continue
			}
			found = true
			require.NotNil(t, by.Label, "%s caches every Secret", role)
			require.True(t, by.Label.Matches(labels.Set{downloadv1alpha1.LabelWatch: downloadv1alpha1.LabelWatchValue}))
			require.False(t, by.Label.Matches(labels.Set{}), "%s caches an unlabelled Secret", role)
		}
		require.True(t, found, "%s has no Secret cache filter", role)
	}
}
