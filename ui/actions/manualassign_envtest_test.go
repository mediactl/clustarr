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

package actions_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// TestManualAssignManagerNeverOwnsStatus is the coordinator's own
// requirement: "one envtest creating the scan against a real apiserver and
// asserting managedFields shows no clustarr-ui on status." It mirrors
// TestUIManagerNeverOwnsStatus's own shape (actions_envtest_test.go) for the
// same reason that one exists -- an over-claim is silent on the object's
// values (CLAUDE.md), so only metadata.managedFields can show whether
// [actions.FieldManager] reaches into a status path it must never touch.
//
// ManualAssign creates a LibraryScan (a status-subresource kind), so a plain
// Create structurally cannot populate status even if this test's fixture set
// one -- the apiserver routes .status through a separate endpoint. What this
// test actually catches is a future ManualAssign that starts calling
// Status().Update/Patch/Apply directly (ui/guard_test.go's AST guard already
// forbids the source from doing that; this is the runtime half of the same
// proof, the way TestUIManagerNeverOwnsStatus is source vs. runtime for
// SetMonitored).
func TestManualAssignManagerNeverOwnsStatus(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	ctx := t.Context()
	const ns = "default"

	target := actions.ManualAssignTarget{Kind: commonv1.MediaKindMovie, Name: "heat-1995"}
	scan, err := actions.ManualAssign(ctx, c, ns, "movies", "Heat (1995)/Heat.1995.mkv", target)
	require.NoError(t, err)
	require.True(t, scan.Name != "", "the apiserver must have named the scan")

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(catalogv1alpha1.GroupVersion.WithKind("LibraryScan"))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: scan.Name}, got))

	found := false
	for _, e := range got.GetManagedFields() {
		if e.Manager != actions.FieldManager {
			continue
		}
		found = true
		require.NotEqual(t, "status", e.Subresource,
			"clustarr-ui has a managedFields entry on the status subresource -- the UI never writes status")
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		_, hasStatus := fields["f:status"]
		require.False(t, hasStatus, "clustarr-ui owns a status field: %v", fields)
	}
	require.True(t, found, "expected exactly one clustarr-ui managedFields entry on the created scan")

	// The annotation and spec this action promises are also on the real
	// object, not just what the fake writer in manualassign_test.go
	// recorded.
	annotations, _, err := unstructured.NestedStringMap(got.Object, "metadata", "annotations")
	require.NoError(t, err)
	require.Equal(t, "movie/heat-1995", annotations[actions.AnnotationImportTarget])
	subpath, _, err := unstructured.NestedString(got.Object, "spec", "subpath")
	require.NoError(t, err)
	require.Equal(t, "Heat (1995)/Heat.1995.mkv", subpath)
}
