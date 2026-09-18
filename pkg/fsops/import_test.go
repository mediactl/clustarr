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
