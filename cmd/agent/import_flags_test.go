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
	"strings"
	"testing"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// TestImportSampleMaxBytesReachesTheOptions pins the one link of the
// sample size floor's wiring that fails silently: for SampleMaxBytes zero is
// not "unset" but a working value -- the rule switched off on every replica,
// every video under 50 MiB imported as a feature film, with nothing in the
// logs. So the import domain's options must carry the default when no flag
// is given. app/import/agent's workers hold the next link, options to both
// workers. The old `clustarr all` leg has no subject (one process runs one
// domain, §3.10), and the installers' own argv is
// TestInstallerArgsParseAsTheirDomain's (W6.11).
func TestImportSampleMaxBytesReachesTheOptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  int64
	}{
		{name: "no flag keeps the 50 MiB default", want: fsops.DefaultSampleMaxBytes},
		{name: "an explicit floor", flags: []string{"--sample-max-bytes", "1048576"}, want: 1 << 20},
		{name: "0 disables the rule", flags: []string{"--sample-max-bytes=0"}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--domain=import"}, tc.flags...)
			got, err := parse(t, args...)
			if err != nil {
				t.Fatalf("agent %v: %v", args, err)
			}
			if got.SampleMaxBytes != tc.want {
				t.Errorf("SampleMaxBytes = %d, want %d", got.SampleMaxBytes, tc.want)
			}
		})
	}

	t.Run("a negative floor is refused", func(t *testing.T) {
		_, err := parse(t, "--domain=import", "--sample-max-bytes=-1")
		if err == nil || !strings.Contains(err.Error(), "sample-max-bytes") {
			t.Errorf("agent --domain=import --sample-max-bytes=-1: %v, want an error naming --sample-max-bytes", err)
		}
	})
}

// TestImportListBaseURLsReachTheOptions holds the first link of the
// Trakt and Plex base-URL wiring (X14): --trakt-base-url/--plex-base-url,
// and the $CLUSTARR_TRAKT_BASE_URL/$CLUSTARR_PLEX_BASE_URL defaults
// config/e2e sets, reach the import domain's options. Before X14 neither
// host was reachable from a deployed binary at all, so test/e2e's Trakt and
// Plex legs skipped.
func TestImportListBaseURLsReachTheOptions(t *testing.T) {
	const trakt, plex = "http://importlist-stub.clustarr-system.svc", "http://plex.example:32400"

	t.Run("flags", func(t *testing.T) {
		got, err := parse(t, "--domain=import", "--namespace", "clustarr",
			"--trakt-base-url", trakt, "--plex-base-url", plex)
		if err != nil {
			t.Fatalf("agent --domain=import: %v", err)
		}
		if got.TraktBaseURL != trakt || got.PlexBaseURL != plex {
			t.Errorf("Trakt/Plex base URLs = %q/%q, want %q/%q",
				got.TraktBaseURL, got.PlexBaseURL, trakt, plex)
		}
	})

	t.Run("environment", func(t *testing.T) {
		t.Setenv(cli.EnvTraktBaseURL, trakt)
		t.Setenv(cli.EnvPlexBaseURL, plex)
		got, err := parse(t, "--domain=import", "--namespace", "clustarr")
		if err != nil {
			t.Fatalf("agent --domain=import: %v", err)
		}
		if got.TraktBaseURL != trakt || got.PlexBaseURL != plex {
			t.Errorf("Trakt/Plex base URLs = %q/%q, want $%s/$%s's %q/%q",
				got.TraktBaseURL, got.PlexBaseURL, cli.EnvTraktBaseURL, cli.EnvPlexBaseURL, trakt, plex)
		}
	})

	t.Run("unset is the providers' own default", func(t *testing.T) {
		t.Setenv(cli.EnvTraktBaseURL, "")
		t.Setenv(cli.EnvPlexBaseURL, "")
		got, err := parse(t, "--domain=import", "--namespace", "clustarr")
		if err != nil {
			t.Fatalf("agent --domain=import: %v", err)
		}
		if got.TraktBaseURL != "" || got.PlexBaseURL != "" {
			t.Errorf("Trakt/Plex base URLs = %q/%q with nothing set, want empty",
				got.TraktBaseURL, got.PlexBaseURL)
		}
	})
}
