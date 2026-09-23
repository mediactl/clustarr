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
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The environment variables a Clustarr Deployment sets, each the default for
// the flag named beside it. config/manager and charts/clustarr configure the
// pods this way rather than by argv, so a flag whose default ignored its
// variable would silently run against the wrong endpoint or path.
const (
	// namespaceEnv is the downward-API variable naming the pod's namespace.
	// It is the default for --namespace, and therefore for where the
	// leader-election Lease is created.
	namespaceEnv = "POD_NAMESPACE"

	// natsURLEnv is the JetStream endpoint, the default for --nats-url.
	natsURLEnv = "NATS_URL"

	// indexPathEnv is indexarr's SQLite release index file on the RWO
	// volume, the default for --index-path.
	indexPathEnv = "CLUSTARR_INDEX_PATH"

	// facadeBindAddressEnv is the address indexarr's Torznab facade binds,
	// the default for --facade-bind-address. No manifest sets it -- both
	// installers use the flag's own :8080, the port their Services route --
	// but `clustarr all` reads it too, since its ui already holds :8080.
	facadeBindAddressEnv = "CLUSTARR_FACADE_BIND_ADDRESS"

	// facadeAPIKeySecretEnv names the Secret holding the facade's API keys,
	// the default for --facade-api-key-secret. It is a Secret NAME, never a
	// key: a key in a flag default would print in --help and sit in argv.
	// Both installers set it: config/ to the flag's own default,
	// "indexarr-facade", so the manifest says where the key lives, and the
	// chart to "<release fullname>-indexarr-facade".
	facadeAPIKeySecretEnv = "CLUSTARR_FACADE_API_KEY_SECRET"

	// engineImageEnv is the image grabarr's DownloadClient controller stamps
	// onto the engine StatefulSet/Deployment it creates, the default for
	// --engine-image. config/manager/grabarr.yaml already sets it on the
	// grabarr Deployment.
	engineImageEnv = "CLUSTARR_ENGINE_IMAGE"

	// workerImageEnv and workerImageCUDAEnv are the images squasharr's
	// TranscodeJob controller stamps onto the cpu/intel and nvidia transcode
	// Jobs it creates, the defaults for --worker-image and
	// --worker-image-cuda. config/manager/squasharr.yaml and the chart set
	// both on the squasharr Deployment.
	workerImageEnv     = "CLUSTARR_WORKER_IMAGE"
	workerImageCUDAEnv = "CLUSTARR_WORKER_IMAGE_CUDA"

	// workerServiceAccountEnv is the ServiceAccount those Jobs run as, the
	// default for --worker-service-account. Only the chart sets it: its
	// ServiceAccounts carry the release fullname, while config/'s is the
	// flag's own default.
	workerServiceAccountEnv = "CLUSTARR_WORKER_SERVICE_ACCOUNT"

	// dataClaimEnv is the RWX /data claim those Jobs mount, the default for
	// squasharr's --data-claim, and likewise the claim grabarr's engine
	// workloads mount, the default for grabarr's --data-claim. Only the
	// chart sets it, for the same reason: its claim is "<fullname>-data",
	// while config/'s is both flags' own default, "clustarr-data".
	dataClaimEnv = "CLUSTARR_DATA_CLAIM"

	// traktBaseURLEnv and plexBaseURLEnv point importarr's Trakt and Plex
	// import-list providers at another host, the defaults for
	// --trakt-base-url and --plex-base-url. No shipped manifest sets them --
	// empty is each provider's public API -- but config/e2e does, on both
	// importarr Deployments, to reach test/fixtures/importliststub in a
	// cluster with no egress.
	traktBaseURLEnv = "CLUSTARR_TRAKT_BASE_URL"
	plexBaseURLEnv  = "CLUSTARR_PLEX_BASE_URL"

	// intelRenderGroupsEnv is the default for squasharr's
	// --intel-render-groups: the comma-separated GIDs of the host group
	// owning /dev/dri/renderD* on the Intel GPU nodes, which every Intel
	// transcode Job's pod gets as supplementalGroups. It has no default
	// because the GID is per host install (images/Dockerfile.media's header
	// names the flag); the chart sets it from squasharr.intelRenderGroups.
	intelRenderGroupsEnv = "CLUSTARR_INTEL_RENDER_GROUPS"
)

// envOr returns $name when it is set and non-empty, and fallback otherwise.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

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

	o.NATSURL = envOr(natsURLEnv, o.NATSURL)
	fs.StringVar(&o.NATSURL, "nats-url", o.NATSURL,
		"JetStream endpoint. Defaults to $"+natsURLEnv+".")
	fs.BoolVar(&o.BusSingleNode, "nats-single-node", o.BusSingleNode,
		"Collapse the JetStream topology to one replica, for kind and single-node NATS.")

	fs.DurationVar(&o.GracefulShutdownTimeout, "graceful-shutdown-timeout", o.GracefulShutdownTimeout,
		"How long to let runnables drain on SIGTERM before the manager returns.")

	return &o
}

// bindObservabilityFlags registers the logging and tracing flags every
// subcommand shares and returns the options they write into.
//
// Unlike bindCommonFlags (called once per subcommand, on that subcommand's own
// FlagSet), this is called once on the ROOT command's persistent flags: every
// subcommand -- including `all` -- inherits the same --log-* and --tracing-*
// flags rather than each defining its own copy, and every RunE built from the
// pointers this returns sees the values cobra parsed before dispatching to it.
//
// The two returned pointers must come from a single NewRootCommand call and
// never be shared across calls: a package-level pair would let flag values
// parsed by one `execute` in a test leak into the next.
func bindObservabilityFlags(fs *pflag.FlagSet) (*logging.Options, *tracing.Options) {
	lo := &logging.Options{}
	logging.BindFlags(fs, lo)

	to := &tracing.Options{SampleRatio: 1}
	fs.BoolVar(&to.Enabled, "tracing-enabled", to.Enabled,
		"Export spans over OTLP gRPC. Sampling still runs when this is off; only the exporter is skipped.")
	fs.StringVar(&to.Endpoint, "tracing-endpoint", to.Endpoint,
		`OTLP gRPC collector endpoint, e.g. "otel-collector:4317". Read only when --tracing-enabled.`)
	fs.BoolVar(&to.Insecure, "tracing-insecure", to.Insecure,
		"Disable transport security on the OTLP gRPC connection. Read only when --tracing-enabled.")
	fs.Float64Var(&to.SampleRatio, "tracing-sample-ratio", to.SampleRatio,
		"Fraction (0..1) of root spans sampled. A span whose parent was sampled is always sampled "+
			"regardless of this ratio; collector-side tail sampling is what keeps every erroring span, "+
			"not this SDK-side setting (docs/observability.md).")
	return lo, to
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

// parseGIDs reads a comma-separated list of group ids, such as
// --intel-render-groups' "44,109". Empty is no groups. A negative, a
// non-number or an empty element is an error: a supplementalGroups entry
// the kubelet rejects fails every Job at pod creation, far from the typo.
func parseGIDs(s string) ([]int64, error) {
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
