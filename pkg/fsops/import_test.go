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

package fsops_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestImportDispatchesByMode(t *testing.T) {
	tests := []struct {
		name string
		mode fsops.ImportMode
	}{
		{"hardlink", fsops.ImportHardlink},
		{"copy", fsops.ImportCopy},
		{"move", fsops.ImportMove},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src.mkv")
			dst := filepath.Join(dir, "nested", "dst.mkv")
			require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

			err := fsops.Import(context.Background(), src, dst, tt.mode)
			require.NoError(t, err)

			got, err := os.ReadFile(dst)
			require.NoError(t, err)
			require.Equal(t, "payload", string(got))

			_, statErr := os.Stat(src)
			if tt.mode == fsops.ImportMove {
				require.True(t, os.IsNotExist(statErr), "move must remove the source")
			} else {
				require.NoError(t, statErr, "%s must preserve the source", tt.name)
			}
		})
	}
}

func TestImportCopyStopsMidFileOnCancellationAndRemovesPartial(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	// Large enough to span several of io.Copy's default ~32KiB chunks,
	// so a cancellation that fires after the first chunk genuinely stops
	// the transfer mid-file rather than before it starts.
	payload := bytes.Repeat([]byte{'x'}, 5*1024*1024)
	require.NoError(t, os.WriteFile(src, payload, 0o664))

	// Call 1 lets the first chunk read through copyFile's ctx-checking
	// reader proceed; call 2 onward reports cancelled.
	ctx := &cancelAfterNChecks{Context: context.Background(), n: 1}

	err := fsops.Import(ctx, src, dst, fsops.ImportCopy)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.GreaterOrEqual(t, ctx.calls, 2,
		"copyFile must check ctx.Err() more than once -- between chunks, not only at the start")

	_, statErr := os.Stat(dst)
	require.True(t, os.IsNotExist(statErr), "dst must not exist after a mid-copy cancellation")
	_, statErr = os.Stat(dst + ".partial")
	require.True(t, os.IsNotExist(statErr), ".partial must be removed, not left behind")
}
