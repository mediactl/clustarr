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
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// namespaceEnv is the downward-API variable a Clustarr Deployment sets. It is
// the default for --namespace, and therefore for where the leader-election
// Lease is created.
const namespaceEnv = "POD_NAMESPACE"

// bindCommonFlags registers the flags every `clustarr <service>` accepts and
// returns the options they write into.
//
// The returned pointer is filled by cobra during flag parsing, so a command's
// RunE reads it after Execute has parsed, not before.
func bindCommonFlags(fs *pflag.FlagSet) *k8s.Options {
	o := k8s.DefaultOptions()
	o.Namespace = os.Getenv(namespaceEnv)

	fs.StringVar(&o.MetricsBindAddress, "metrics-bind-address", o.MetricsBindAddress,
		`Address the Prometheus endpoint binds to. "0" disables it.`)
	fs.BoolVar(&o.MetricsSecure, "metrics-secure", o.MetricsSecure,
		"Serve metrics over HTTPS. Turn this off only for local debugging.")
	fs.StringVar(&o.HealthProbeBindAddress, "health-probe-bind-address", o.HealthProbeBindAddress,
		`Address /healthz and /readyz bind to. "0" disables them, and with them the Kubernetes probes.`)
	fs.StringVar(&o.PprofBindAddress, "pprof-bind-address", o.PprofBindAddress,
		"Address net/http/pprof binds to. Empty disables it.")

	fs.BoolVar(&o.LeaderElect, "leader-elect", o.LeaderElect,
		"Take a leader lease before running controllers, so only one replica reconciles. "+
			"Worker and engine roles ignore it: they are meant to run on every replica.")
	fs.StringVar(&o.LeaderElectionNamespace, "leader-election-namespace", o.LeaderElectionNamespace,
		"Namespace holding the leader-election Lease. Defaults to --namespace.")

	fs.StringVar(&o.Namespace, "namespace", o.Namespace,
		"Namespace this process runs in. Defaults to $"+namespaceEnv+".")
	fs.StringSliceVar(&o.WatchNamespaces, "watch-namespace", o.WatchNamespaces,
		"Restrict the cache, and so every controller, to these namespaces. Empty watches the cluster. "+
			"Repeatable or comma-separated.")

	fs.StringVar(&o.NATSURL, "nats-url", o.NATSURL,
		"JetStream endpoint.")
	fs.BoolVar(&o.BusSingleNode, "nats-single-node", o.BusSingleNode,
		"Collapse the JetStream topology to one replica, for kind and single-node NATS.")

	fs.DurationVar(&o.GracefulShutdownTimeout, "graceful-shutdown-timeout", o.GracefulShutdownTimeout,
		"How long to let runnables drain on SIGTERM before the manager returns.")

	return &o
}

// offsetAddress shifts the port of a "host:port" bind address by n, leaving
// the disable sentinel and an empty address alone.
//
// `clustarr all` runs five managers in one process and each wants its own
// metrics and probe listeners; without this they would race for one port and
// four of the five would fail to bind.
func offsetAddress(addr string, n int) (string, error) {
	if n == 0 || addr == "" || addr == k8s.DisabledBindAddress {
		return addr, nil
	}
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", fmt.Errorf("bind address %q has no port to offset", addr)
	}
	port, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return "", fmt.Errorf("bind address %q has a non-numeric port: %w", addr, err)
	}
	shifted := port + n
	if shifted < 1 || shifted > 65535 {
		return "", fmt.Errorf("offsetting %q by %d leaves port %d out of range", addr, n, shifted)
	}
	return addr[:i+1] + strconv.Itoa(shifted), nil
}
