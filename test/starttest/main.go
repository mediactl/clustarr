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

package starttest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// Main runs m against one envtest control plane built from the repository's
// CRDs, storing its config in *cfg, and returns m's exit code. Without
// KUBEBUILDER_ASSETS it runs m with *cfg nil, and every test calling
// RequireEnvtest skips by name: a suite that finished in milliseconds
// skipped, it did not pass.
func Main(m *testing.M, cfg **rest.Config) int {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		return m.Run()
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "starttest:", err)
		return 1
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(root, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	c, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "starttest: start envtest:", err)
		return 1
	}
	*cfg = c
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "starttest: stop envtest:", err)
	}
	return code
}

// repoRoot is the nearest directory at or above the working directory that
// holds go.mod. Main has no testing.TB, so it cannot use
// installtest.RepoRoot.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod at or above the working directory")
		}
		dir = parent
	}
}

// RequireEnvtest returns cfg, or skips the test when Main ran without a
// control plane (and fails it under installtest.RequireTools).
func RequireEnvtest(t testing.TB, cfg *rest.Config) *rest.Config {
	t.Helper()
	if cfg == nil {
		requireAssets(t)
		t.Fatal("KUBEBUILDER_ASSETS is set but Main started no control plane; call starttest.Main from TestMain")
	}
	return cfg
}

// NewManager builds an unstarted manager on cfg with no listeners, the
// clustarr scheme and SkipNameValidation (one test binary may register a
// domain more than once), then applies each mutate.
func NewManager(t testing.TB, cfg *rest.Config, mutate ...func(*ctrl.Options)) ctrl.Manager {
	t.Helper()
	opts := ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
		Controller:             config.Controller{SkipNameValidation: new(true)},
	}
	for _, f := range mutate {
		f(&opts)
	}
	mgr, err := ctrl.NewManager(cfg, opts)
	require.NoError(t, err)
	return mgr
}

// Start runs mgr until the test ends and returns the error it stops with.
// The cleanup waits for the stop on its own signal, so a test that already
// read the error cannot hang it.
func Start(t testing.TB, mgr ctrl.Manager) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	failed := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		failed <- mgr.Start(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			t.Error("the manager did not stop within 30s")
		}
	})
	return failed
}

// Client is an uncached client on cfg, for a test's own reads and writes.
func Client(t testing.TB, cfg *rest.Config) client.Client {
	t.Helper()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}
