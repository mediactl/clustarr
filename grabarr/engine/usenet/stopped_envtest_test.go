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

package usenet_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/grabarr/engine/usenet"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Gap fix Y2, the usenet engine's half: a job's failure reaches status as
// the engine's report; once the controller's verdict is on the object the
// engine removes the job -- scratch and published data -- and, above all,
// does not re-add it, which getOrAdd would do for an id the client no
// longer knows.
func TestReconcileReportsAFailureThenRemovesTheJobOnceStopped(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("<nzb>missing</nzb>"))
	dl := newUsenetDownload("movie-missing", "sabnzbd-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	reconcileEngine(t, r, "default", "movie-missing")
	require.Len(t, fc.addCalls, 1)

	key := types.NamespacedName{Namespace: "default", Name: "movie-missing"}
	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &got))
	id := got.Status.DownloadID
	require.NotEmpty(t, id)

	it, err := fc.Get(ctx, id)
	require.NoError(t, err)
	it.Status = download.StatusFailed
	it.FailureReason = downloadv1alpha1.DownloadFailureMissingArticles
	it.Stage = downloadv1alpha1.DownloadStageDone
	fc.setItem(it)

	res := reconcileEngine(t, r, "default", "movie-missing")
	assert.Zero(t, res.RequeueAfter, "a failed job is terminal")
	require.NoError(t, c.Get(ctx, key, &got))
	assert.Equal(t, downloadv1alpha1.DownloadFailureMissingArticles, got.Status.EngineFailureReason)
	assert.Empty(t, fc.removeCalls, "the engine must not act on its own observation")

	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadac.Download(dl.Name, dl.Namespace).WithStatus(
		downloadac.DownloadStatus().
			WithPhase(downloadv1alpha1.DownloadPhaseBlocklisted).
			WithFailureReason(downloadv1alpha1.DownloadFailureMissingArticles)))
	require.NoError(t, err)

	reconcileEngine(t, r, "default", "movie-missing")
	require.Len(t, fc.removeCalls, 1)
	assert.Equal(t, id, fc.removeCalls[0].id)
	assert.True(t, fc.removeCalls[0].deleteData, "removeDataOnDelete defaults true")

	// The client no longer knows the id. A re-run must not re-add it.
	reconcileEngine(t, r, "default", "movie-missing")
	assert.Len(t, fc.addCalls, 1, "a stopped Download's removed job was re-added")

	require.NoError(t, c.Get(ctx, key, &got))
	assert.Equal(t, downloadv1alpha1.DownloadFailureMissingArticles, got.Status.EngineFailureReason,
		"the engine's report outlives the job")
}

// A Download labelled blocklisted before its engine reached it is never
// fetched.
func TestReconcileNeverAddsABlocklistLabelledDownload(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	url := nzbFixtureServer(t, []byte("<nzb>labelled</nzb>"))
	dl := newUsenetDownload("movie-labelled", "sabnzbd-0", url)
	dl.Labels[downloadv1alpha1.LabelBlocklisted] = downloadv1alpha1.LabelBlocklistedValue
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	reconcileEngine(t, r, "default", "movie-labelled")
	assert.Empty(t, fc.addCalls)
	assert.Empty(t, fc.removeCalls)
}
