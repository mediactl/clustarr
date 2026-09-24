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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	captionarr "github.com/mediactl/clustarr/app/caption"
	catalogarr "github.com/mediactl/clustarr/app/catalog"
	grabarr "github.com/mediactl/clustarr/app/grab"
	importarr "github.com/mediactl/clustarr/app/import"
	indexarr "github.com/mediactl/clustarr/app/indexer"
	squasharr "github.com/mediactl/clustarr/app/squash"
	"github.com/mediactl/clustarr/ui"
)

// wantDSN is the Postgres DSN both cases in TestIndexDSNFlagReachesIndexerOptions
// pass on the command line. It is never dialled: the stubbed entrypoint never
// calls indexarr.Run.
const wantDSN = "postgres://clustarr:secret@postgres.example:5432/clustarr?sslmode=disable"

// TestIndexDSNFlagReachesIndexerOptions is A2's flag-wiring test, mirroring
// ui_options_wiring_test.go's approach: execute the real command tree with
// every service entrypoint stubbed, and assert on the indexer.Options the
// stub actually received, rather than re-parsing argv by hand.
//
// It covers both places §A.3's --index-dsn is bound: `clustarr indexarr`
// (services.go) and `clustarr all` (all.go, the dev entry point) -- A2's
// checklist calls out both by name, because indexarr's other dev options
// (facade address, Cardigann bundle) are wired into `all` only through
// $CLUSTARR_* env defaults with no flag of their own, and --index-dsn is
// deliberately NOT that: it is a first-class flag on both commands so a
// developer can point `clustarr all` at a shared Postgres index without
// exporting an environment variable.
func TestIndexDSNFlagReachesIndexerOptions(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)
	// Clearing the environment variable makes the assertion about the FLAG
	// reaching Options, not about the env default doing so -- the two are
	// easy to conflate since the flag's own default reads the same variable.
	t.Setenv(indexDSNEnv, "")

	for _, argv := range [][]string{
		{"indexarr", "--index-dsn", wantDSN},
		{"all", "--index-dsn", wantDSN, "--ui-auth-mode", "anonymous"},
	} {
		t.Run("clustarr "+argv[0], func(t *testing.T) {
			got := captureIndexarrOptions(t, argv...)
			require.Equal(t, wantDSN, got.IndexDSN,
				"clustarr %v did not carry --index-dsn through to indexer.Options.IndexDSN", argv)
		})
	}
}

// TestIndexDSNEnvDefaultsBothCommands proves $CLUSTARR_INDEX_DSN is the
// flag's default on both commands too, the way indexPathEnv already is for
// --index-path.
func TestIndexDSNEnvDefaultsBothCommands(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv(indexDSNEnv, wantDSN)

	for _, argv := range [][]string{
		{"indexarr"},
		{"all", "--ui-auth-mode", "anonymous"},
	} {
		t.Run("clustarr "+argv[0], func(t *testing.T) {
			got := captureIndexarrOptions(t, argv...)
			require.Equal(t, wantDSN, got.IndexDSN,
				"clustarr %v did not default --index-dsn from $%s", argv, indexDSNEnv)
		})
	}
}

// TestIndexDSNEmptyKeepsSQLite proves the zero value -- no flag, no env --
// leaves IndexDSN empty on both commands, so indexarr.Run's open-path pivot
// takes the SQLite branch (spec §A.3: "empty keeps SQLite... the default").
func TestIndexDSNEmptyKeepsSQLite(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv(indexDSNEnv, "")

	for _, argv := range [][]string{
		{"indexarr"},
		{"all", "--ui-auth-mode", "anonymous"},
	} {
		t.Run("clustarr "+argv[0], func(t *testing.T) {
			got := captureIndexarrOptions(t, argv...)
			require.Empty(t, got.IndexDSN, "clustarr %v set IndexDSN with neither --index-dsn nor $%s given",
				argv, indexDSNEnv)
			require.NotEmpty(t, got.IndexPath, "IndexPath must still default when IndexDSN is empty")
		})
	}
}

// captureIndexarrOptions executes argv with every service entrypoint stubbed
// (the `all` command starts all seven; `indexarr` alone only ever calls its
// own) and returns the indexer.Options runIndexarr was called with.
func captureIndexarrOptions(t *testing.T, argv ...string) indexarr.Options {
	t.Helper()

	catalog, index, grab, squash, caption, importa, uiRun :=
		runCatalogarr, runIndexarr, runGrabarr, runSquasharr, runCaptionarr, runImportarr, runUI
	t.Cleanup(func() {
		runCatalogarr, runIndexarr, runGrabarr, runSquasharr, runCaptionarr, runImportarr, runUI =
			catalog, index, grab, squash, caption, importa, uiRun
	})

	var got indexarr.Options
	var called bool
	runCatalogarr = func(context.Context, catalogarr.Options) error { return nil }
	runIndexarr = func(_ context.Context, o indexarr.Options) error {
		got, called = o, true
		return nil
	}
	runGrabarr = func(context.Context, grabarr.Options) error { return nil }
	runSquasharr = func(context.Context, squasharr.Options) error { return nil }
	runCaptionarr = func(context.Context, captionarr.Options) error { return nil }
	runImportarr = func(context.Context, importarr.Options) error { return nil }
	runUI = func(context.Context, ui.Options) error { return nil }

	_, err := execute(t, argv...)
	require.NoError(t, err, "clustarr %v", argv)
	require.True(t, called, "clustarr %v never called runIndexarr", argv)
	return got
}
