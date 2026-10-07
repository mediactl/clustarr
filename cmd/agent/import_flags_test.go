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
	"path/filepath"
	"strings"
	"testing"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestImportSampleMaxBytesReachesTheOptions pins the one link of the
// sample size floor's wiring that fails silently. newImportCommand builds
// import.Options as a bare literal rather than from DefaultOptions(), so
// a field it leaves out is zero -- and for SampleMaxBytes zero is not "unset"
// but a working value: the rule switched off on every replica, every video
// under 50 MiB imported as a feature film, with nothing in the logs.
// import's own TestWorkersGetTheSampleSizeFloor holds the next link,
// Options to both workers.
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
			got := stub(t, &runImport)
			args := append([]string{"import", "--role", "worker", "--namespace", "clustarr"}, tc.flags...)
			if _, err := execute(t, args...); err != nil {
				t.Fatalf("clustarr %v: %v", args, err)
			}
			if got.SampleMaxBytes != tc.want {
				t.Errorf("import.Options.SampleMaxBytes = %d, want %d", got.SampleMaxBytes, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("the parsed options are invalid: %v", err)
			}
		})
	}

	t.Run("a negative floor is refused", func(t *testing.T) {
		got := stub(t, &runImport)
		if _, err := execute(t, "import", "--role", "worker", "--namespace", "clustarr",
			"--sample-max-bytes=-1"); err != nil {
			t.Fatalf("clustarr import: %v", err)
		}
		if err := got.Validate(); err == nil || !strings.Contains(err.Error(), "sample-max-bytes") {
			t.Errorf("Validate() = %v, want an error naming --sample-max-bytes", err)
		}
	})

	// `clustarr all` starts from import.DefaultOptions(), so it keeps the
	// default without a flag of its own -- asserted through its own closure,
	// not a restatement of it.
	t.Run("clustarr all", func(t *testing.T) {
		got := stub(t, &runImport)
		if err := allServiceRun(t, "import")(context.Background(), k8s.DefaultOptions()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if got.SampleMaxBytes != fsops.DefaultSampleMaxBytes {
			t.Errorf("`clustarr all` gives import SampleMaxBytes = %d, want %d",
				got.SampleMaxBytes, fsops.DefaultSampleMaxBytes)
		}
	})

	// And through what ships: both import Deployments' own argv.
	t.Run("config/manager", func(t *testing.T) {
		t.Setenv(natsURLEnv, "nats://nats.clustarr-system.svc:4222")
		t.Setenv(namespaceEnv, "clustarr-system")
		var checked int
		for _, file := range []string{"import.yaml", "import-worker.yaml"} {
			for _, d := range deploymentsIn(t, filepath.Join("../../config/manager", file)) {
				argv := d.Spec.Template.Spec.Containers[0].Args
				got := stub(t, &runImport)
				if _, err := execute(t, argv...); err != nil {
					t.Fatalf("clustarr %v: %v", argv, err)
				}
				if got.SampleMaxBytes != fsops.DefaultSampleMaxBytes {
					t.Errorf("Deployment %q runs import with SampleMaxBytes = %d, want the %d default",
						d.Name, got.SampleMaxBytes, fsops.DefaultSampleMaxBytes)
				}
				checked++
			}
		}
		if checked != 2 {
			t.Fatalf("checked %d import Deployments, want 2 (import, import-worker)", checked)
		}
	})
}

// TestImportListBaseURLsReachTheOptions holds the first link of the
// Trakt and Plex base-URL wiring (X14): --trakt-base-url/--plex-base-url,
// and the $CLUSTARR_TRAKT_BASE_URL/$CLUSTARR_PLEX_BASE_URL defaults
// config/e2e sets, reach import.Options -- from `clustarr import` and
// from `clustarr all` alike. import's TestImportListsGetTheirBaseURLs
// holds the next, Options to the list worker and the ImportList controller.
// Before X14 neither host was reachable from a deployed binary at all, so
// test/e2e's Trakt and Plex legs skipped.
func TestImportListBaseURLsReachTheOptions(t *testing.T) {
	const trakt, plex = "http://importlist-stub.clustarr-system.svc", "http://plex.example:32400"

	t.Run("flags", func(t *testing.T) {
		got := stub(t, &runImport)
		if _, err := execute(t, "import", "--role", "worker", "--namespace", "clustarr",
			"--trakt-base-url", trakt, "--plex-base-url", plex); err != nil {
			t.Fatalf("clustarr import: %v", err)
		}
		if got.TraktBaseURL != trakt || got.PlexBaseURL != plex {
			t.Errorf("import.Options Trakt/Plex base URLs = %q/%q, want %q/%q",
				got.TraktBaseURL, got.PlexBaseURL, trakt, plex)
		}
	})

	t.Run("environment", func(t *testing.T) {
		t.Setenv(traktBaseURLEnv, trakt)
		t.Setenv(plexBaseURLEnv, plex)
		got := stub(t, &runImport)
		if _, err := execute(t, "import", "--role", "worker", "--namespace", "clustarr"); err != nil {
			t.Fatalf("clustarr import: %v", err)
		}
		if got.TraktBaseURL != trakt || got.PlexBaseURL != plex {
			t.Errorf("import.Options Trakt/Plex base URLs = %q/%q, want $%s/$%s's %q/%q",
				got.TraktBaseURL, got.PlexBaseURL, traktBaseURLEnv, plexBaseURLEnv, trakt, plex)
		}
	})

	t.Run("unset is the providers' own default", func(t *testing.T) {
		got := stub(t, &runImport)
		if _, err := execute(t, "import", "--role", "worker", "--namespace", "clustarr"); err != nil {
			t.Fatalf("clustarr import: %v", err)
		}
		if got.TraktBaseURL != "" || got.PlexBaseURL != "" {
			t.Errorf("import.Options Trakt/Plex base URLs = %q/%q with nothing set, want empty",
				got.TraktBaseURL, got.PlexBaseURL)
		}
	})

	t.Run("clustarr all", func(t *testing.T) {
		t.Setenv(traktBaseURLEnv, trakt)
		t.Setenv(plexBaseURLEnv, plex)
		got := stub(t, &runImport)
		if err := allServiceRun(t, "import")(context.Background(), k8s.DefaultOptions()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if got.TraktBaseURL != trakt || got.PlexBaseURL != plex {
			t.Errorf("`clustarr all` gives import Trakt/Plex base URLs %q/%q, want %q/%q",
				got.TraktBaseURL, got.PlexBaseURL, trakt, plex)
		}
	})
}
