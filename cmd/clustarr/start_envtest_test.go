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
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestServiceStartsServesProbesAndStopsOnSignal is the M0 acceptance check for
// the binary: a service comes up against a real apiserver and a real JetStream
// server, answers /healthz and /readyz, and returns cleanly when its context is
// cancelled -- which is exactly what SIGTERM does through
// ctrl.SetupSignalHandler.
//
// It needs the envtest control-plane binaries and skips without them, like
// pkg/crdcheck; `make test` sets KUBEBUILDER_ASSETS.
func TestServiceStartsServesProbesAndStopsOnSignal(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	if _, err := env.Start(); err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	// ctrl.GetConfig reads KUBECONFIG, so point it at the test apiserver.
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, env.KubeConfig, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)

	natsURL := startEmbeddedNATS(t)
	probeAddr := freeAddress(t)

	o := catalogarr.DefaultOptions()
	o.Role = catalogarr.RoleController
	o.Namespace = "default"
	o.LeaderElect = false
	o.MetricsBindAddress = k8s.DisabledBindAddress
	o.HealthProbeBindAddress = probeAddr
	o.NATSURL = natsURL
	o.BusSingleNode = true
	o.GracefulShutdownTimeout = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- catalogarr.Run(ctx, o) }()

	// /healthz is a ping and comes up with the probe listener.
	waitForProbe(t, "http://"+probeAddr+"/healthz")

	// /readyz additionally pings JetStream (§13), which is why the embedded
	// server has to be running for this to ever pass.
	waitForProbe(t, "http://"+probeAddr+"/readyz")

	// SIGTERM cancels the signal-handler context; cancelling here is the
	// same path.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on a clean shutdown", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s of its context being cancelled")
	}

	// The probe listener is gone once the manager has stopped.
	if _, err := http.Get("http://" + probeAddr + "/healthz"); err == nil { //nolint:noctx // liveness of a closed listener
		t.Error("the health probe endpoint is still listening after shutdown")
	}
}

// TestServiceFailsFastOnBadOptions proves the validation runs before anything
// touches the cluster: no kubeconfig and no NATS server are needed for these.
func TestServiceFailsFastOnBadOptions(t *testing.T) {
	ctx := context.Background()

	o := catalogarr.DefaultOptions()
	o.Role = "nonsense"
	if err := catalogarr.Run(ctx, o); err == nil {
		t.Error("an unknown role reached the manager")
	}

	o = catalogarr.DefaultOptions()
	o.NATSURL = ""
	if err := catalogarr.Run(ctx, o); err == nil {
		t.Error("a missing --nats-url reached the manager")
	}

	o = catalogarr.DefaultOptions()
	o.LeaderElect = true
	o.Namespace = ""
	o.LeaderElectionNamespace = ""
	if err := catalogarr.Run(ctx, o); err == nil {
		t.Error("leader election with nowhere to put the Lease reached the manager")
	}
}

func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-cmd-test",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   t.TempDir(),
		NoLog:      true,
		NoSigs:     true,
	})
	if err != nil {
		t.Fatalf("new NATS server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// freeAddress asks the kernel for an unused port and hands back the address.
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return addr
}

func waitForProbe(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // bounded by the loop deadline
		if err != nil {
			last = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body := resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
		if body == http.StatusOK {
			return
		}
		last = fmt.Errorf("status %d", body)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never returned 200: %v", url, errors.Join(last))
}
