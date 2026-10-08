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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/internal/cli/agent"
)

// wantDSN is the Postgres DSN the cases below pass. It is never dialled: the
// recording run never opens the index.
const wantDSN = "postgres://clustarr:secret@postgres.example:5432/clustarr?sslmode=disable"

// clearIndexEnv unsets the index domain's environment defaults, so a case
// asserts the flags' compiled-in defaults rather than the caller's shell.
func clearIndexEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{cli.EnvIndexPath, cli.EnvIndexDSN, cli.EnvFacadeBindAddress, cli.EnvFacadeAPIKeySecret} {
		t.Setenv(k, "")
	}
}

// TestIndexDomainOptions: an unflagged index domain runs SQLite at the
// compiled-in path on the RWO volume and serves the Torznab facade on the
// port the installers' Service routes, behind the Secret the manager
// creates.
func TestIndexDomainOptions(t *testing.T) {
	clearIndexEnv(t)
	o, err := parse(t, "--domain=index")
	require.NoError(t, err)
	assert.Equal(t, agent.DefaultIndexPath, o.IndexPath)
	assert.Equal(t, "/var/lib/clustarr/index/releases.db", o.IndexPath)
	assert.Equal(t, ":8080", o.FacadeBindAddress)
	assert.Equal(t, agent.DefaultFacadeAPIKeySecret, o.FacadeAPIKeySecret)
	assert.Equal(t, "index-facade", o.FacadeAPIKeySecret)
	assert.Empty(t, o.IndexDSN, "SQLite unless a DSN is given")
}

// TestFacadeSettings: the facade's address and key Secret come from the
// environment the chart sets, and the facade fails closed: without a key
// Secret and a namespace to read it from, the domain refuses to start
// unless the facade is disabled.
func TestFacadeSettings(t *testing.T) {
	clearIndexEnv(t)
	t.Setenv(cli.EnvFacadeBindAddress, ":9696")
	t.Setenv(cli.EnvFacadeAPIKeySecret, "media-clustarr-index-facade")
	o, err := parse(t, "--domain=index", "--namespace=media")
	require.NoError(t, err)
	assert.Equal(t, ":9696", o.FacadeBindAddress)
	assert.Equal(t, "media-clustarr-index-facade", o.FacadeAPIKeySecret)

	_, err = parse(t, "--domain=index", "--namespace=", "--facade-api-key-secret=")
	require.ErrorContains(t, err, "the Torznab facade fails closed")
	_, err = parse(t, "--domain=index", "--namespace=media", "--facade-api-key-secret=")
	require.ErrorContains(t, err, "the Torznab facade fails closed", "a namespace alone is not a key")
	_, err = parse(t, "--domain=index", "--namespace=", "--facade-bind-address=0")
	require.NoError(t, err, "a disabled facade needs neither")
}

// TestIndexDSNFlagReachesTheIndexDomain is A2's flag-wiring test: execute the
// real command tree with a recording run and assert on the options the index
// domain would start with. --index-dsn is a first-class flag of
// `agent --domain=index` (spec §A.3), and under a DSN --index-path may be
// empty.
func TestIndexDSNFlagReachesTheIndexDomain(t *testing.T) {
	// Clearing the environment makes the assertion about the FLAG reaching
	// the options, not about the env default doing so -- the two are easy
	// to conflate since the flag's own default reads the same variable.
	clearIndexEnv(t)
	o, err := parse(t, "--domain=index", "--index-dsn", wantDSN, "--index-path=")
	require.NoError(t, err, "--index-path may be empty under a DSN")
	assert.Equal(t, wantDSN, o.IndexDSN, "--index-dsn did not reach the index domain's options")
}

// TestIndexDSNEnvDefault proves $CLUSTARR_INDEX_DSN is the flag's default,
// the way $CLUSTARR_INDEX_PATH is for --index-path, and that an explicit
// flag wins over it.
func TestIndexDSNEnvDefault(t *testing.T) {
	clearIndexEnv(t)
	t.Setenv(cli.EnvIndexDSN, wantDSN)
	o, err := parse(t, "--domain=index")
	require.NoError(t, err)
	assert.Equal(t, wantDSN, o.IndexDSN, "--index-dsn did not default from $%s", cli.EnvIndexDSN)

	t.Setenv(cli.EnvIndexPath, "/var/lib/clustarr/index/other.db")
	o, err = parse(t, "--domain=index", "--index-dsn=postgres://flag@pg/index")
	require.NoError(t, err)
	assert.Equal(t, "postgres://flag@pg/index", o.IndexDSN, "an explicit flag wins over the environment")
	assert.Equal(t, "/var/lib/clustarr/index/other.db", o.IndexPath, "--index-path defaults from $%s", cli.EnvIndexPath)
}

// TestIndexDSNEmptyKeepsSQLite proves the zero value -- no flag, no env --
// leaves IndexDSN empty, so the index domain's open-path pivot takes the
// SQLite branch (spec §A.3: "empty keeps SQLite... the default"), and that
// SQLite then needs its file: an empty --index-path with no DSN is refused.
func TestIndexDSNEmptyKeepsSQLite(t *testing.T) {
	clearIndexEnv(t)
	o, err := parse(t, "--domain=index")
	require.NoError(t, err)
	require.Empty(t, o.IndexDSN, "IndexDSN set with neither --index-dsn nor $%s given", cli.EnvIndexDSN)
	require.NotEmpty(t, o.IndexPath, "IndexPath must still default when IndexDSN is empty")

	_, err = parse(t, "--domain=index", "--index-path=")
	require.ErrorContains(t, err, "--index-path is required unless --index-dsn is set")
}
