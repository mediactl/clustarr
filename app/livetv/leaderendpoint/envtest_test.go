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

package leaderendpoint_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/app/livetv/leaderendpoint"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/test/starttest"
)

// TestLiveTVServiceRoutesToTheLeader is the control-plane placement design's
// §8 guard: two managers share one lease, the first leader's slice names its
// address, and once it stops the second leader's address replaces it.
func TestLiveTVServiceRoutesToTheLeader(t *testing.T) {
	env := starttest.StartEnv(t)
	start := func(ip string) context.CancelFunc {
		mgr, err := ctrl.NewManager(env.Config, ctrl.Options{
			Scheme: k8s.MustNewScheme(), LeaderElection: true, LeaderElectionID: "livetv-endpoint-test",
			LeaderElectionNamespace: "default", LeaderElectionReleaseOnCancel: true,
			Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
			// The publisher logs through its context: a failed write shows
			// in the test's output.
			BaseContext: func() context.Context {
				return logging.NewContext(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil)).With("ip", ip))
			},
		})
		if err != nil {
			t.Fatalf("manager: %v", err)
		}
		listening := tuner{ch: make(chan struct{})}
		close(listening.ch)
		if err := mgr.Add(&leaderendpoint.Publisher{Client: mgr.GetClient(), Tuner: listening,
			Service: "clustarr-livetv", Namespace: "default", PodName: "manager-" + ip, PodIP: ip, Port: 5004}); err != nil {
			t.Fatalf("add: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = mgr.Start(ctx) }()
		return func() { cancel(); <-done }
	}
	c, err := client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatal(err)
	}
	address := func() string {
		var s discoveryv1.EndpointSlice
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "clustarr-livetv"}, &s); err != nil || len(s.Endpoints) != 1 {
			return ""
		}
		return s.Endpoints[0].Addresses[0]
	}

	stopFirst := start("10.0.0.1")
	starttest.WaitFor(t, "the first leader's address", func() bool { return address() == "10.0.0.1" })
	stopSecond := start("10.0.0.2")
	defer stopSecond()
	time.Sleep(2 * time.Second)
	if got := address(); got != "10.0.0.1" {
		t.Fatalf("a standby published %q while the leader held the lease", got)
	}
	stopFirst()
	starttest.WaitForLong(t, "the second leader's address", func() bool { return address() == "10.0.0.2" })
}
