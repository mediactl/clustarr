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

package rescan_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/import/worker/rescan"
)

// walkOrderLess must agree with the order filepath.WalkDir really visits a
// tree in, or a resumed walk would pass over files it never counted (or
// count some twice). It is checked against a real walk, including the case
// a plain string comparison gets wrong: "a/b" is visited before "a-c".
func TestWalkOrderLessMatchesWalkDir(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a/b", "a-c", "a/b/z.mkv", "a/c.mkv", "a-c/x.mkv", "B.mkv", "a.mkv", "a/b.mkv", "ab/y.mkv"} {
		p := filepath.Join(root, rel)
		if filepath.Ext(rel) == "" {
			require.NoError(t, os.MkdirAll(p, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
	var visited []string
	require.NoError(t, filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		visited = append(visited, p)
		return err
	}))
	for i := range visited {
		for j := range visited {
			assert.Equalf(t, i < j, rescan.WalkOrderLess(visited[i], visited[j]),
				"%s before %s", visited[i], visited[j])
		}
	}
	assert.False(t, "a/b" < "a-c", "setup: the plain string order disagrees here")
}

// A reason is clamped to the CRD's MaxLength, which counts characters, on a
// character boundary.
func TestClampRunes(t *testing.T) {
	assert.Equal(t, "abc", rescan.ClampRunes("abc", 3))
	assert.Equal(t, "ab", rescan.ClampRunes("abc", 2))
	assert.Equal(t, "日本", rescan.ClampRunes("日本語", 2))
	assert.Equal(t, strings.Repeat("é", 256), rescan.ClampRunes(strings.Repeat("é", 300), 256))
}
