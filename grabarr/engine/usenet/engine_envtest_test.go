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
// apiserver conflict: pkg/k8s.PatchStatus forces ownership unconditionally,
// so an over-claim is silent everywhere except metadata.managedFields (CLAUDE.md;
// grabarr/controller/downloadclient/managedfields_envtest_test.go is the
// pattern this file copies). This file reads managedFields directly wherever
// that matters.
package usenet_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/grabarr/engine/usenet"
	grabarrstatus "github.com/mediactl/clustarr/grabarr/status"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

// newUsenetDownload builds a usenet-shaped Download: no infoHash, matching the
// genuine shape pkg/crdcheck/download_cel_test.go guards (c5e86d5) rather
// than the torrent-shaped workaround older fixtures carried.
func newUsenetDownload(name, engine, nzbURL string) *downloadv1alpha1.Download {
	return &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{downloadv1alpha1.LabelEngine: engine},
		},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Source:   downloadv1alpha1.DownloadSource{NZBURL: &nzbURL},
			Release: commonv1alpha1.ReleaseInfo{
				GUID:       "guid-" + name,
				IndexerRef: "nzbgeek",
				Title:      "Fixture.Release.1080p",
				Protocol:   commonv1alpha1.ProtocolUsenet,
			},
			Target: commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "fixture-movie"},
		},
	}
}

func reconcileEngine(t *testing.T, r *usenetengine.Reconciler, ns, name string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
	return res
}

func nzbFixtureServer(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

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

func TestReconcileAddsNewDownloadAndReportsTelemetryUnderEngineManager(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("<nzb>fixture-payload</nzb>"))
	dl := newUsenetDownload("movie-1", "sabnzbd-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	r := &usenetengine.Reconciler{
		Client:   c,
		Download: fc,
		Resolver: &usenetengine.Resolver{},
		Engine:   "sabnzbd-0",
	}
	res := reconcileEngine(t, r, "default", "movie-1")
	assert.Positive(t, res.RequeueAfter, "a non-terminal transfer must be polled again")

	require.Len(t, fc.addCalls, 1)
	assert.Equal(t, "movie-1", fc.addCalls[0].Name)
	assert.Equal(t, []byte("<nzb>fixture-payload</nzb>"), fc.addCalls[0].Payload)
	assert.Equal(t, "movie", fc.addCalls[0].Category, "Target.Kind's own name is the category default")

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-1"}, &got))
	assert.Equal(t, "id-1", got.Status.DownloadID)

	statusManagers := managersOf(got.ManagedFields, "status")
	assert.Equal(t, map[string]bool{k8s.ManagerGrabarrEngine.String(): true}, statusManagers,
		"Download.status must be owned only by k8s.ManagerGrabarrEngine after an engine-only write")
}

func TestReconcileDoesNotReAddAnAlreadyKnownTransfer(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("unused"))
	dl := newUsenetDownload("movie-2", "sabnzbd-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	fc.setItem(download.Item{ID: "pre-existing", Status: download.StatusDownloading, TotalBytes: 1000, DownloadedBytes: 400})

	// Simulate a previous reconcile that already recorded the id.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-2"}, dl))
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarrEngine, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { ac.WithDownloadID("pre-existing") }))

	r := &usenetengine.Reconciler{
		Client:   c,
		Download: fc,
		Resolver: &usenetengine.Resolver{},
		Engine:   "sabnzbd-0",
	}
	reconcileEngine(t, r, "default", "movie-2")

	assert.Empty(t, fc.addCalls, "an already-known id must never be re-Added")

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-2"}, &got))
	assert.Equal(t, int64(400), got.Status.DownloadedBytes)
	assert.Equal(t, int64(1000), got.Status.TotalBytes)
}

func TestReconcilePausesAndResumesToMatchSpec(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("payload"))
	dl := newUsenetDownload("movie-3", "sabnzbd-0", url)
	dl.Spec.Paused = true
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	reconcileEngine(t, r, "default", "movie-3")

	// Add(Paused: true) already starts the fake item paused, so the first
	// reconcile should issue no separate Pause call.
	assert.Empty(t, fc.pauseCalls)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-3"}, dl))
	dl.Spec.Paused = false
	require.NoError(t, c.Update(ctx, dl))

	reconcileEngine(t, r, "default", "movie-3")
	assert.Len(t, fc.resumeCalls, 1)
}

func TestReconcileMarksImportedAndRemovesOnImportByDefault(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("payload"))
	dl := newUsenetDownload("movie-4", "sabnzbd-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	fc.setItem(download.Item{ID: "done-1", Status: download.StatusCompleted, CanMoveFiles: true})

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-4"}, dl))
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarrEngine, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { ac.WithDownloadID("done-1") }))

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-4"}, dl))
	importAC := downloadac.Download(dl.Name, dl.Namespace).WithStatus(
		downloadac.DownloadStatus().WithImport(downloadac.ImportState().WithState(downloadv1alpha1.ImportPhaseImported)))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerImportarr, importAC)
	require.NoError(t, err)

	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	reconcileEngine(t, r, "default", "movie-4")

	assert.Contains(t, fc.markImportedIDs, "done-1")
	require.Len(t, fc.removeCalls, 1)
	assert.Equal(t, "done-1", fc.removeCalls[0].id)
	assert.False(t, fc.removeCalls[0].deleteData, "an imported release's files may be hard-linked into the library; a usenet Remove never deletes data on import")

	// Nothing was ever added: reconcileImported must short-circuit before
	// getOrAdd, or a removed-and-imported transfer would be re-downloaded.
	assert.Empty(t, fc.addCalls)
}

func TestReconcileDoesNotRemoveDataOnImportWhenRemoveOnImportIsFalse(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("payload"))
	dl := newUsenetDownload("movie-5", "sabnzbd-0", url)
	no := false
	dl.Spec.RemoveOnImport = &no
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	fc.setItem(download.Item{ID: "done-2", Status: download.StatusCompleted})

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-5"}, dl))
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarrEngine, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { ac.WithDownloadID("done-2") }))

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-5"}, dl))
	importAC := downloadac.Download(dl.Name, dl.Namespace).WithStatus(
		downloadac.DownloadStatus().WithImport(downloadac.ImportState().WithState(downloadv1alpha1.ImportPhaseImported)))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerImportarr, importAC)
	require.NoError(t, err)

	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	reconcileEngine(t, r, "default", "movie-5")

	assert.Contains(t, fc.markImportedIDs, "done-2")
	assert.Empty(t, fc.removeCalls, "removeOnImport=false must leave the transfer in the client")
}

func TestReconcileDeletingRemovesTransferAndTouchesNoOtherManagedField(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("payload"))
	dl := newUsenetDownload("movie-6", "sabnzbd-0", url)
	dl.Finalizers = []string{"test.clustarr.io/keep"}
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	fc.setItem(download.Item{ID: "live-1", Status: download.StatusDownloading})

	// Seed BOTH managers' fields, simulating steady-state: the (not-yet-built)
	// Download controller already assigned phase/engine, and a prior engine
	// reconcile already wrote telemetry. The deleting path must release
	// neither.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-6"}, dl))
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarr, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			ac.WithPhase(downloadv1alpha1.DownloadPhaseDownloading).WithEngine("sabnzbd-0")
		}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-6"}, dl))
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarrEngine, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { ac.WithDownloadID("live-1") }))

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-6"}, dl))
	require.NoError(t, c.Delete(ctx, dl))

	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	reconcileEngine(t, r, "default", "movie-6")

	require.Len(t, fc.removeCalls, 1)
	assert.Equal(t, "live-1", fc.removeCalls[0].id)
	assert.True(t, fc.removeCalls[0].deleteData, "spec.removeDataOnDelete defaults to true")

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movie-6"}, &got))
	assert.Equal(t, downloadv1alpha1.DownloadPhaseDownloading, got.Status.Phase,
		"the deleting path must not touch ManagerGrabarr's fields at all")
	assert.Equal(t, "sabnzbd-0", got.Status.Engine)
	assert.Equal(t, "live-1", got.Status.DownloadID,
		"the deleting path makes no status write of its own -- it only calls the in-memory client")

	statusManagers := managersOf(got.ManagedFields, "status")
	assert.Equal(t, map[string]bool{k8s.ManagerGrabarr.String(): true, k8s.ManagerGrabarrEngine.String(): true}, statusManagers,
		"reconcileDeleting must add no managedFields entry of its own")
}

func TestReconcileSkipsADownloadLabelledForAnotherEngine(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("payload"))
	dl := newUsenetDownload("movie-7", "other-engine-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	res := reconcileEngine(t, r, "default", "movie-7")

	assert.Zero(t, res.RequeueAfter)
	assert.Empty(t, fc.addCalls)
}
