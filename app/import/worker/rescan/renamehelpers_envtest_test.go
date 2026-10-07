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

package rescan_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The rename pass's envtests (scanrename_envtest_test.go) share these with
// RenameFile's own, which moved to app/import/mediafilespec with it.

// correctedQuality is the probe-corrected quality catalogarr proposes: the
// name said 1080p, the probe found 2160p.
var correctedQuality = commonv1.Quality{Name: "Bluray-2160p", Source: commonv1.SourceBluray, Resolution: 2160}

// staleFile is one steady MediaFile catalogarr has proposed a new name for.
type staleFile struct {
	name     string
	path     string // spec.path, where the file is
	expected string // status.naming.expectedPath
}

// seedNamingStatus applies catalogarr's status as its MediaFile reconciler
// would for a file it proposes to rename: Ready, Probed and a False
// NamingCurrent, beside naming and the sidecars.
func (f *fixture) seedNamingStatus(t *testing.T, ctx context.Context, name string,
	naming *catalogac.NamingStatusApplyConfiguration, sidecars ...*catalogac.SidecarApplyConfiguration,
) {
	t.Helper()
	now := metav1.Now()
	conditions := []metav1.Condition{
		{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "file present and probed", LastTransitionTime: now},
		{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed", Message: "probed", LastTransitionTime: now},
		{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale", Message: "the file's canonical path differs", LastTransitionTime: now},
	}
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalog, catalogac.MediaFile(name, f.ns).WithStatus(
		catalogac.MediaFileStatus().
			WithConditions(k8s.ConditionACs(conditions)...).
			WithNaming(naming).
			WithSidecars(sidecars...)))
	require.NoError(t, err)
}

// read reads a MediaFile past the cache.
func (f *fixture) read(t *testing.T, ctx context.Context, name string) *catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api(t).Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &mf))
	return &mf
}

func requireExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.NoError(t, err, "%s should exist", path)
}

func requireAbsent(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	require.True(t, os.IsNotExist(err), "%s should not exist (err %v)", path, err)
}
