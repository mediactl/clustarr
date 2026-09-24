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

package torrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// TestEngineNotReadyUntilReAttachRuns is [Engine]'s own half of R4: an engine
// that has not called ReAttach must report not-ready, both on Ready() and on
// the healthz.Checker a caller wires into readyz.
func TestEngineNotReadyUntilReAttachRuns(t *testing.T) {
	e := &Engine{Client: newFakeClient(), StateDir: t.TempDir()}

	assert.False(t, e.Ready(), "a freshly constructed engine must not be ready")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	assert.Error(t, e.HealthzCheck(req), "the healthz check must fail before re-attach")

	_, err := e.ReAttach(context.Background())
	require.NoError(t, err)

	assert.True(t, e.Ready(), "ready after ReAttach completes")
	assert.NoError(t, e.HealthzCheck(req), "the healthz check must pass once ready")
}

// TestReAttachAddsEveryPersistedTransfer proves ReAttach actually restores
// state into the client, not just flips a flag.
func TestReAttachAddsEveryPersistedTransfer(t *testing.T) {
	dir := t.TempDir()
	oneID := fakeID(download.AddRequest{Magnet: "magnet:?xt=urn:btih:one"})
	twoID := fakeID(download.AddRequest{Magnet: "magnet:?xt=urn:btih:two"})
	require.NoError(t, saveDescriptor(dir, oneID, nil,
		descriptor{Name: "one", Magnet: "magnet:?xt=urn:btih:one"}))
	require.NoError(t, saveDescriptor(dir, twoID, nil,
		descriptor{Name: "two", Magnet: "magnet:?xt=urn:btih:two", Paused: true}))

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: dir}

	res, err := e.ReAttach(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, res.Attached)
	assert.Equal(t, 0, res.Failed)
	assert.Equal(t, 2, fc.addCallCount())

	// The paused one must come back paused (state.go's own claim: Paused
	// always comes from the descriptor's CURRENT value).
	item, err := fc.Get(context.Background(), twoID)
	require.NoError(t, err)
	assert.Equal(t, download.StatusPaused, item.Status)
}

// TestReAttachSkipsACorruptDescriptorButStillReadiesUp proves one damaged
// entry does not prevent the engine from ever becoming ready -- a stuck
// re-attach would leave the pod permanently unready, worse than losing one
// transfer's re-attach.
func TestReAttachSkipsACorruptDescriptorButStillReadiesUp(t *testing.T) {
	dir := t.TempDir()
	goodID := fakeID(download.AddRequest{Magnet: "magnet:?xt=urn:btih:good"})
	require.NoError(t, saveDescriptor(dir, goodID, nil, descriptor{Name: "good", Magnet: "magnet:?xt=urn:btih:good"}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o640))

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: dir}

	res, err := e.ReAttach(context.Background())
	require.NoError(t, err, "one corrupt descriptor must not fail ReAttach outright")
	assert.Equal(t, 1, res.Attached)
	assert.Equal(t, 1, res.Failed)
	assert.True(t, e.Ready(), "the engine must still become ready despite the corrupt entry")
}
