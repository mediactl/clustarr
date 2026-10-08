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
	"github.com/mediactl/clustarr/pkg/fsops"
)

// TestImportDomainOptions holds the import domain's flags to the options it
// starts with. The sample size floor is the link that fails silently: for
// SampleMaxBytes zero is not "unset" but a working value -- the rule switched
// off on every replica, every video under 50 MiB imported as a feature film,
// with nothing in the logs -- so the default must reach the options when no
// flag is given, and a negative floor, which pkg/fsops reads as "disabled"
// exactly as it reads zero, is a typo the command refuses rather than a
// second way to switch the rule off. app/import/agent's workers hold the
// next link, options to both workers; the installers' own argv is
// TestInstallerArgsParseAsTheirDomain's.
//
// The Trakt and Plex base URLs (X14) reach the options from their flags and
// from $CLUSTARR_TRAKT_BASE_URL/$CLUSTARR_PLEX_BASE_URL, which config/e2e
// sets; unset, each is the provider's own default. Before X14 neither host
// was reachable from a deployed binary at all.
func TestImportDomainOptions(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv(cli.EnvTraktBaseURL, "")
		t.Setenv(cli.EnvPlexBaseURL, "")
		o, err := parse(t, "--domain=import")
		require.NoError(t, err)
		assert.Equal(t, "/data", o.DataDir)
		assert.Equal(t, fsops.DefaultSampleMaxBytes, o.SampleMaxBytes, "a zero floor would switch the sample rule off")
		assert.Empty(t, o.TraktBaseURL, "unset is Trakt's own API")
		assert.Empty(t, o.PlexBaseURL, "unset is Plex's own Discover API")
	})

	for _, tc := range []struct {
		name string
		flag string
		want int64
	}{
		{name: "an explicit floor", flag: "--sample-max-bytes=1048576", want: 1 << 20},
		{name: "0 disables the rule", flag: "--sample-max-bytes=0", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parse(t, "--domain=import", tc.flag)
			require.NoError(t, err)
			assert.Equal(t, tc.want, o.SampleMaxBytes)
		})
	}

	t.Run("a negative floor is refused", func(t *testing.T) {
		_, err := parse(t, "--domain=import", "--sample-max-bytes=-1")
		require.ErrorContains(t, err, "--sample-max-bytes must be 0 (disabled) or more, got -1")
	})

	const trakt, plex = "http://importlist-stub.clustarr-system.svc", "http://plex.example:32400"
	t.Run("base URL flags", func(t *testing.T) {
		t.Setenv(cli.EnvTraktBaseURL, "")
		t.Setenv(cli.EnvPlexBaseURL, "")
		o, err := parse(t, "--domain=import", "--trakt-base-url", trakt, "--plex-base-url", plex)
		require.NoError(t, err)
		assert.Equal(t, trakt, o.TraktBaseURL)
		assert.Equal(t, plex, o.PlexBaseURL)
	})

	t.Run("base URLs from the environment", func(t *testing.T) {
		t.Setenv(cli.EnvTraktBaseURL, trakt)
		t.Setenv(cli.EnvPlexBaseURL, plex)
		o, err := parse(t, "--domain=import")
		require.NoError(t, err)
		assert.Equal(t, trakt, o.TraktBaseURL, "--trakt-base-url defaults to $%s", cli.EnvTraktBaseURL)
		assert.Equal(t, plex, o.PlexBaseURL, "--plex-base-url defaults to $%s", cli.EnvPlexBaseURL)
	})
}

// TestImportDomainRejectsAnEmptyDataDir: the import domain reads and writes
// the library under --data-dir, so an empty one is refused before anything
// touches the cluster.
func TestImportDomainRejectsAnEmptyDataDir(t *testing.T) {
	_, err := parse(t, "--domain=import", "--data-dir=")
	require.ErrorContains(t, err, "--domain import needs --data-dir")
}
