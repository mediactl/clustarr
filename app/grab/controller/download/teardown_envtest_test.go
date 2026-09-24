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

package download_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	downloadctl "github.com/mediactl/clustarr/app/grab/controller/download"
	"github.com/mediactl/clustarr/app/grab/engine"
	grabarrstatus "github.com/mediactl/clustarr/app/grab/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// patchEngine re-points status.engine through the controller's own complete
// declaration, standing in for a Download pinned to an ordinal its client
// has since scaled below.
func patchEngine(ctx context.Context, c client.Client, dl *downloadv1alpha1.Download, engineID string) error {
	return grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarr, dl, func(ac *downloadac.DownloadStatusApplyConfiguration) {
		ac.WithEngine(engineID)
	})
}

// assignedWithEngineFinalizer creates a Download, lets the controller pin it
// to engine "<client>-0", plants real output on disk, adds the engine
// finalizer the way an engine does on its first reconcile, and deletes it.
// It returns the deleting object and its output path.
func assignedWithEngineFinalizer(
	t *testing.T, ctx context.Context, c client.Client, r *downloadctl.Reconciler, ns, clientName, name string,
) (*downloadv1alpha1.Download, string) {
	t.Helper()
	dl := newTorrentDownload(t, ctx, c, ns, name, "guid-"+name)
	reconcileOK(t, r, ns, dl.Name) // pins the engine and adds the controller's finalizer
	pinned := getDownload(t, ctx, c, ns, dl.Name)
	require.Equal(t, clientName+"-0", pinned.Status.Engine)

	outputPath := filepath.Join(r.DataDir, "torrents", "movies", dl.Name)
	seedOutputPath(t, ctx, c, ns, dl.Name, outputPath)

	live := getDownload(t, ctx, c, ns, dl.Name)
	_, err := k8s.EnsureFinalizer(ctx, c, live, engine.Finalizer)
	require.NoError(t, err)
	require.NoError(t, c.Delete(ctx, live))
	return getDownload(t, ctx, c, ns, dl.Name), outputPath
}

// TestFinalizerWaitsForALiveEngineBeforeRemovingData is ruling R-6's
// ordering: while the engine's finalizer is on, the transfer may still hold
// the files, so the controller removes nothing -- however long that takes,
// since this engine is alive and will act. Once the engine drops its
// finalizer the data goes and the Download is released.
func TestFinalizerWaitsForALiveEngineBeforeRemovingData(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	newTorrentClient(t, ctx, c, ns, "qbit-live", 1, 1)
	markEngineReady(t, ctx, c, ns, "qbit-live", true)

	r := downloadctl.NewReconciler(c, k8sevents.NewFakeRecorder(10), t.TempDir())
	deleting, outputPath := assignedWithEngineFinalizer(t, ctx, c, r, ns, "qbit-live", "live-engine-dl")

	// Far past the teardown timeout: a live engine is still waited for.
	r.Now = func() time.Time { return deleting.DeletionTimestamp.Add(time.Hour) }
	res := reconcileOK(t, r, ns, deleting.Name)
	assert.Positive(t, res.RequeueAfter)
	assert.FileExists(t, outputPath, "no data may be removed while the engine's finalizer is on")
	held := getDownload(t, ctx, c, ns, deleting.Name)
	assert.Contains(t, held.Finalizers, engine.Finalizer)

	// The engine removes its transfer and drops its finalizer.
	_, err := k8s.RemoveFinalizer(ctx, c, held, engine.Finalizer)
	require.NoError(t, err)
	reconcileOK(t, r, ns, deleting.Name)

	_, statErr := os.Stat(outputPath)
	assert.True(t, os.IsNotExist(statErr), "the data goes once the engine has let go of it")
	err = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: deleting.Name}, &downloadv1alpha1.Download{})
	assert.True(t, apierrors.IsNotFound(err))
}

// TestFinalizerReleasesAGoneEngineAfterTheTimeout covers each way an engine
// can be gone. Each is waited for until the teardown timeout has passed
// since the deletion -- it may only be restarting -- and then released on
// its behalf, with a Warning Event, so deleting a Download never wedges on
// a DownloadClient that no longer exists.
func TestFinalizerReleasesAGoneEngineAfterTheTimeout(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// Each case gets its own namespace: the controller picks among every
	// enabled DownloadClient in the Download's namespace, so a sibling
	// case's client would otherwise win the pick.
	cases := []struct {
		name string
		gone func(t *testing.T, ns, clientName string)
	}{
		{"engine not ready", func(t *testing.T, ns, clientName string) {
			markEngineReady(t, ctx, c, ns, clientName, false)
		}},
		{"client deleted", func(t *testing.T, ns, clientName string) {
			var dc downloadv1alpha1.DownloadClient
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: clientName}, &dc))
			require.NoError(t, c.Delete(ctx, &dc))
		}},
		{"ordinal scaled away", func(t *testing.T, ns, clientName string) {
			// The Download sits on ordinal 0; a client whose replica count
			// the ordinal is no longer below has no pod for it. spec.replicas
			// has a minimum of 1, so model the scale-down by an engine pinned
			// past it instead.
			var dl downloadv1alpha1.DownloadList
			require.NoError(t, c.List(ctx, &dl, client.InNamespace(ns), client.MatchingLabels{downloadv1alpha1.LabelClient: clientName}))
			require.Len(t, dl.Items, 1)
			d := &dl.Items[0]
			require.NoError(t, patchEngine(ctx, c, d, clientName+"-3"))
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientName := "qbit-gone-" + string(rune('a'+i))
			ns := "teardown-" + string(rune('a'+i))
			require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
			newTorrentClient(t, ctx, c, ns, clientName, 1, 1)
			markEngineReady(t, ctx, c, ns, clientName, true)
			recorder := k8sevents.NewFakeRecorder(10)
			r := downloadctl.NewReconciler(c, recorder, t.TempDir())
			deleting, outputPath := assignedWithEngineFinalizer(t, ctx, c, r, ns, clientName, clientName+"-dl")
			tc.gone(t, ns, clientName)

			r.Now = func() time.Time { return deleting.DeletionTimestamp.Add(time.Minute) }
			res := reconcileOK(t, r, ns, deleting.Name)
			assert.Positive(t, res.RequeueAfter, "a gone engine is still waited for until the timeout")
			assert.FileExists(t, outputPath)
			assert.Contains(t, getDownload(t, ctx, c, ns, deleting.Name).Finalizers, engine.Finalizer)

			r.Now = func() time.Time {
				return deleting.DeletionTimestamp.Add(downloadctl.DefaultEngineTeardownTimeout + time.Second)
			}
			reconcileOK(t, r, ns, deleting.Name)
			_, statErr := os.Stat(outputPath)
			assert.True(t, os.IsNotExist(statErr), "past the timeout the controller removes the data itself")
			err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: deleting.Name}, &downloadv1alpha1.Download{})
			assert.True(t, apierrors.IsNotFound(err), "and releases the engine finalizer on the engine's behalf")

			var recorded []string
			for len(recorder.Events) > 0 {
				recorded = append(recorded, <-recorder.Events)
			}
			found := false
			for _, ev := range recorded {
				if strings.HasPrefix(ev, "Warning "+downloadctl.ReasonEngineGone) {
					found = true
				}
			}
			assert.True(t, found, "releasing a gone engine's finalizer must record an EngineGone Warning; got %q", recorded)
		})
	}
}
