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

package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	importapp "github.com/mediactl/clustarr/app/import"
)

func TestRunRejectsAnUnknownRole(t *testing.T) {
	err := importapp.Run(context.Background(), importapp.Options{Role: "nonsense"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "role")
}

func TestManagerOptionsUseTheImportLeaderElectionID(t *testing.T) {
	o := importapp.Options{Role: "controller", LeaderElect: true}
	mo := o.ManagerOptions()
	require.Equal(t, "importarr.clustarr.io", mo.LeaderElectionID)
	require.True(t, mo.LeaderElection)
}

func TestWorkerRoleDoesNotLeaderElect(t *testing.T) {
	o := importapp.Options{Role: "worker", LeaderElect: true}
	require.False(t, o.ManagerOptions().LeaderElection,
		"workers are queue consumers; electing a leader would idle every other replica")
}

func TestAllRoleLeaderElectsWhenAsked(t *testing.T) {
	o := importapp.Options{Role: "all", LeaderElect: true}
	require.True(t, o.ManagerOptions().LeaderElection,
		"the all role runs controllers too, so it takes the lease like a dedicated controller replica")
}

func TestValidateRejectsAnEmptyDataPath(t *testing.T) {
	o := importapp.Options{Role: "worker", DataPath: "  "}
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "data-path")
}

func TestValidateRequiresTheBus(t *testing.T) {
	o := importapp.DefaultOptions()
	o.NATSURL = ""
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "nats-url")
}

// mediaFileCacheConfig is the ByObject entry o's manager gives MediaFile.
func mediaFileCacheConfig(o importapp.Options) (cache.ByObject, bool) {
	for obj, cfg := range o.ManagerOptions().Cache.ByObject {
		if _, ok := obj.(*catalogv1alpha1.MediaFile); ok {
			return cfg, true
		}
	}
	return cache.ByObject{}, false
}

// The controller role caches MediaFiles without status.mediaInfo -- the
// rename controller watches all of them in a 512Mi pod and none of its code
// reads it -- and without managedFields, as every cache does; everything
// else a controller reads is kept. A role that also runs the workers keeps
// the whole object.
func TestTheControllerRoleCachesMediaFilesWithoutMediaInfo(t *testing.T) {
	cfg, ok := mediaFileCacheConfig(importapp.Options{Role: importapp.RoleController})
	require.True(t, ok, "the controller role sets no MediaFile cache transform")
	require.NotNil(t, cfg.Transform)

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{
			Name:          "heat",
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "catalogarr"}},
		},
		Spec: catalogv1alpha1.MediaFileSpec{Path: "/data/media/movies/Heat/heat.mkv"},
		Status: catalogv1alpha1.MediaFileStatus{
			Conditions: []metav1.Condition{{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse}},
			MediaInfo:  &commonv1.MediaInfo{Width: 1920, Height: 1080},
			Sidecars:   []catalogv1alpha1.Sidecar{{Path: "/data/media/movies/Heat/heat.en.srt", Language: "en"}},
			Naming:     &catalogv1alpha1.NamingStatus{ExpectedPath: "/data/media/movies/Heat/Heat (1995).mkv"},
			ProbeHash:  "hash",
		},
	}
	want := mf.DeepCopy()
	want.ManagedFields = nil
	want.Status.MediaInfo = nil

	got, err := cfg.Transform(mf)
	require.NoError(t, err)
	require.Equal(t, want, got)

	for _, role := range []importapp.Role{importapp.RoleWorker, importapp.RoleAll, "controller,worker"} {
		_, ok := mediaFileCacheConfig(importapp.Options{Role: role})
		require.False(t, ok, "role %q keeps the whole MediaFile", role)
	}
}

// No importarr role caches Secrets or ConfigMaps: the role grants get alone
// on both, and a cached read starts an informer, which needs list and watch
// on every one in scope. The list worker's read of a Plex watchlist's token
// Secret waited forever on that informer, so no ImportList ever synced
// (kind-cluster-plex, 2026-10-06).
func TestManagerOptionsNeverCacheSecretsOrConfigMaps(t *testing.T) {
	for _, role := range importapp.Roles() {
		mo := importapp.Options{Role: role}.ManagerOptions()
		require.NotNil(t, mo.Client.Cache, "%s caches Secrets", role)
		var secrets, configMaps bool
		for _, obj := range mo.Client.Cache.DisableFor {
			_, s := obj.(*corev1.Secret)
			_, c := obj.(*corev1.ConfigMap)
			secrets, configMaps = secrets || s, configMaps || c
		}
		require.True(t, secrets, "%s caches Secrets", role)
		require.True(t, configMaps, "%s caches ConfigMaps", role)
	}
}
