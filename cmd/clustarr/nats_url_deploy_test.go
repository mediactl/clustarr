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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	captionarr "github.com/mediactl/clustarr/app/caption"
	catalogarr "github.com/mediactl/clustarr/app/catalog"
	grabarr "github.com/mediactl/clustarr/app/grab"
	importarr "github.com/mediactl/clustarr/app/import"
	indexarr "github.com/mediactl/clustarr/app/indexer"
	squasharr "github.com/mediactl/clustarr/app/squash"
	"github.com/mediactl/clustarr/ui"
)

// TestEveryNATSDialingDeploymentCarriesNATSURL holds both installers to
// giving NATS_URL to every Deployment whose command dials NATS.
//
// The set is derived from the command tree, not listed: a Deployment's argv
// is resolved by the real root command, and a subcommand that defines
// --nats-url reads $NATS_URL as that flag's default -- so its pod dials
// whatever the variable says, or the binary's built-in default when it is
// unset. That default is a Service name no installer creates, and
// k8s.ConnectBus retries a failed connect in the background without ever
// returning an error, so a missing variable is silence, not a crash: B3 gave
// ui a bus for /art and neither installer gave ui the variable, so every
// artwork and Plex image request hung and then 500ed, with every test green.
//
// Every value within one installer must also be the same endpoint.
func TestEveryNATSDialingDeploymentCarriesNATSURL(t *testing.T) {
	check := func(t *testing.T, installer string, deployments []appsv1.Deployment) {
		t.Helper()
		dialers := map[string]string{} // component -> NATS_URL
		for _, d := range deployments {
			containers := d.Spec.Template.Spec.Containers
			if len(containers) == 0 || len(containers[0].Args) == 0 {
				continue
			}
			root := NewRootCommand()
			cmd, _, err := root.Find(containers[0].Args)
			if err != nil || cmd == root {
				// Not a clustarr command line: a subchart's operator.
				continue
			}
			if cmd.Flags().Lookup("nats-url") == nil {
				continue
			}
			component := d.Spec.Template.Labels["app.kubernetes.io/component"]
			if component == "" {
				component = d.Name
			}
			var url string
			for _, e := range containers[0].Env {
				if e.Name == natsURLEnv {
					url = e.Value
				}
			}
			require.NotEmptyf(t, url,
				"%s: Deployment %q runs `clustarr %s`, which dials NATS, and sets no %s -- "+
					"it would dial the binary's default %q, which no installer creates",
				installer, d.Name, cmd.Name(), natsURLEnv, defaultNATSURLForTest())
			dialers[component] = url
		}

		require.Contains(t, dialers, "ui",
			"%s: the ui Deployment was not recognised as dialling NATS (serves /art from the object store)", installer)
		require.Contains(t, dialers, "catalogarr", "%s: no catalogarr Deployment was checked", installer)
		want := dialers["catalogarr"]
		for component, url := range dialers {
			require.Equalf(t, want, url, "%s: Deployment %q dials a different NATS endpoint from catalogarr",
				installer, component)
		}
	}

	t.Run("kustomize", func(t *testing.T) {
		paths, err := filepath.Glob("../../config/manager/*.yaml")
		require.NoError(t, err)
		require.NotEmpty(t, paths)
		var all []appsv1.Deployment
		for _, p := range paths {
			all = append(all, deploymentsIn(t, p)...)
		}
		check(t, "config/manager", all)
	})

	t.Run("chart", func(t *testing.T) {
		helm := findTool(t, "helm")
		root, err := filepath.Abs("../..")
		require.NoError(t, err)
		check(t, "charts/clustarr",
			decodeRendered(t, run(t, root, helm, "template", "clustarr", "charts/clustarr")).deployments)
	})
}

// defaultNATSURLForTest is the --nats-url default a pod with no NATS_URL
// falls back to, read off the real flag so the failure message names it.
func defaultNATSURLForTest() string {
	cmd, _, err := NewRootCommand().Find([]string{"ui"})
	if err != nil {
		return ""
	}
	if f := cmd.Flags().Lookup("nats-url"); f != nil {
		return f.DefValue
	}
	return ""
}

// TestUICommandsCloseTheirBusOnShutdown proves both `clustarr ui` and
// `clustarr all` hold ui's artwork connection open for as long as runUI
// runs, and close it once runUI returns rather than leaving it to process
// exit. The server's own client count is the observation: nothing else in
// either command dials it, since every other service is stubbed.
func TestUICommandsCloseTheirBusOnShutdown(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)

	srv := startCountingNATS(t)

	for _, argv := range [][]string{
		{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous", "--nats-url", srv.ClientURL()},
		{"all", "--ui-auth-mode", "anonymous", "--nats-url", srv.ClientURL()},
	} {
		t.Run("clustarr "+argv[0], func(t *testing.T) {
			var (
				mu           sync.Mutex
				whileRunning = -1
			)
			stubServicesExceptUI(t, func(context.Context, ui.Options) error {
				mu.Lock()
				defer mu.Unlock()
				whileRunning = srv.NumClients()
				return nil
			})

			out, err := execute(t, argv...)
			require.NoError(t, err, out)

			mu.Lock()
			got := whileRunning
			mu.Unlock()
			require.Equal(t, 1, got, "ui's bus was not connected while runUI ran")
			waitFor(t, "ui's NATS connection to close after runUI returned", func() bool {
				return srv.NumClients() == 0
			})
		})
	}
}

// TestBuildUIArtworkLogsAnUnconnectedBus: an endpoint nothing listens on is
// not a ConnectBus error (it retries in the background), so the only signal
// an operator gets is the one startup line naming the URL. A reachable
// endpoint logs nothing of the kind.
func TestBuildUIArtworkLogsAnUnconnectedBus(t *testing.T) {
	capture := func(t *testing.T, url string) string {
		t.Helper()
		var (
			mu    sync.Mutex
			lines []string
		)
		sink := funcr.New(func(prefix, args string) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, prefix+" "+args)
		}, funcr.Options{})
		ctx := logr.NewContext(context.Background(), sink)

		store, stop := buildUIArtwork(ctx, url)
		require.NotNil(t, store)
		require.NotNil(t, stop)
		stop()

		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}

	t.Run("unreachable", func(t *testing.T) {
		url := "nats://" + freeAddress(t)
		logged := capture(t, url)
		require.Contains(t, logged, "NATS is not connected yet")
		require.Contains(t, logged, url, "the line must name the endpoint being dialled")
	})

	t.Run("reachable", func(t *testing.T) {
		srv := startCountingNATS(t)
		require.NotContains(t, capture(t, srv.ClientURL()), "not connected")
	})
}

// startCountingNATS runs an embedded JetStream server and returns it, so a
// test can count the clients connected to it (start_envtest_test.go's
// startEmbeddedNATS returns only the URL).
func startCountingNATS(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-cmd-ui-bus-test",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   t.TempDir(),
		NoLog:      true,
		NoSigs:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded NATS server did not become ready")
	t.Cleanup(srv.Shutdown)
	return srv
}

// stubServicesExceptUI replaces every service entrypoint with a no-op and
// runUI with uiRun, restoring them all when the test ends.
func stubServicesExceptUI(t *testing.T, uiRun func(context.Context, ui.Options) error) {
	t.Helper()
	catalog, index, grab, squash, caption, importa, origUI :=
		runCatalogarr, runIndexarr, runGrabarr, runSquasharr, runCaptionarr, runImportarr, runUI
	t.Cleanup(func() {
		runCatalogarr, runIndexarr, runGrabarr, runSquasharr, runCaptionarr, runImportarr, runUI =
			catalog, index, grab, squash, caption, importa, origUI
	})
	runCatalogarr = func(context.Context, catalogarr.Options) error { return nil }
	runIndexarr = func(context.Context, indexarr.Options) error { return nil }
	runGrabarr = func(context.Context, grabarr.Options) error { return nil }
	runSquasharr = func(context.Context, squasharr.Options) error { return nil }
	runCaptionarr = func(context.Context, captionarr.Options) error { return nil }
	runImportarr = func(context.Context, importarr.Options) error { return nil }
	runUI = uiRun
}
