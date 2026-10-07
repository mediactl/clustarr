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

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

// TestEveryCommandAppliesUMASK is design §11's "UMASK 002 in every
// media-touching pod", which until X14 nothing in the binary applied: the
// manifests set $UMASK and every service ignored it, so files were created
// under the image's default 022 and another service in the shared fsGroup
// could not rewrite them. The root command applies it before any
// subcommand runs, so it is proven here through a real file create -- the
// only observable that matters -- rather than by reading the mask back.
//
// The umask is process state, so the test restores the one it found.
//
// pkg/fsops.ParseUmask itself is tested in pkg/fsops/umask_test.go; this
// test is left here because it exercises the root command's
// PersistentPreRunE wiring to fsops.ApplyUmaskFromEnv, not the parser.
func TestEveryCommandAppliesUMASK(t *testing.T) {
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })

	t.Setenv(fsops.UmaskEnv, "002")
	if _, err := execute(t, "version"); err != nil {
		t.Fatalf("clustarr version: %v", err)
	}
	path := filepath.Join(t.TempDir(), "probe")
	require.NoError(t, os.WriteFile(path, nil, 0o666))
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o664), st.Mode().Perm(),
		"a file created after `clustarr` started with UMASK=002 is not group-writable: the umask was not applied")

	t.Setenv(fsops.UmaskEnv, "nonsense")
	_, err = execute(t, "version")
	require.ErrorContains(t, err, "UMASK", "an unparseable UMASK must stop the process, not fall back silently")
}
