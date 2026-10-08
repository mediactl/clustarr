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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli"
)

// wantDSN is the Postgres DSN the cases below pass. It is never dialled: the
// recording run never opens the index.
const wantDSN = "postgres://clustarr:secret@postgres.example:5432/clustarr?sslmode=disable"

// TestIndexDSNFlagReachesIndexerOptions is A2's flag-wiring test: execute the
// real command tree with a recording run and assert on the options the index
// domain would start with, rather than re-parsing argv by hand. --index-dsn
// is a first-class flag of `agent --domain=index` (spec §A.3). The old
// `clustarr all` leg has no subject: one process runs one domain (§3.10).
func TestIndexDSNFlagReachesIndexerOptions(t *testing.T) {
	// Clearing the environment variable makes the assertion about the FLAG
	// reaching the options, not about the env default doing so -- the two
	// are easy to conflate since the flag's own default reads the same
	// variable.
	t.Setenv(cli.EnvIndexDSN, "")
	got, err := parse(t, "--domain=index", "--index-dsn", wantDSN)
	require.NoError(t, err)
	require.Equal(t, wantDSN, got.IndexDSN, "--index-dsn did not reach the index domain's options")
}

// TestIndexDSNEnvDefaultsBothCommands proves $CLUSTARR_INDEX_DSN is the
// flag's default, the way $CLUSTARR_INDEX_PATH is for --index-path.
func TestIndexDSNEnvDefaultsBothCommands(t *testing.T) {
	t.Setenv(cli.EnvIndexDSN, wantDSN)
	got, err := parse(t, "--domain=index")
	require.NoError(t, err)
	require.Equal(t, wantDSN, got.IndexDSN, "--index-dsn did not default from $%s", cli.EnvIndexDSN)
}

// TestIndexDSNEmptyKeepsSQLite proves the zero value -- no flag, no env --
// leaves IndexDSN empty, so the index domain's open-path pivot takes the
// SQLite branch (spec §A.3: "empty keeps SQLite... the default"), and that
// SQLite then needs its file: an empty --index-path with no DSN is refused.
func TestIndexDSNEmptyKeepsSQLite(t *testing.T) {
	t.Setenv(cli.EnvIndexDSN, "")
	t.Setenv(cli.EnvIndexPath, "")
	got, err := parse(t, "--domain=index")
	require.NoError(t, err)
	require.Empty(t, got.IndexDSN, "IndexDSN set with neither --index-dsn nor $%s given", cli.EnvIndexDSN)
	require.NotEmpty(t, got.IndexPath, "IndexPath must still default when IndexDSN is empty")

	_, err = parse(t, "--domain=index", "--index-path=")
	require.ErrorContains(t, err, "--index-path")
}
