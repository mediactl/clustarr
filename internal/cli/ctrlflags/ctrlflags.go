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

// Package ctrlflags binds the controller-runtime flags the manager and agent
// binaries share onto a pflag.FlagSet, and registers the clustarr_ collectors
// once per process (spec §3.2). cmd/ui never imports it: it pulls pkg/k8s.
package ctrlflags

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/pflag"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// BindManager binds all eleven controller-runtime flags, with --leader-elect
// on by default: every controller manifest passed it (spec §3.4.1).
func BindManager(fs *pflag.FlagSet) *k8s.Options { return bind(fs, true) }

// BindAgent binds eight of them: an agent never elects and never ensures the
// topology, so --leader-elect, --leader-election-namespace and
// --nats-single-node are not flags at all (spec §3.5.1).
func BindAgent(fs *pflag.FlagSet) *k8s.Options { return bind(fs, false) }

// bind registers the flags and returns the options they write into. The
// returned pointer is filled by cobra during flag parsing, so a RunE reads it
// after Execute has parsed, not before.
func bind(fs *pflag.FlagSet, manager bool) *k8s.Options {
	o := k8s.DefaultOptions()
	o.Namespace = os.Getenv(cli.EnvNamespace)
	if manager {
		o.LeaderElect = true
	}

	fs.StringVar(&o.MetricsBindAddress, "metrics-bind-address", o.MetricsBindAddress,
		`Address the Prometheus endpoint binds to. "0" disables it.`)
	fs.BoolVar(&o.MetricsSecure, "metrics-secure", o.MetricsSecure,
		"Serve metrics over HTTPS. Turn this off only for local debugging.")
	fs.StringVar(&o.HealthProbeBindAddress, "health-probe-bind-address", o.HealthProbeBindAddress,
		`Address /healthz and /readyz bind to. "0" disables them, and with them the Kubernetes probes.`)
	fs.StringVar(&o.PprofBindAddress, "pprof-bind-address", o.PprofBindAddress,
		"Address net/http/pprof binds to. Empty disables it.")

	if manager {
		fs.BoolVar(&o.LeaderElect, "leader-elect", o.LeaderElect,
			"Take the leader lease before running controllers, so only one replica reconciles.")
		fs.StringVar(&o.LeaderElectionNamespace, "leader-election-namespace", o.LeaderElectionNamespace,
			"Namespace holding the leader-election Lease. Defaults to --namespace.")
	}

	fs.StringVar(&o.Namespace, "namespace", o.Namespace,
		"Namespace this process runs in. Defaults to $"+cli.EnvNamespace+".")
	fs.StringSliceVar(&o.WatchNamespaces, "watch-namespace", o.WatchNamespaces,
		"Restrict the cache, and so every controller, to these namespaces. Empty watches the cluster. "+
			"Repeatable or comma-separated.")

	o.NATSURL = cli.EnvOr(cli.EnvNATSURL, o.NATSURL)
	fs.StringVar(&o.NATSURL, "nats-url", o.NATSURL,
		"JetStream endpoint. Defaults to $"+cli.EnvNATSURL+".")
	if manager {
		fs.BoolVar(&o.BusSingleNode, "nats-single-node", o.BusSingleNode,
			"Collapse the JetStream topology to one replica, for kind and single-node NATS.")
	}

	fs.DurationVar(&o.GracefulShutdownTimeout, "graceful-shutdown-timeout", o.GracefulShutdownTimeout,
		"How long to let runnables drain on SIGTERM before the manager returns.")

	return &o
}

var (
	registerOnce sync.Once
	registerErr  error
)

// RegisterMetrics registers the clustarr_ collectors in controller-runtime's
// registry once per process, so repeated Execute calls in one test binary
// stay safe: registering the same collector set twice returns a
// prometheus.AlreadyRegisteredError.
func RegisterMetrics() error {
	registerOnce.Do(func() { registerErr = metrics.Register(ctrlmetrics.Registry) })
	return registerErr
}

// ParseGIDs reads a comma-separated list of group ids, such as
// --intel-render-groups' "44,109". Empty is no groups. A negative, a
// non-number or an empty element is an error: a supplementalGroups entry
// the kubelet rejects fails every Job at pod creation, far from the typo.
func ParseGIDs(s string) ([]int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(s, ",") {
		gid, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || gid < 0 {
			return nil, fmt.Errorf("%q is not a list of group ids (element %q)", s, part)
		}
		out = append(out, gid)
	}
	return out, nil
}
