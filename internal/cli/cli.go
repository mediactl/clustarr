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

// Package cli is the plumbing the cobra binaries (manager, agent, ui) share
// (spec §3.2). It imports only cobra, pflag, pkg/version, pkg/obs/obsflags,
// pkg/fsops and controller-runtime's signals package, so cmd/ui links no
// manager and no pkg/k8s (TestCLIStaysLight).
package cli

import (
	"os"
	"strconv"
)

// The environment variables a Clustarr Deployment sets, each the default for
// the flag named beside it. The installers configure the pods this way rather
// than by argv, so a flag whose default ignored its variable would silently
// run against the wrong endpoint or path.
const (
	// EnvNamespace is the downward-API variable naming the pod's namespace.
	// It is the default for --namespace, and therefore for where the
	// leader-election Lease is created.
	EnvNamespace = "POD_NAMESPACE"

	// EnvNATSURL is the JetStream endpoint, the default for --nats-url.
	EnvNATSURL = "NATS_URL"

	// EnvIndexPath is the index agent's SQLite release index file on the RWO
	// volume, the default for --index-path.
	EnvIndexPath = "CLUSTARR_INDEX_PATH"

	// EnvIndexDSN is a Postgres DSN for the release index, the default for
	// --index-dsn. Non-empty selects relindex.OpenPostgres and --index-path
	// is ignored (spec §A.3); empty keeps SQLite, the default.
	EnvIndexDSN = "CLUSTARR_INDEX_DSN"

	// EnvFacadeBindAddress is the address the index agent's Torznab facade
	// binds, the default for --facade-bind-address.
	EnvFacadeBindAddress = "CLUSTARR_FACADE_BIND_ADDRESS"

	// EnvFacadeAPIKeySecret names the Secret holding the facade's API keys,
	// the default for --facade-api-key-secret. It is a Secret NAME, never a
	// key: a key in a flag default would print in --help and sit in argv.
	EnvFacadeAPIKeySecret = "CLUSTARR_FACADE_API_KEY_SECRET"

	// EnvNativeImage is the image the manager stamps onto the download
	// engines it renders and onto every transcode pool, nvidia's included
	// (there is no CUDA image: docs/adr/0015-no-cuda-image.md), the default
	// for --native-image. It was CLUSTARR_ENGINE_IMAGE and
	// CLUSTARR_WORKER_IMAGE, merged once both became the one native image
	// (spec §3.11).
	EnvNativeImage = "CLUSTARR_NATIVE_IMAGE"

	// EnvGPUNodeLabelNVIDIA and EnvGPUNodeLabelIntel override the node label
	// (set to "true") that marks a GPU node of each class, the defaults for
	// --gpu-node-label-nvidia and --gpu-node-label-intel.
	// pool.DefaultNodeLabelNVIDIA/DefaultNodeLabelIntel already match what
	// the NVIDIA GPU Operator's GPU Feature Discovery and Intel's Node
	// Feature Discovery set by default, but the chart accepts an override
	// for a cluster that labels its GPU nodes differently.
	EnvGPUNodeLabelNVIDIA = "CLUSTARR_GPU_NODE_LABEL_NVIDIA"
	EnvGPUNodeLabelIntel  = "CLUSTARR_GPU_NODE_LABEL_INTEL"

	// EnvDataClaim is the RWX /data claim the transcode pools and the
	// download engines mount, the default for --data-claim. Only the chart
	// sets it: its claim is "<fullname>-data", while config/'s is the flag's
	// own default, "clustarr-data".
	EnvDataClaim = "CLUSTARR_DATA_CLAIM"

	// EnvEngineServiceAccount is the ServiceAccount the download engine pods
	// run as, the default for --engine-service-account. Only the chart sets
	// it, for the reason EnvDataClaim gives: its ServiceAccounts carry the
	// release fullname, while config/'s is the flag's own default,
	// "grabarr-engine".
	EnvEngineServiceAccount = "CLUSTARR_ENGINE_SERVICE_ACCOUNT"

	// EnvTraktBaseURL and EnvPlexBaseURL point the Trakt and Plex import-list
	// providers at another host, the defaults for --trakt-base-url and
	// --plex-base-url. No shipped manifest sets them -- empty is each
	// provider's public API -- but config/e2e does, to reach
	// test/fixtures/importliststub in a cluster with no egress.
	EnvTraktBaseURL = "CLUSTARR_TRAKT_BASE_URL"
	EnvPlexBaseURL  = "CLUSTARR_PLEX_BASE_URL"

	// EnvIntelRenderGroups is the default for --intel-render-groups: the
	// comma-separated GIDs of the host group owning /dev/dri/renderD* on the
	// Intel GPU nodes, which every Intel transcode pool's pod gets as
	// supplementalGroups. It has no default because the GID is per host
	// install; the chart sets it from its values.
	EnvIntelRenderGroups = "CLUSTARR_INTEL_RENDER_GROUPS"

	// EnvCardigannDefinitionsDir is the default for
	// --cardigann-definitions-dir: a mounted directory of Cardigann
	// definitions (what hack/sync-cardigann writes) to load as
	// IndexerDefinitions at startup, in place of the embedded corpus.
	EnvCardigannDefinitionsDir = "CLUSTARR_CARDIGANN_DEFINITIONS_DIR"

	// EnvCardigannBundled is the default for --cardigann-bundled: load the
	// Cardigann corpus compiled into the binary when no
	// --cardigann-definitions-dir is given. Unset means on.
	EnvCardigannBundled = "CLUSTARR_CARDIGANN_BUNDLED"

	// EnvExternalURL is the default for ui's --external-url (design spec
	// §D.1): the absolute base every thumb, art and Image[].url the Plex
	// Custom Metadata Provider hands Plex is built on. An operator's own
	// reachable hostname is not something a default here could ever guess.
	EnvExternalURL = "CLUSTARR_EXTERNAL_URL"

	// EnvArtSigningKey holds the key ui signs its /art/search URLs with
	// (ui.Options.ArtSigningKey). It is an environment variable, never a
	// flag, so the key stays out of the process list; the chart fills it
	// from the Secret it creates once (templates/ui-art-signing-key.yaml).
	EnvArtSigningKey = "CLUSTARR_ART_SIGNING_KEY"

	// EnvExternalMetricsService is the default for the manager's
	// --external-metrics-service: the Service fronting the External Metrics
	// API, named in the serving certificate's SANs and in the APIService's
	// spec.service.name (spec §9.4).
	EnvExternalMetricsService = "CLUSTARR_EXTERNAL_METRICS_SERVICE"

	// EnvExternalMetricsSecret is the default for the manager's
	// --external-metrics-secret: the Secret holding the External Metrics
	// API's self-generated CA and serving certificate (spec §9.4).
	EnvExternalMetricsSecret = "CLUSTARR_EXTERNAL_METRICS_SECRET"
)

// EnvOr returns $name when it is set and non-empty, else fallback.
func EnvOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// EnvBoolOr returns $name parsed as a bool when it parses, else fallback.
func EnvBoolOr(name string, fallback bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(name)); err == nil {
		return v
	}
	return fallback
}
