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

package k8s

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDefaultOptionsMatchTheSpec(t *testing.T) {
	o := DefaultOptions()
	if o.MetricsBindAddress != ":8443" {
		t.Errorf("metrics bind address = %q, want :8443 (§13)", o.MetricsBindAddress)
	}
	if !o.MetricsSecure {
		t.Error("metrics are not served securely by default")
	}
	if !o.UsesBus() {
		t.Error("the default options have no NATS endpoint")
	}
}

func TestManagerOptionsLeaderElection(t *testing.T) {
	o := DefaultOptions()
	o.Namespace = "clustarr"

	// Controllers are leader-elected; §12 runs one active replica per
	// service.
	opts := o.ManagerOptions("catalogarr.clustarr.io", true)
	if !opts.LeaderElection {
		t.Error("leader election was not enabled for a controller role")
	}
	if opts.LeaderElectionID != "catalogarr.clustarr.io" {
		t.Errorf("LeaderElectionID = %q", opts.LeaderElectionID)
	}
	if opts.LeaderElectionNamespace != "clustarr" {
		t.Errorf("LeaderElectionNamespace = %q, want the process namespace", opts.LeaderElectionNamespace)
	}
	if !opts.LeaderElectionReleaseOnCancel {
		t.Error("the leader does not step down on SIGTERM; failover would wait out the lease")
	}

	// Workers run on every replica: a worker that waited for the lease would
	// never start.
	opts = o.ManagerOptions("catalogarr.clustarr.io", false)
	if opts.LeaderElection {
		t.Error("leader election was enabled for a worker role")
	}
}

func TestManagerOptionsLeaderElectionNamespaceOverride(t *testing.T) {
	o := DefaultOptions()
	o.Namespace = "pod-namespace"
	o.LeaderElectionNamespace = "explicit"
	if got := o.ManagerOptions("indexarr.clustarr.io", true).LeaderElectionNamespace; got != "explicit" {
		t.Fatalf("LeaderElectionNamespace = %q, want the explicit flag to win", got)
	}
}

func TestManagerOptionsMetrics(t *testing.T) {
	o := DefaultOptions()
	opts := o.ManagerOptions("indexarr.clustarr.io", false)
	if !opts.Metrics.SecureServing {
		t.Error("SecureServing is off")
	}

	o.MetricsSecure = false
	if o.ManagerOptions("indexarr.clustarr.io", false).Metrics.SecureServing {
		t.Error("SecureServing survived --metrics-secure=false")
	}

	o.MetricsBindAddress = DisabledBindAddress
	opts = o.ManagerOptions("indexarr.clustarr.io", false)
	if opts.Metrics.BindAddress != DisabledBindAddress {
		t.Errorf("BindAddress = %q, want the disable sentinel", opts.Metrics.BindAddress)
	}
	if opts.Metrics.FilterProvider != nil {
		t.Error("a disabled metrics server was given a filter")
	}
}

func TestManagerOptionsWatchNamespaces(t *testing.T) {
	o := DefaultOptions()

	if got := o.ManagerOptions("grabarr.clustarr.io", false).Cache.DefaultNamespaces; got != nil {
		t.Errorf("an unset --watch-namespace scoped the cache to %v", got)
	}

	o.WatchNamespaces = []string{"media", "media-staging"}
	got := o.ManagerOptions("grabarr.clustarr.io", false).Cache.DefaultNamespaces
	if len(got) != 2 {
		t.Fatalf("cache namespaces = %v, want two entries", got)
	}
	for _, ns := range o.WatchNamespaces {
		if _, ok := got[ns]; !ok {
			t.Errorf("namespace %q is not in the cache config", ns)
		}
	}
}

func TestManagerOptionsGracefulShutdown(t *testing.T) {
	o := DefaultOptions()
	opts := o.ManagerOptions("squasharr.clustarr.io", false)
	if opts.GracefulShutdownTimeout == nil || *opts.GracefulShutdownTimeout != DefaultGracefulShutdownTimeout {
		t.Fatalf("GracefulShutdownTimeout = %v", opts.GracefulShutdownTimeout)
	}

	o.GracefulShutdownTimeout = 90 * time.Second
	opts = o.ManagerOptions("squasharr.clustarr.io", false)
	if *opts.GracefulShutdownTimeout != 90*time.Second {
		t.Fatalf("GracefulShutdownTimeout = %v, want the override", *opts.GracefulShutdownTimeout)
	}
}

func TestManagerOptionsAlwaysHasAScheme(t *testing.T) {
	opts := DefaultOptions().ManagerOptions("captionarr.clustarr.io", false)
	if opts.Scheme == nil {
		t.Fatal("ManagerOptions returned a nil scheme")
	}
	if len(opts.Scheme.AllKnownTypes()) == 0 {
		t.Fatal("the scheme is empty")
	}
}

func TestOptionsValidate(t *testing.T) {
	good := DefaultOptions()
	good.Namespace = "clustarr"
	if err := good.Validate(); err != nil {
		t.Fatalf("the default options are invalid: %v", err)
	}

	bad := DefaultOptions()
	bad.GracefulShutdownTimeout = -time.Second
	if err := bad.Validate(); err == nil {
		t.Error("a negative shutdown timeout was accepted")
	}

	bad = DefaultOptions()
	bad.WatchNamespaces = []string{"media", "  "}
	if err := bad.Validate(); err == nil {
		t.Error("a blank watch namespace was accepted")
	}

	// Leader election with nowhere to put the Lease fails fast rather than
	// looping on a Lease in the empty namespace.
	bad = DefaultOptions()
	bad.LeaderElect = true
	if err := bad.Validate(); err == nil {
		t.Error("leader election with no namespace was accepted")
	}
}

func TestUsesBus(t *testing.T) {
	o := DefaultOptions()
	o.NATSURL = ""
	if o.UsesBus() {
		t.Error("UsesBus = true with no url")
	}
	o.NATSURL = "   "
	if o.UsesBus() {
		t.Error("UsesBus = true for a blank url")
	}
}

func TestRegisterRESTClientMetricsIsIdempotent(t *testing.T) {
	// The underlying registration panics on a duplicate, and `clustarr all`
	// starts five services in one process.
	RegisterRESTClientMetrics()
	RegisterRESTClientMetrics()
}

// TestManagerOptionsSurviveABigLibrary: on a library of 15,000 Episodes
// the initial List is 57 MB and does not always finish inside
// controller-runtime's two-minute cache-sync default, which crash-looped
// captionarr (2026-09-24), so every manager waits CacheSyncTimeout and
// every cache strips managedFields -- a third of those bytes that no
// reconciler reads from a cached object (the two readers of managedFields
// go through the API reader). The transform survives a namespace list.
func TestManagerOptionsSurviveABigLibrary(t *testing.T) {
	for name, o := range map[string]Options{
		"cluster wide": {},
		"namespaced":   {WatchNamespaces: []string{"media"}},
	} {
		t.Run(name, func(t *testing.T) {
			opts := o.ManagerOptions("catalogarr.clustarr.io", false)
			if opts.Controller.CacheSyncTimeout != CacheSyncTimeout {
				t.Fatalf("cache sync timeout = %v, want %v", opts.Controller.CacheSyncTimeout, CacheSyncTimeout)
			}
			if CacheSyncTimeout < 10*time.Minute {
				t.Fatalf("CacheSyncTimeout = %v, want at least ten minutes", CacheSyncTimeout)
			}
			if opts.Cache.DefaultTransform == nil {
				t.Fatal("the cache has no default transform")
			}
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name:          "x",
				ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply}},
			}}
			got, err := opts.Cache.DefaultTransform(cm)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.(*corev1.ConfigMap).ManagedFields) != 0 {
				t.Fatal("the transform left managedFields on the object")
			}
			if got.(*corev1.ConfigMap).Name != "x" {
				t.Fatal("the transform touched more than managedFields")
			}
			if len(o.WatchNamespaces) > 0 && len(opts.Cache.DefaultNamespaces) != 1 {
				t.Fatalf("namespaces = %v, want the one configured", opts.Cache.DefaultNamespaces)
			}
		})
	}
}

// controller-runtime hands every runnable a context derived from
// Options.BaseContext, a fresh Background by default, and not from the one
// passed to mgr.Start; so the logger obs.Bootstrap put on the service's
// context reached no reconciler, and every service's own log lines were
// silence. WithBaseContext is the one place that closes the gap.
func TestWithBaseContextCarriesTheServicesLoggerToRunnables(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := logging.NewContext(context.Background(), logger)
	opts := WithBaseContext(DefaultOptions().ManagerOptions("catalogarr.clustarr.io", false), ctx)
	if opts.BaseContext == nil {
		t.Fatal("no BaseContext: runnables would get context.Background and its discard logger")
	}
	if got := logging.FromContext(opts.BaseContext()); got != logger {
		t.Fatal("the runnables' base context does not carry the service's logger")
	}
}
