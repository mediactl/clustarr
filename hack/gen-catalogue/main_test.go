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
	"testing"

	"github.com/stretchr/testify/require"
)

// realCorpusDir and realManifestDir locate the actual vendored corpus and
// this generator's own curated selection manifest from a test binary
// running inside hack/gen-catalogue.
const (
	realCorpusDir   = "../../test/data/trash/docs/json"
	realManifestDir = "manifest"
	realDataDir     = "../../pkg/quality/catalogue/data/formats"
)

// TestGenerateAllReproducesTheEmbeddedCatalogueByteForByte is Step 2's
// acceptance test, expressed as a Go test rather than a one-off manual
// check: running this generator against the real vendored corpus and its
// own manifest must reproduce pkg/quality/catalogue/data/formats/*.json
// byte for byte. A failure here means either the generator drifted from
// the checked-in data (someone hand-edited data/ again, or the corpus was
// re-vendored without regenerating), or the manifest needs updating -- see
// the task report for how this was established the first time (every
// value-bearing field matched the hand-authored data exactly; only the
// hand files' inconsistent manual JSON formatting differed, which is why
// data/ was normalized to this generator's canonical shape once, as part
// of this task).
func TestGenerateAllReproducesTheEmbeddedCatalogueByteForByte(t *testing.T) {
	families, err := GenerateAll(realCorpusDir, realManifestDir)
	require.NoError(t, err)

	wantEntries, err := os.ReadDir(realDataDir)
	require.NoError(t, err)

	wantNames := make(map[string]bool, len(wantEntries))
	for _, e := range wantEntries {
		wantNames[e.Name()] = true
	}
	gotNames := make(map[string]bool, len(families))
	for name := range families {
		gotNames[name] = true
	}
	require.Equal(t, wantNames, gotNames, "generator must emit exactly the family files data/formats/ has, no more, no less")

	for name, got := range families {
		want, err := os.ReadFile(filepath.Join(realDataDir, name))
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "generated %s must be byte-identical to the checked-in file", name)
	}
}

// TestGenerateAllIsIdempotent guards against a generator that produces
// different bytes on a second run over the same inputs (e.g. from
// nondeterministic map iteration leaking into output ordering).
func TestGenerateAllIsIdempotent(t *testing.T) {
	first, err := GenerateAll(realCorpusDir, realManifestDir)
	require.NoError(t, err)
	second, err := GenerateAll(realCorpusDir, realManifestDir)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

// TestRunWritesFilesUnderOutDir exercises the CLI's run() wiring
// end-to-end into a scratch directory, independent of the byte-for-byte
// comparison above -- this is what `go run ./hack/gen-catalogue` actually
// does.
func TestRunWritesFilesUnderOutDir(t *testing.T) {
	outDir := t.TempDir()
	require.NoError(t, run(realCorpusDir, realManifestDir, outDir))

	entries, err := os.ReadDir(outDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	want, err := os.ReadFile(filepath.Join(realDataDir, "season_pack.json"))
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(outDir, "season_pack.json"))
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}
