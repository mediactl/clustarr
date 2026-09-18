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
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Defaults for the flags every service shares.
const (
	// DefaultMetricsBindAddress is §13's secure metrics endpoint. It is
	// served over TLS behind the authentication/authorization filter, which
	// is what lets the ServiceMonitor scrape it with a bearer token.
	DefaultMetricsBindAddress = ":8443"

	// DefaultHealthProbeBindAddress serves /healthz and /readyz.
	DefaultHealthProbeBindAddress = ":8081"

	// DefaultNATSURL is the in-cluster address of the JetStream service the
	// umbrella chart installs.
	DefaultNATSURL = "nats://clustarr-nats:4222"

	// DefaultGracefulShutdownTimeout bounds how long the manager waits for
	// runnables to stop on SIGTERM. §12 ties a worker's drain to its AckWait,
	// so this has to be comfortably longer than the longest AckWait in the
	// default topology.
	DefaultGracefulShutdownTimeout = 60 * time.Second

	// DisabledBindAddress is controller-runtime's sentinel for "do not serve".
	DisabledBindAddress = "0"
)

// Options is the runtime configuration every `clustarr <service>` shares.
// A service embeds it in its own Options struct and adds its role and whatever
// else §6 gives it.
type Options struct {
	// MetricsBindAddress is where controller-runtime serves Prometheus
	// metrics. "0" disables the server.
	MetricsBindAddress string

	// MetricsSecure serves metrics over TLS. It is on by default; turning it
	// off is for local debugging only.
	MetricsSecure bool

	// MetricsFilterProvider wraps the metrics handler, and is how §13's
	// authentication/authorization filter gets installed.
	//
	// It is a field rather than a hard-wired call to
	// sigs.k8s.io/controller-runtime/pkg/metrics/filters because that package
	// pulls k8s.io/apiserver, k8s.io/component-base, OpenTelemetry and gRPC
	// into the module -- roughly twenty-five modules, into an image that is
	// meant to be distroless static. Wiring it is a one-line change in
	// cmd/clustarr once that cost is accepted.
	//
	// TODO(M1): set this to filters.WithAuthenticationAndAuthorization and
	// add the ServiceMonitor RBAC, per §13.
	MetricsFilterProvider func(c *rest.Config, httpClient *http.Client) (metricsserver.Filter, error)

	// HealthProbeBindAddress is where /healthz and /readyz are served. "0"
	// disables them, which also disables the Kubernetes probes, so leave it
	// set in a Deployment.
	HealthProbeBindAddress string

	// PprofBindAddress optionally serves net/http/pprof. Empty disables it.
	PprofBindAddress string

	// LeaderElect turns on leader election for the roles that run
	// controllers. Worker and engine roles ignore it: they are meant to run
	// on every replica.
	LeaderElect bool

	// LeaderElectionNamespace is where the Lease lives. Empty means the
	// namespace the process runs in, which is what an in-cluster Deployment
	// wants and what a kubeconfig-based local run has to override.
	LeaderElectionNamespace string

	// Namespace is the namespace the process itself runs in, from the
	// downward API. It is the default for the leader-election Lease and for
	// the objects a service creates for itself.
	Namespace string

	// WatchNamespaces restricts the cache, and therefore every controller, to
	// these namespaces. Empty means cluster-wide.
	WatchNamespaces []string

	// NATSURL is the JetStream endpoint. Empty disables the bus, which is
	// only valid for a role that does no queue work.
	NATSURL string

	// BusSingleNode collapses the topology to one replica. §12 runs NATS R3
	// in a cluster and R1 on kind; a stream asking for three replicas on a
	// one-node server is rejected outright, so `clustarr all` and the kind
	// scripts set this.
	BusSingleNode bool

	// GracefulShutdownTimeout bounds the drain on SIGTERM.
	GracefulShutdownTimeout time.Duration

	// Scheme is the scheme the manager's client and cache use. Empty means
	// [NewScheme].
	Scheme *runtime.Scheme
}

// DefaultOptions returns the options a Deployment gets with no flags set.
func DefaultOptions() Options {
	return Options{
		MetricsBindAddress:      DefaultMetricsBindAddress,
		MetricsSecure:           true,
		HealthProbeBindAddress:  DefaultHealthProbeBindAddress,
		LeaderElect:             false,
		NATSURL:                 DefaultNATSURL,
		GracefulShutdownTimeout: DefaultGracefulShutdownTimeout,
	}
}

// Validate checks the options that would otherwise fail deep inside the
// manager, or not at all.
func (o Options) Validate() error {
	if o.GracefulShutdownTimeout < 0 {
		return fmt.Errorf("k8s: graceful-shutdown-timeout must not be negative")
	}
	for _, ns := range o.WatchNamespaces {
		if strings.TrimSpace(ns) == "" {
			return fmt.Errorf("k8s: watch-namespace contains an empty entry")
		}
	}
	if o.LeaderElect && o.LeaderElectionNamespace == "" && o.Namespace == "" {
		return fmt.Errorf(
			"k8s: leader election needs a namespace: set --leader-election-namespace, " +
				"or --namespace, or run in-cluster with the downward API")
	}
	return nil
}

// UsesBus reports whether a NATS endpoint was configured.
func (o Options) UsesBus() bool { return strings.TrimSpace(o.NATSURL) != "" }

// ManagerOptions renders ctrl.Options for a manager.
//
// leaderElectionID is §2's `<service>.clustarr.io`. leaderElect is passed
// separately from o.LeaderElect so a service can refuse leader election for a
// worker role no matter what the flag says: §3's topology runs controllers
// leader-only and workers on every replica, and a worker that waited for a
// lease would simply never start.
//
// It does not touch the cluster, so it is safe to call from a test.
func (o Options) ManagerOptions(leaderElectionID string, leaderElect bool) ctrl.Options {
	scheme := o.Scheme
	if scheme == nil {
		scheme = MustNewScheme()
	}

	shutdown := o.GracefulShutdownTimeout
	if shutdown == 0 {
		shutdown = DefaultGracefulShutdownTimeout
	}

	opts := ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       o.metricsOptions(),
		HealthProbeBindAddress:        o.HealthProbeBindAddress,
		PprofBindAddress:              o.PprofBindAddress,
		LeaderElection:                leaderElect,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       o.leaderElectionNamespace(),
		LeaderElectionReleaseOnCancel: true,
		GracefulShutdownTimeout:       &shutdown,
	}

	if len(o.WatchNamespaces) > 0 {
		byNamespace := make(map[string]cache.Config, len(o.WatchNamespaces))
		for _, ns := range o.WatchNamespaces {
			byNamespace[ns] = cache.Config{}
		}
		opts.Cache = cache.Options{DefaultNamespaces: byNamespace}
	}

	return opts
}

func (o Options) leaderElectionNamespace() string {
	if o.LeaderElectionNamespace != "" {
		return o.LeaderElectionNamespace
	}
	return o.Namespace
}

func (o Options) metricsOptions() metricsserver.Options {
	addr := o.MetricsBindAddress
	if addr == "" {
		addr = DefaultMetricsBindAddress
	}
	m := metricsserver.Options{BindAddress: addr}
	if addr == DisabledBindAddress {
		return m
	}
	if o.MetricsSecure {
		m.SecureServing = true
	}
	if o.MetricsFilterProvider != nil {
		m.FilterProvider = o.MetricsFilterProvider
	}
	return m
}

var restClientMetricsOnce sync.Once

// RegisterRESTClientMetrics turns on the client-go REST metrics §13 asks for.
// It is safe to call from every service and from `clustarr all`; the
// underlying registration panics on a duplicate, so it is guarded.
func RegisterRESTClientMetrics() {
	restClientMetricsOnce.Do(func() {
		crmetrics.RegisterRESTClientMetrics(
			crmetrics.MetricRequestLatency,
			crmetrics.MetricRequestSize,
			crmetrics.MetricResponseSize,
			crmetrics.MetricRateLimiterLatency,
			crmetrics.MetricRequestRetry,
		)
	})
}

// AddProbes wires the manager's liveness and readiness endpoints.
//
// Liveness is a ping: the process answering at all is the whole claim, and a
// liveness probe that depends on an external system turns that system's outage
// into a cluster-wide restart loop. Readiness is where the dependencies go --
// §13 puts the JetStream ping on every service, the SQLite open on indexarr
// and the engine re-attach on grabarr engines -- because an unready pod is
// taken out of Services and left alone.
func AddProbes(mgr ctrl.Manager, ready map[string]healthz.Checker) error {
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("k8s: add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("k8s: add readyz ping check: %w", err)
	}
	for name, check := range ready {
		if err := mgr.AddReadyzCheck(name, check); err != nil {
			return fmt.Errorf("k8s: add readyz check %q: %w", name, err)
		}
	}
	return nil
}
