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

package rollup_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// TestDownloadOverlay asserts the overlay for every DownloadPhase value
// (and for a Download being deleted): each non-terminal one is Downloading
// and active, each terminal one has no opinion and is not.
func TestDownloadOverlay(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	cases := []struct {
		name       string
		dl         *downloadv1alpha1.Download
		wantClass  rollup.Overlay
		wantActive bool
	}{
		{"nil download, nothing to say", nil, rollup.OverlayNone, false},
		{"no phase yet: the grab made it, grabarr has not seen it", dl(""), rollup.OverlayDownloading, true},
		{"pending is grabbed and queued, not delayed", dl(downloadv1alpha1.DownloadPhasePending), rollup.OverlayDownloading, true},
		{"assigned", dl(downloadv1alpha1.DownloadPhaseAssigned), rollup.OverlayDownloading, true},
		{"queued", dl(downloadv1alpha1.DownloadPhaseQueued), rollup.OverlayDownloading, true},
		{"downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), rollup.OverlayDownloading, true},
		{"paused is still the item's download", dl(downloadv1alpha1.DownloadPhasePaused), rollup.OverlayDownloading, true},
		{"completed is waiting for import, not missing", dl(downloadv1alpha1.DownloadPhaseCompleted), rollup.OverlayDownloading, true},
		{"seeding is not imported yet", dl(downloadv1alpha1.DownloadPhaseSeeding), rollup.OverlayDownloading, true},
		{"imported defers to the MediaFile", dl(downloadv1alpha1.DownloadPhaseImported), rollup.OverlayNone, false},
		{"failed defers: the item is wanted again", dl(downloadv1alpha1.DownloadPhaseFailed), rollup.OverlayNone, false},
		{"blocklisted defers", dl(downloadv1alpha1.DownloadPhaseBlocklisted), rollup.OverlayNone, false},
		{"removing defers", dl(downloadv1alpha1.DownloadPhaseRemoving), rollup.OverlayNone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			overlay, active := rollup.DownloadOverlay(c.dl)
			assert.Equal(t, c.wantClass, overlay)
			assert.Equal(t, c.wantActive, active)
			assert.Equal(t, rollup.DownloadNonTerminal(c.dl), active, "the overlay and the ref are one test")
		})
	}

	t.Run("a Download being deleted defers whatever its phase", func(t *testing.T) {
		d := dl(downloadv1alpha1.DownloadPhaseDownloading)
		now := metav1.Now()
		d.DeletionTimestamp = &now
		overlay, active := rollup.DownloadOverlay(d)
		assert.Equal(t, rollup.OverlayNone, overlay)
		assert.False(t, active)
	})

	// Every value the Download CRD admits for status.phase has a row above,
	// read from the generated CRD rather than restated: a phase added to the
	// API without a row here is a mapping nobody decided.
	t.Run("every DownloadPhase the CRD admits has a row", func(t *testing.T) {
		covered := map[string]bool{}
		for _, c := range cases {
			if c.dl != nil {
				covered[string(c.dl.Status.Phase)] = true
			}
		}
		enum := crdPhaseEnum(t)
		require.NotEmpty(t, enum)
		for _, p := range enum {
			assert.True(t, covered[p], "no row for DownloadPhase %q", p)
		}
	})
}

// crdPhaseEnum reads status.phase's enum out of the generated Download CRD.
func crdPhaseEnum(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../../../config/crd/bases/download.clustarr.io_downloads.yaml")
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions)
	phase := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"].Properties["phase"]
	out := make([]string, 0, len(phase.Enum))
	for _, v := range phase.Enum {
		var s string
		require.NoError(t, json.Unmarshal(v.Raw, &s))
		out = append(out, s)
	}
	return out
}
