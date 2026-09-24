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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/squasharr/worker"
)

const reexecEnv = "SQUASHARR_WORKER_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		os.Exit(run(nil, os.Getenv))
	}
	os.Exit(m.Run())
}

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func fakeTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, b), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	}
	return dir
}

// ensureTopology creates the streams, consumers and buckets the worker needs
// against a real embedded JetStream server, the way squasharr/worker's own
// NATS-backed tests do (e.g. lease_nats_test.go's leaseKVWithTTL): a bus
// that has never had Ensure run against it has no clustarr-transcode-tasks
// stream to pull from and no clustarr-transcode-leases or clustarr-progress
// bucket, so Serve's Pull would fail before the SIGTERM this test means to
// exercise ever gets a chance to matter.
func ensureTopology(t *testing.T, url string) {
	t.Helper()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
}

func TestMissingEnvironmentIsMisconfigured(t *testing.T) {
	assert.Equal(t, worker.WorkerExitMisconfigured, run(nil, env(nil)))
}

func TestUnreachableNATSIsRetriable(t *testing.T) {
	t.Setenv("PATH", fakeTools(t))
	assert.Equal(t, worker.WorkerExitRetriable, run(nil, env(map[string]string{
		"NATS_URL": "nats://127.0.0.1:1", "CLUSTARR_POOL_PROFILE_UID": "p", "CLUSTARR_POOL_CLASS": "cpu", "POD_NAME": "w",
	})))
}

func TestSIGTERMIsDrained(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true,
		StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second))
	t.Cleanup(srv.Shutdown)
	ensureTopology(t, srv.ClientURL()) // natsbus.New + bus.Ensure(events.Default().ForSingleNode())

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), reexecEnv+"=1", "PATH="+fakeTools(t), "NATS_URL="+srv.ClientURL(),
		"CLUSTARR_POOL_PROFILE_UID=p", "CLUSTARR_POOL_CLASS=cpu", "POD_NAME=w")
	require.NoError(t, cmd.Start())
	time.Sleep(2 * time.Second) // connected and pulling
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	err = cmd.Wait()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, worker.WorkerExitDrained, ee.ExitCode())
}

// A work-queue Job ends the whole pool when one pod exits 0 (spec §9).
func TestRunNeverReturnsZero(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	require.NoError(t, err)
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
				if lit, ok := r.Results[0].(*ast.BasicLit); ok && lit.Value == "0" {
					t.Errorf("run returns 0 at offset %d", lit.Pos())
				}
				if sel, ok := r.Results[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "ExitOK" {
					t.Error("run returns worker.ExitOK")
				}
			}
			return true
		})
		return false
	})
}

func TestBinaryImportsNoKubernetesClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	require.NoError(t, err, string(out))
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range []string{
			"k8s.io/client-go", "sigs.k8s.io/controller-runtime/pkg/client",
			"sigs.k8s.io/controller-runtime/pkg/manager", "github.com/mediactl/clustarr/pkg/k8s",
		} {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Errorf("cmd/squasharr-worker depends on %s", dep)
			}
		}
		if dep == "github.com/mediactl/clustarr/pkg/obs" {
			t.Error("cmd/squasharr-worker depends on pkg/obs (links controller-runtime)")
		}
	}
}
