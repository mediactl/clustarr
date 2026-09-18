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

package subtitles_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestWriterWritesAtomicallyAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Movie.en.srt")

	w := subtitles.NewWriter()
	require.NoError(t, w.Write(context.Background(), path, []byte("first")))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "first", string(got))

	// No leftover temp file in the directory.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	require.NoError(t, w.Write(context.Background(), path, []byte("second, longer content")))
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second, longer content", string(got))
}

func TestWriterFailsCleanlyOnAMissingDirectory(t *testing.T) {
	w := subtitles.NewWriter()
	err := w.Write(context.Background(), filepath.Join(t.TempDir(), "no-such-dir", "Movie.en.srt"), []byte("x"))
	assert.Error(t, err)
}
