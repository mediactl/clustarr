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

package k8s_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/starttest"
)

type watchRecorder struct {
	mu        sync.Mutex
	resources map[string]bool
	next      http.RoundTripper
}

func (r *watchRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Query().Get("watch") == "true" {
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		r.mu.Lock()
		r.resources[parts[len(parts)-1]] = true
		r.mu.Unlock()
	}
	return r.next.RoundTrip(req)
}

func (r *watchRecorder) watched() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for k := range r.resources {
		out[k] = true
	}
	return out
}

// TestStandbyWarmsTheFailoverKinds is the control-plane placement design's
// §8 guard: a manager that does not hold the lease still watches MediaFile,
// Episode, Movie and Series (the kinds whose initial list dominates a cold
// start), and nothing else of clustarr's, so a failover takes seconds and a
// standby holds only those caches.
func TestStandbyWarmsTheFailoverKinds(t *testing.T) {
	env := starttest.StartEnv(t)
	starttest.HoldLease(t, env.Config, "default", "standby-warm-test")
	rec := &watchRecorder{resources: map[string]bool{}}
	cfg := rest.CopyConfig(env.Config)
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper { rec.next = rt; return rec })
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: k8s.MustNewScheme(), LeaderElection: true, LeaderElectionID: "standby-warm-test",
		LeaderElectionNamespace: "default", Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := k8s.WarmInformers(mgr, &catalogv1alpha1.MediaFile{}, &catalogv1alpha1.Episode{},
		&catalogv1alpha1.Movie{}, &catalogv1alpha1.Series{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	defer func() { cancel(); <-done }()

	starttest.WaitFor(t, "the four failover kinds watched", func() bool {
		w := rec.watched()
		return w["mediafiles"] && w["episodes"] && w["movies"] && w["series"]
	})
	time.Sleep(2 * time.Second)
	for res := range rec.watched() {
		switch res {
		case "mediafiles", "episodes", "movies", "series", "leases":
		default:
			t.Errorf("a standby watches %s, which no warmer asked for", res)
		}
	}
}
