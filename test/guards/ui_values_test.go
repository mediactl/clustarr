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

package guards

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// uiArtKeyRef returns the ui Deployment's $CLUSTARR_ART_SIGNING_KEY source.
func uiArtKeyRef(t *testing.T, installer string, deployments []appsv1.Deployment) *corev1.SecretKeySelector {
	t.Helper()
	for _, d := range deployments {
		if d.Spec.Template.Labels["app.kubernetes.io/component"] != "ui" {
			continue
		}
		for _, e := range d.Spec.Template.Spec.Containers[0].Env {
			if e.Name == artSigningKeyEnv {
				require.NotNil(t, e.ValueFrom, "%s: %s must come from a Secret, never a literal", installer, artSigningKeyEnv)
				require.NotNil(t, e.ValueFrom.SecretKeyRef, "%s: %s must come from a Secret", installer, artSigningKeyEnv)
				return e.ValueFrom.SecretKeyRef
			}
		}
		t.Fatalf("%s: the ui Deployment sets no %s, so the photo URLs Plex stores break on every ui restart",
			installer, artSigningKeyEnv)
	}
	t.Fatalf("%s: no ui Deployment rendered", installer)
	return nil
}

// Plex stores the provider photo URLs the ui signs, so both installers give
// the ui a signing key that outlives the pod.
func TestBothInstallersGiveTheUIAStableArtSigningKey(t *testing.T) {
	t.Run("chart", func(t *testing.T) {
		helm := findTool(t, "helm")
		root, err := filepath.Abs("../..")
		require.NoError(t, err)
		r := decodeRendered(t, run(t, root, helm, "template", "clustarr", "charts/clustarr"))
		ref := uiArtKeyRef(t, "charts/clustarr", r.deployments)
		sec, ok := r.secrets[ref.Name]
		require.True(t, ok, "the chart renders no Secret %q for the ui's signing key", ref.Name)
		require.Equal(t, "keep", sec.Annotations["helm.sh/resource-policy"],
			"an uninstall or a rename must not rotate the key every stored Plex photo URL is signed with")
		require.GreaterOrEqual(t, len(sec.Data[ref.Key])+len(sec.StringData[ref.Key]), minArtSigningKeyBytes)
	})
	t.Run("kustomize", func(t *testing.T) {
		ref := uiArtKeyRef(t, "config/manager", deploymentsIn(t, "../../config/manager/ui.yaml"))
		require.NotNil(t, ref.Optional)
		require.True(t, *ref.Optional,
			"kustomize cannot generate a random key, so the operator creates the Secret and the ui starts without one")
	})
}
