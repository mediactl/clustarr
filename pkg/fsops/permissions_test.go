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

package fsops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// cancelAfterNChecks wraps a real context so its Err() reports cancelled
// starting from call number n+1: calls 1..n observe the embedded
// context's real (uncancelled) state, call n+1 onward always report
// context.Canceled. This proves a function checks ctx.Err() at each of
// several points (once per WalkDir/ReadDir entry, once per copy chunk)
// rather than only once at the top, without racy wall-clock timing or
// needing to inject a fake per-entry hook into filepath.WalkDir itself.
type cancelAfterNChecks struct {
	context.Context
	n     int
	calls int
}

func (c *cancelAfterNChecks) Err() error {
	c.calls++
	if c.calls > c.n {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestSetPermissionsChmodsRecursively(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "file.mkv"), []byte("x"), 0o600))

	require.NoError(t, SetPermissions(context.Background(), dir, 0o664, 0o775, -1, -1))

	fi, err := os.Stat(filepath.Join(dir, "sub"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o775), fi.Mode().Perm())

	fi, err = os.Stat(filepath.Join(dir, "sub", "file.mkv"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o664), fi.Mode().Perm())
}

func TestSetPermissionsSkipsChownWhenNotRoot(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 1000 }

	var chownCalls int
	oldChown := chownFunc
	t.Cleanup(func() { chownFunc = oldChown })
	chownFunc = func(string, int, int) error { chownCalls++; return nil }

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.mkv"), []byte("x"), 0o600))

	require.NoError(t, SetPermissions(context.Background(), dir, 0o664, 0o775, 1000, 100))
	require.Zero(t, chownCalls, "chown must be a no-op when not root")
}

func TestSetPermissionsChownsAsRoot(t *testing.T) {
	old := geteuid
	t.Cleanup(func() { geteuid = old })
	geteuid = func() int { return 0 }

	type call struct {
		path     string
		uid, gid int
	}
	var calls []call
	oldChown := chownFunc
	t.Cleanup(func() { chownFunc = oldChown })
	chownFunc = func(p string, uid, gid int) error {
		calls = append(calls, call{p, uid, gid})
		return nil
	}

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.mkv"), []byte("x"), 0o600))

	require.NoError(t, SetPermissions(context.Background(), dir, 0o664, 0o775, -1, 100))
	require.Len(t, calls, 2, "root dir + file.mkv")
	for _, c := range calls {
		require.Equal(t, -1, c.uid)
		require.Equal(t, 100, c.gid)
	}
}

func TestSetPermissionsReturnsQuicklyOnAPreCancelledContext(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.mkv"), []byte("x"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := SetPermissions(ctx, dir, 0o664, 0o775, -1, -1)
	require.ErrorIs(t, err, context.Canceled)

	fi, statErr := os.Stat(filepath.Join(dir, "file.mkv"))
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "a pre-cancelled context must do no work")
}

func TestSetPermissionsCancellationAfterTheFirstEntryStopsTheWalk(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.mkv"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.mkv"), []byte("x"), 0o600))

	// filepath.WalkDir visits dir itself first (entry 1, the walk root),
	// then "a.mkv" (entry 2, lexically first child), then "b.mkv" (entry
	// 3). n=2 lets the top-of-function check (call 1) and dir's own
	// per-entry check (call 2) both see an uncancelled context, so dir
	// gets chmod'd; the third check, for a.mkv, reports cancelled and
	// stops the walk before a.mkv or b.mkv are touched.
	ctx := &cancelAfterNChecks{Context: context.Background(), n: 2}

	err := SetPermissions(ctx, dir, 0o664, 0o775, -1, -1)
	require.ErrorIs(t, err, context.Canceled)

	fi, statErr := os.Stat(dir)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0o775), fi.Mode().Perm(), "the walk root (first entry) must have been processed")

	fi, statErr = os.Stat(filepath.Join(dir, "a.mkv"))
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "a.mkv must be untouched -- cancellation stopped the walk before it")

	fi, statErr = os.Stat(filepath.Join(dir, "b.mkv"))
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "b.mkv must be untouched -- never reached")
}
