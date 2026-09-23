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
func TestEveryCommandAppliesUMASK(t *testing.T) {
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })

	t.Setenv(umaskEnv, "002")
	if _, err := execute(t, "version"); err != nil {
		t.Fatalf("clustarr version: %v", err)
	}
	path := filepath.Join(t.TempDir(), "probe")
	require.NoError(t, os.WriteFile(path, nil, 0o666))
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o664), st.Mode().Perm(),
		"a file created after `clustarr` started with UMASK=002 is not group-writable: the umask was not applied")

	t.Setenv(umaskEnv, "nonsense")
	_, err = execute(t, "version")
	require.ErrorContains(t, err, "UMASK", "an unparseable UMASK must stop the process, not fall back silently")
}

func TestParseUmask(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
		bad  bool
	}{
		{in: "", ok: false},
		{in: "  ", ok: false},
		{in: "002", want: 0o002, ok: true},
		{in: "0002", want: 0o002, ok: true},
		{in: "0o027", want: 0o027, ok: true},
		{in: "22", want: 0o022, ok: true},
		{in: "777", want: 0o777, ok: true},
		{in: "1000", bad: true},
		{in: "008", bad: true},
		{in: "-1", bad: true},
		{in: "u=rwx", bad: true},
	} {
		got, ok, err := parseUmask(tc.in)
		if tc.bad {
			require.Error(t, err, "parseUmask(%q)", tc.in)
			continue
		}
		require.NoError(t, err, "parseUmask(%q)", tc.in)
		require.Equal(t, tc.ok, ok, "parseUmask(%q) ok", tc.in)
		require.Equal(t, tc.want, got, "parseUmask(%q)", tc.in)
	}
}
