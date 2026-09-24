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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
)

// TestEngineFinalizerOrdersTheUsenetTeardown is ruling R-6 for this engine:
// the finalizer goes on with the first reconcile, before the transfer; a
// deletion whose Remove fails keeps it on (so the Download controller,
// which waits for it, never deletes files a live job holds); and once
// Remove succeeds it comes off and the Download is gone.
func TestEngineFinalizerOrdersTheUsenetTeardown(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	key := types.NamespacedName{Namespace: "default", Name: "movie-fin"}

	url := nzbFixtureServer(t, []byte("<nzb>fixture-payload</nzb>"))
	dl := newUsenetDownload(key.Name, "sabnzbd-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	reconcileEngine(t, r, key.Namespace, key.Name)
	require.Len(t, fc.addCalls, 1)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &got))
	require.Equal(t, []string{engine.Finalizer}, got.Finalizers, "the engine finalizer goes on with the first Add")
	id := got.Status.DownloadID
	require.NotEmpty(t, id)

	require.NoError(t, c.Delete(ctx, &got))
	fc.removeErr = errors.New("provider socket wedged")
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.Error(t, err)
	require.NoError(t, c.Get(ctx, key, &got), "a failed Remove must leave the engine finalizer holding the object")
	assert.Equal(t, []string{engine.Finalizer}, got.Finalizers)

	fc.removeErr = nil
	reconcileEngine(t, r, key.Namespace, key.Name)
	err = c.Get(ctx, key, &downloadv1alpha1.Download{})
	assert.True(t, apierrors.IsNotFound(err), "Remove done, finalizer dropped, nothing else holding it")
	require.NotEmpty(t, fc.removeCalls)
	last := fc.removeCalls[len(fc.removeCalls)-1]
	assert.Equal(t, id, last.id)
	assert.True(t, last.deleteData, "spec.removeDataOnDelete defaults to true")
}

// TestEngineFinalizerIsDroppedWhenNoTransferWasEverAdded: the finalizer is
// added before the Add, so a Download whose payload never resolved still
// carries it and must release it on deletion.
func TestEngineFinalizerIsDroppedWhenNoTransferWasEverAdded(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	key := types.NamespacedName{Namespace: "default", Name: "movie-noadd"}

	url := nzbFixtureServer(t, []byte("<nzb/>"))
	dl := newUsenetDownload(key.Name, "sabnzbd-0", url)
	require.NoError(t, c.Create(ctx, dl))

	fc := newFakeDownloadClient()
	fc.addErr = errors.New("nzb rejected")
	r := &usenetengine.Reconciler{Client: c, Download: fc, Resolver: &usenetengine.Resolver{}, Engine: "sabnzbd-0"}
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.Error(t, err)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &got))
	require.Equal(t, []string{engine.Finalizer}, got.Finalizers)
	require.Empty(t, got.Status.DownloadID)

	require.NoError(t, c.Delete(ctx, &got))
	reconcileEngine(t, r, key.Namespace, key.Name)
	err = c.Get(ctx, key, &downloadv1alpha1.Download{})
	assert.True(t, apierrors.IsNotFound(err))
	assert.Empty(t, fc.removeCalls)
}
