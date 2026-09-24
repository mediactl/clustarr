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

// A double-claim against a field another manager owns does NOT surface as an
// apiserver conflict: pkg/k8s.PatchStatus and pkg/k8s.Apply both force
// ownership unconditionally, so an over-claim is silent everywhere except
// metadata.managedFields -- see CLAUDE.md, "A double-claim will NOT surface
// as an apiserver conflict", and grabarr/status's own doc comment. This file
// reads managedFields directly, which is the only place this class of bug is
// visible at all.
package download_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	downloadctl "github.com/mediactl/clustarr/app/grab/controller/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func managersOf(entries []metav1.ManagedFieldsEntry, subresource string) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		if e.Subresource != subresource {
			continue
		}
		out[e.Manager] = true
	}
	return out
}

// TestDownloadStatusIsOwnedOnlyByManagerGrabarr guards this task's own claim:
// every status write this controller makes goes out under k8s.ManagerGrabarr
// and nothing else -- not k8s.ManagerGrabarrEngine (the engine's disjoint
// telemetry set) and not k8s.ManagerImportarr (status.import, settled by
// D2-7). grabarr/status.Patch already refuses any manager but the first two,
// but a refusal only catches a caller that mis-names the manager at the
// Patch call site; it says nothing about whether some OTHER path in this
// package reached for k8s.PatchStatus/k8s.Apply directly with the wrong
// name. This test is what actually observes the object.
func TestDownloadStatusIsOwnedOnlyByManagerGrabarr(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	newTorrentClient(t, ctx, c, "default", "qbit-mf", 1, 1)
	markEngineReady(t, ctx, c, "default", "qbit-mf", true)

	dl := newTorrentDownload(t, ctx, c, "default", "managedfields-dl", "guid-mf-1")
	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	reconcileOK(t, r, "default", dl.Name)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: dl.Name}, &got))

	statusManagers := managersOf(got.ManagedFields, "status")
	assert.Equal(t, map[string]bool{k8s.ManagerGrabarr.String(): true}, statusManagers,
		"Download.status must be owned by k8s.ManagerGrabarr alone once this controller has reconciled it")
}

// TestDownloadMainResourceClientRefAndLabelsAreOwnedByManagerGrabarr guards
// the OTHER write this controller makes: spec.clientRef and the two
// assignment labels, applied on the main resource (not the status
// subresource) via k8s.Apply. It must be attributed to k8s.ManagerGrabarr
// too, the same field-manager vocabulary k8s.PatchStatus and k8s.Apply share
// by design (pkg/k8s/patch.go's own doc comment).
func TestDownloadMainResourceClientRefAndLabelsAreOwnedByManagerGrabarr(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	newTorrentClient(t, ctx, c, "default", "qbit-mf2", 1, 1)
	markEngineReady(t, ctx, c, "default", "qbit-mf2", true)

	dl := newTorrentDownload(t, ctx, c, "default", "managedfields-main-dl", "guid-mf-2")
	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	reconcileOK(t, r, "default", dl.Name)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: dl.Name}, &got))

	mainManagers := managersOf(got.ManagedFields, "")
	assert.Contains(t, mainManagers, k8s.ManagerGrabarr.String())
}
