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

package embedded_test

import (
	"io/fs"
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/indexer/bundle/embedded"
	"github.com/mediactl/clustarr/pkg/cardigann"
)

// TestTheEmbeddedArchiveIsAFlatDirectoryOfDefinitions pins the archive's
// shape: cardigann.LoadBundle lists only the root of the fs.FS it is given,
// so a definition packed under a subdirectory would be silently skipped.
func TestTheEmbeddedArchiveIsAFlatDirectoryOfDefinitions(t *testing.T) {
	fsys, err := embedded.FS()
	require.NoError(t, err)

	entries, err := fs.ReadDir(fsys, ".")
	require.NoError(t, err)
	require.Greater(t, len(entries), 500, "the embedded corpus shrank to %d entries", len(entries))
	for _, e := range entries {
		require.False(t, e.IsDir(), "the archive must be flat; %s is a directory", e.Name())
		require.Contains(t, []string{".yml", ".yaml"}, path.Ext(e.Name()), "unexpected entry %s", e.Name())
	}

	data, err := fs.ReadFile(fsys, "1337x.yml")
	require.NoError(t, err, "a known definition must be readable through the zip reader")
	require.Contains(t, string(data), "id: 1337x")
}

// TestEveryEmbeddedDefinitionLoads runs the whole corpus through the loader
// indexarr uses, so a definition the engine refuses is visible here rather
// than as a startup warning on a cluster.
func TestEveryEmbeddedDefinitionLoads(t *testing.T) {
	fsys, err := embedded.FS()
	require.NoError(t, err)

	defs, issues, err := cardigann.LoadBundle(fsys)
	require.NoError(t, err)
	for _, issue := range issues {
		t.Logf("refused: %v", issue)
	}
	entries, err := fs.ReadDir(fsys, ".")
	require.NoError(t, err)
	require.Len(t, defs, len(entries)-len(issues))
	// A handful of upstream definitions use features the engine does not
	// parse; more than a few percent refused means the engine regressed.
	require.LessOrEqual(t, len(issues), len(entries)/50,
		"%d of %d embedded definitions were refused", len(issues), len(entries))
}
