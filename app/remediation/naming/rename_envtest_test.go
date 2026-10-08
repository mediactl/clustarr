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

package naming_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/remediation/remediationtest"
)

// TestTheLoopRenamesOnThePassThatMakesTheFileRenameable: the rename actuator
// runs after every pass against the status that pass applied, so the loop
// renames on the pass whose apply makes the file renameable (naming stale,
// probe current, Ready) under a RootFolder that sets renameFiles; the spec
// apply wakes the file, and that pass reports it current. A collision is
// tried once per proposal, not on every pass.
func TestTheLoopRenamesOnThePassThatMakesTheFileRenameable(t *testing.T) {
	f := newNamedFile(t, "loop-rename", withRenameFiles(true))
	h := newHarness(t)
	r := h.loop(t, f, true)
	reconcileOnce(t, r, f.key)
	h.Answer(t, f.uid(t), fakeMediaInfo())
	reconcileOnce(t, r, f.key) // the probe lands; naming's Gather was gated
	require.Equal(t, f.path, f.get(t).Spec.Path, "setup: nothing to rename before naming proposes")
	reconcileOnce(t, r, f.key) // naming proposes; the actuator renames after this pass's apply
	got := f.get(t)
	require.NotNil(t, got.Status.Naming)
	require.Equal(t, filepath.Dir(f.path), filepath.Dir(got.Status.Naming.ExpectedPath), "setup: the proposal is in the file's folder")
	assert.Equal(t, got.Status.Naming.ExpectedPath, got.Spec.Path, "renamed in the pass that proposed it")
	assert.FileExists(t, got.Spec.Path)
	assert.NoFileExists(t, f.path)
	assert.Len(t, filterPrefix(remediationtest.Drain(h.renameRec), "Normal Renamed"), 1)
	sent := len(h.Sent())
	reconcileOnce(t, r, f.key) // the spec apply's wake: a path-only probe incorporation, naming current
	got = f.get(t)
	assert.True(t, got.Status.Naming.Current)
	assert.True(t, got.ProbeCurrent())
	assert.Len(t, h.Sent(), sent, "a rename probes nothing")

	g := newNamedFile(t, "loop-rename-collision", withRenameFiles(false))
	r = h.loop(t, g, true)
	probeAndName(t, h, r, g, fakeMediaInfo())
	proposal := g.get(t).Status.Naming.ExpectedPath
	require.NotEmpty(t, proposal, "setup: naming proposed nothing")
	assert.FileExists(t, g.path, "renameFiles off: nothing moved")
	require.NoError(t, os.WriteFile(proposal, []byte("in the way"), 0o644))
	setRenameFiles(t, g, true)
	reconcileOnce(t, r, g.key)
	reconcileOnce(t, r, g.key)
	assert.Len(t, filterPrefix(remediationtest.Drain(h.renameRec), "Warning Collision"), 1,
		"a collision was retried on a pass that changed nothing")
	assert.FileExists(t, g.path)
	b, err := os.ReadFile(proposal)
	require.NoError(t, err)
	assert.Equal(t, "in the way", string(b), "the file in the way is untouched")
}
