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
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/captionarr"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/importarr"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// The service entrypoints, as variables so the package's tests can execute a
// command line and inspect the options it produced without starting a manager
// or reaching for a kubeconfig.
var (
	runCatalogarr = catalogarr.Run
	runImportarr  = importarr.Run
	runIndexarr   = indexarr.Run
	runGrabarr    = grabarr.Run
	runSquasharr  = squasharr.Run
	runCaptionarr = captionarr.Run
	runUI         = ui.Run
)

// tracingFor derives the per-service tracing.Options from the root command's
// shared flags, stamping ServiceName so every span this process starts
// carries a resource.service.name distinct from every other Clustarr
// process. common is dereferenced here, once per command execution, after
// cobra has finished parsing -- never stored, so two subcommands (or two
// `execute` calls in a test) never share a mutable tracing.Options.
func tracingFor(common *tracing.Options, serviceName string) tracing.Options {
	to := *common
	to.ServiceName = serviceName
	return to
}

// roleUsage renders a --role help string from a service's own role list, so
// the help can never drift from what Validate accepts.
func roleUsage[R fmt.Stringer](roles []R) string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.String())
	}
	return "What this replica does: " + strings.Join(names, "|") + "."
}

func newCatalogarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := catalogarr.DefaultOptions()
	var role string

	cmd := &cobra.Command{
		Use:   "catalogarr",
		Short: "Inventory, metadata and release decisions",
		Long: "catalogarr owns catalog.clustarr.io: inventory, the metadata gateway and\n" +
			"release decisions (search, grab, rss-matcher).\n\n" +
			"Everything entering the library -- root-folder rescan, import lists and\n" +
			"completed-download import -- belongs to `clustarr importarr` (amendment §A1.2,\n" +
			"§A1.3), not here.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(),
		roleUsage(catalogarr.Roles())+
			" §3 puts the controllers, the queue workers and the history sink in one"+
			" Deployment and the metadata gateway in its own, so this one accepts a"+
			" comma-separated combination too.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runCatalogarr(cmd.Context(), catalogarr.Options{
			Options: *common,
			Role:    catalogarr.Role(role),
			Logging: *lo,
			Tracing: tracingFor(to, catalogarr.ServiceName),
		})
	}
	return cmd
}

func newIndexarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := indexarr.DefaultOptions()
	var (
		role         string
		indexPath    string
		facade       string
		facadeSecret string
		definitions  string
		bundled      bool
	)

	cmd := &cobra.Command{
		Use:   "indexarr",
		Short: "Aggregate remote indexers into one search engine",
		Long: "indexarr owns index.clustarr.io: indexer definitions, the search RPC, the RSS\n" +
			"worker, the SQLite release index and the Torznab facade.\n\n" +
			"It runs as exactly one replica with a Recreate strategy: the release index is a\n" +
			"SQLite database on an RWO volume, and a second writer would corrupt it.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(indexarr.Roles()))
	cmd.Flags().StringVar(&indexPath, "index-path", envOr(indexPathEnv, defaults.IndexPath),
		"SQLite release index file, on the RWO volume. Defaults to $"+indexPathEnv+".")
	cmd.Flags().StringVar(&facade, "facade-bind-address", envOr(facadeBindAddressEnv, defaults.FacadeBindAddress),
		`Address the Torznab facade binds to. "0" disables it. Defaults to $`+facadeBindAddressEnv+".")
	cmd.Flags().StringVar(&facadeSecret, "facade-api-key-secret",
		envOr(facadeAPIKeySecretEnv, defaults.FacadeAPIKeySecret),
		"Secret in --namespace whose every non-blank entry is an API key the Torznab facade accepts; "+
			"created with one random key under \""+indexarr.FacadeAPIKeyField+"\" when absent. The facade "+
			"never serves without a key. Defaults to $"+facadeAPIKeySecretEnv+".")
	cmd.Flags().StringVar(&definitions, "cardigann-definitions-dir", envOr(cardigannDefinitionsDirEnv, ""),
		"Directory of Cardigann definition files (what hack/sync-cardigann writes) to load as IndexerDefinitions "+
			"at startup, so an Indexer's spec.definition can name any of them. When set it replaces the embedded "+
			"corpus. Defaults to $"+cardigannDefinitionsDirEnv+".")
	cmd.Flags().BoolVar(&bundled, "cardigann-bundled", envBoolOr(cardigannBundledEnv, true),
		"Load the Cardigann definitions compiled into the binary as IndexerDefinitions when "+
			"--cardigann-definitions-dir is empty. Defaults to $"+cardigannBundledEnv+", else true.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runIndexarr(cmd.Context(), indexarr.Options{
			Options:                 *common,
			Role:                    indexarr.Role(role),
			IndexPath:               indexPath,
			FacadeBindAddress:       facade,
			FacadeAPIKeySecret:      facadeSecret,
			CardigannDefinitionsDir: definitions,
			CardigannBundled:        bundled,
			Logging:                 *lo,
			Tracing:                 tracingFor(to, indexarr.ServiceName),
		})
	}
	return cmd
}

func newGrabarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := grabarr.DefaultOptions()
	var (
		role        string
		engine      string
		dataDir     string
		scratch     string
		engineImage string
		dataClaim   string
		engineSA    string
	)

	cmd := &cobra.Command{
		Use:   "grabarr",
		Short: "Download clients and the torrent and usenet engines",
		Long: "grabarr owns download.clustarr.io: the DownloadClient and Download controllers,\n" +
			"and the embedded engines that do the transfers. An engine pod runs the same\n" +
			"binary with --role torrent-engine or --role usenet-engine and an --engine\n" +
			"identity, and watches only the Downloads labelled for it.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(grabarr.Roles()))
	cmd.Flags().StringVar(&engine, "engine", defaults.Engine,
		"This engine's identity, \"<client>-<ordinal>\". Required for an engine role.")
	cmd.Flags().StringVar(&dataDir, "data-dir", defaults.DataDir,
		"RWX media volume.")
	cmd.Flags().StringVar(&scratch, "scratch-dir", defaults.ScratchDir,
		"Usenet engine working area for yEnc assembly, PAR2 repair and extraction.")
	cmd.Flags().StringVar(&engineImage, "engine-image", envOr(engineImageEnv, defaults.EngineImage),
		"Image the DownloadClient controller stamps onto the engine StatefulSet/Deployment "+
			"it creates. Required for --role controller. Defaults to $"+engineImageEnv+".")
	cmd.Flags().StringVar(&dataClaim, "data-claim", envOr(dataClaimEnv, defaults.DataClaimName),
		"RWX PersistentVolumeClaim the engine workloads the controller creates mount at --data-dir. "+
			"Defaults to $"+dataClaimEnv+".")
	cmd.Flags().StringVar(&engineSA, "engine-service-account",
		envOr(engineServiceAccountEnv, defaults.EngineServiceAccount),
		"ServiceAccount the engine pods the controller creates run as; it must hold the engine's RBAC "+
			"(config/rbac/grabarr_engine_role.yaml). Defaults to $"+engineServiceAccountEnv+", then "+
			defaults.EngineServiceAccount+".")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runGrabarr(cmd.Context(), grabarr.Options{
			Options:              *common,
			Role:                 grabarr.Role(role),
			Engine:               engine,
			DataDir:              dataDir,
			ScratchDir:           scratch,
			EngineImage:          engineImage,
			DataClaimName:        dataClaim,
			EngineServiceAccount: engineSA,
			Logging:              *lo,
			Tracing:              tracingFor(to, grabarr.ServiceName),
		})
	}
	return cmd
}

func newSquasharrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := squasharr.DefaultOptions()
	var (
		role            string
		slots           string
		dataDir         string
		workerImage     string
		workerImageCUDA string
		workerAccount   string
		dataClaim       string
		renderGroups    string
		labelNVIDIA     string
		labelIntel      string
	)

	cmd := &cobra.Command{
		Use:   "squasharr",
		Short: "Distributed HEVC 10-bit transcoding",
		Long: "squasharr owns transcode.clustarr.io: it watches MediaFiles for non-compliant\n" +
			"video and dispatches HEVC 10-bit / AAC transcodes, admitted against a\n" +
			"per-hardware slot budget, to per-profile worker pools over NATS. The pool\n" +
			"pods run the squasharr-worker binary.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(squasharr.Roles()))
	cmd.Flags().StringVar(&slots, "slots", squasharr.FormatSlots(defaults.Slots),
		"Concurrent transcode budget per hardware class, e.g. cpu=2,nvidia=1,intel=1. "+
			"A budget of 0 means that class is never admitted.")
	cmd.Flags().StringVar(&dataDir, "data-dir", defaults.DataDir,
		"RWX media volume.")
	cmd.Flags().StringVar(&workerImage, "worker-image", envOr(workerImageEnv, defaults.WorkerImage),
		"Image the controller stamps onto cpu and intel transcode pools. Required for --role controller. "+
			"Defaults to $"+workerImageEnv+".")
	cmd.Flags().StringVar(&workerImageCUDA, "worker-image-cuda", envOr(workerImageCUDAEnv, defaults.WorkerImageCUDA),
		"Image the controller stamps onto nvidia transcode pools; empty uses --worker-image. "+
			"Defaults to $"+workerImageCUDAEnv+".")
	cmd.Flags().StringVar(&workerAccount, "worker-service-account",
		envOr(workerServiceAccountEnv, defaults.WorkerServiceAccount),
		"ServiceAccount transcode Job pods run as; it must hold the worker's RBAC "+
			"(config/rbac/squasharr_worker_role.yaml). Defaults to $"+workerServiceAccountEnv+", then "+
			squasharr.DefaultWorkerServiceAccount+".")
	cmd.Flags().StringVar(&dataClaim, "data-claim", envOr(dataClaimEnv, defaults.DataClaimName),
		"RWX PersistentVolumeClaim transcode Jobs mount at --data-dir. Defaults to $"+dataClaimEnv+".")
	cmd.Flags().StringVar(&renderGroups, "intel-render-groups", envOr(intelRenderGroupsEnv, ""),
		"Comma-separated GIDs every Intel transcode Job's pod gets as supplementalGroups: the host group "+
			"owning /dev/dri/renderD* on the Intel GPU nodes, e.g. 109 or 44,109. It varies per host install, "+
			"so there is no default; empty relies on the container runtime's "+
			"device_ownership_from_security_context. Defaults to $"+intelRenderGroupsEnv+".")
	cmd.Flags().StringVar(&labelNVIDIA, "gpu-node-label-nvidia", defaults.NodeLabelNVIDIA,
		"Node label that, set to \"true\", marks an NVIDIA GPU node; the NVIDIA GPU Operator's GPU Feature "+
			"Discovery sets the default. hardware: auto sends work to a profile's nvidia pool only while a Ready node "+
			"carries it with allocatable nvidia.com/gpu, and nvidia pools are held to it.")
	cmd.Flags().StringVar(&labelIntel, "gpu-node-label-intel", defaults.NodeLabelIntel,
		"Node label that, set to \"true\", marks an Intel GPU node; Node Feature Discovery's rules from the "+
			"Intel Device Plugins Operator set the default. hardware: auto sends work to a profile's intel pool only "+
			"while a Ready node carries it with allocatable gpu.intel.com/i915, and intel pools are held to it.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		budget, err := squasharr.ParseSlots(slots)
		if err != nil {
			return err
		}
		gids, err := parseGIDs(renderGroups)
		if err != nil {
			return fmt.Errorf("--intel-render-groups: %w", err)
		}
		return runSquasharr(cmd.Context(), squasharr.Options{
			Options:              *common,
			Role:                 squasharr.Role(role),
			Slots:                budget,
			DataDir:              dataDir,
			WorkerImage:          workerImage,
			WorkerImageCUDA:      workerImageCUDA,
			WorkerServiceAccount: workerAccount,
			DataClaimName:        dataClaim,
			IntelRenderGroups:    gids,
			NodeLabelNVIDIA:      labelNVIDIA,
			NodeLabelIntel:       labelIntel,
			Logging:              *lo,
			Tracing:              tracingFor(to, squasharr.ServiceName),
		})
	}
	return cmd
}

func newCaptionarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := captionarr.DefaultOptions()
	var (
		role    string
		dataDir string
	)

	cmd := &cobra.Command{
		Use:   "captionarr",
		Short: "Distributed subtitle planning and fetching",
		Long: "captionarr owns subtitle.clustarr.io: it plans which subtitles a MediaFile still\n" +
			"wants and fetches them. The fetch workers share a key/value token bucket, so any\n" +
			"number of them stays inside a provider's rate limit.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(captionarr.Roles()))
	cmd.Flags().StringVar(&dataDir, "data-dir", defaults.DataDir,
		"RWX media volume; sidecars are written next to their video file.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runCaptionarr(cmd.Context(), captionarr.Options{
			Options: *common,
			Role:    captionarr.Role(role),
			DataDir: dataDir,
			Logging: *lo,
			Tracing: tracingFor(to, captionarr.ServiceName),
		})
	}
	return cmd
}

func newImportarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := importarr.DefaultOptions()
	var (
		role           string
		dataPath       string
		sampleMaxBytes int64
		traktBaseURL   string
		plexBaseURL    string
	)

	cmd := &cobra.Command{
		Use:   "importarr",
		Short: "Root-folder rescan, import lists and completed-download import",
		Long: "importarr owns everything entering the library: root-folder rescan, import\n" +
			"lists and completed-download import. It owns no CRD group of its own; its\n" +
			"controllers and workers read and write catalog.clustarr.io and\n" +
			"download.clustarr.io resources under their own field manager (amendment §A1.6).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(importarr.Roles()))
	cmd.Flags().StringVar(&dataPath, "data-path", defaults.DataPath,
		"RWX media volume, mounted by the importarr-worker Deployment. The controller "+
			"Deployment does not mount it and only needs this to be non-empty.")
	cmd.Flags().Int64Var(&sampleMaxBytes, "sample-max-bytes", defaults.SampleMaxBytes,
		"Video size floor, in bytes: a video file smaller than this whose name does not mark it a "+
			"sample is a suspected sample, listed as unmatched by a rescan and rejected by a "+
			"completed-download import unless the import is manual. 0 disables the size rule.")
	cmd.Flags().StringVar(&traktBaseURL, "trakt-base-url", envOr(traktBaseURLEnv, ""),
		"Trakt API the import lists reach, for both the controller's device-code flow and the worker's syncs. "+
			"Empty is https://api.trakt.tv. Defaults to $"+traktBaseURLEnv+".")
	cmd.Flags().StringVar(&plexBaseURL, "plex-base-url", envOr(plexBaseURLEnv, ""),
		"Plex Discover API the import lists' Plex watchlist syncs reach. Empty is Plex's own. "+
			"Defaults to $"+plexBaseURLEnv+".")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		// Every field is passed explicitly: this is a bare literal, not
		// importarr.DefaultOptions(), so a field left out here is its zero
		// value. For SampleMaxBytes that zero is not a missing default but
		// a working setting -- the sample size rule silently switched off
		// on every importarr replica.
		return runImportarr(cmd.Context(), importarr.Options{
			Options:        *common,
			Role:           importarr.Role(role),
			DataPath:       dataPath,
			SampleMaxBytes: sampleMaxBytes,
			TraktBaseURL:   traktBaseURL,
			PlexBaseURL:    plexBaseURL,
			Logging:        *lo,
			Tracing:        tracingFor(to, importarr.ServiceName),
		})
	}
	return cmd
}

// buildUICluster attempts to build ui's two seams onto the cluster -- the
// informer-backed reader every page reads through, and the *actions.Actions
// every button writes through -- and reports whether it succeeded through
// the returned values themselves, never through an error: a cluster is
// optional for ui (Task D3-0's brief: "Do not make a cluster connection
// mandatory"), so the ordinary case for a developer running `clustarr ui`
// with no kubeconfig is not a failure at all. Any problem here is logged and
// swallowed; the nils it returns on that path are exactly what
// ui.Options.Reader, WaitForSync and Actions being unset already mean -- see
// ui.NewServer's defaulting, and actions.ErrNoWriter.
//
// The writer is a plain client.Client over the same config and ui's own
// scheme (ui.NewReaderScheme: corev1 plus the five Clustarr groups, which
// covers every kind ui/actions creates or patches). It goes straight into
// actions.New and is never handed to ui by any other route: ui.Options.Reader
// stays a client.Reader, and *actions.Actions holds its writer unexported, so
// the only writes ui can make are the actions ui/actions defines
// (ui/guard_test.go, ruling R2). It is uncached on purpose: ui/actions only
// creates and patches, and a create or a merge patch goes to the apiserver
// whatever client sends it. Until Task G3-5 nothing built it at all, so every
// action answered ErrNoWriter in production.
//
// It uses ctrl.LoggerFrom rather than pkg/obs/logging (which cmd/clustarr
// otherwise never imports): both call sites run this before the service's
// own obs.Bootstrap has installed a logger on ctx, matching the pattern
// cmd/clustarr/all.go's runAll already uses for the same reason.
func buildUICluster(ctx context.Context) (client.Reader, func(context.Context) bool, *actions.Actions) {
	log := ctrl.LoggerFrom(ctx).WithName("ui")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Info("no cluster reachable; ui will serve empty pages and refuse every action",
			"error", err.Error())
		return nil, nil, nil
	}

	scheme, err := ui.NewReaderScheme()
	if err != nil {
		log.Error(err, "build ui reader scheme")
		return nil, nil, nil
	}

	reader, waitForSync, err := ui.NewClusterReader(ctx, cfg, scheme)
	if err != nil {
		log.Error(err, "build ui cluster reader")
		return nil, nil, nil
	}

	writer, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "build ui action writer; ui will serve its pages and refuse every action")
		return reader, waitForSync, nil
	}
	return reader, waitForSync, actions.New(writer)
}

// buildUIProjection builds and starts Task D3-1's shared pipeline
// projection loop (ui/projection.Projection) over reader and returns it so
// the caller can wire every page's accessor and every stream's Subscribe*
// in ui.Options to the same instance -- one list round feeding the initial
// render of each page and every open SSE connection (design plan ruling
// R4). ui_projection_wiring_test.go and ui_options_wiring_test.go hold both
// call sites to wiring every one of them.
//
// Starting it unconditionally, even over a nil reader, keeps both `clustarr
// ui` call sites (this file's newUICommand and all.go's allServices)
// identical: Projection already treats a nil reader as "project nothing"
// (ui/projection/projection.go), the same as ui.Options.Entries being unset
// ever meant.
func buildUIProjection(ctx context.Context, reader client.Reader) *projection.Projection {
	proj := projection.New(reader, projection.DefaultInterval)
	go func() {
		if err := proj.Run(ctx); err != nil && ctx.Err() == nil {
			ctrl.LoggerFrom(ctx).WithName("ui").Error(err, "pipeline projection loop stopped")
		}
	}()
	return proj
}

func newUICommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	var bindAddress string
	var authMode string

	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Server-rendered web UI",
		Long: "ui is the server-rendered web UI (templ + htmx + SSE): it never writes status and\n" +
			"owns no CRD of its own, so it takes no --role. Its authentication mode is chosen\n" +
			"explicitly with --auth-mode and it refuses to serve without one; the only mode,\n" +
			"anonymous, serves every request without a login and must sit behind ingress\n" +
			"authentication (design amendment §A3.5).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	cmd.Flags().StringVar(&bindAddress, "bind-address", ui.DefaultBindAddress,
		"Address the HTTP server listens on. Serves /healthz, /readyz, the Pipeline page and "+
			"its SSE stream on this one address -- ui runs no separate metrics or health port.")
	cmd.Flags().StringVar(&authMode, "auth-mode", "",
		"Authentication mode, chosen explicitly: ui refuses to serve without one. The only mode is "+
			"anonymous, which serves every request without a login and must sit behind ingress "+
			"authentication (design amendment §A3.5).")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		reader, waitForSync, acts := buildUICluster(ctx)
		proj := buildUIProjection(ctx, reader)
		// Every cluster-derived field, in the same order as all.go's ui
		// closure; ui_options_wiring_test.go executes both commands and fails
		// on any func, pointer or interface field of ui.Options left nil.
		return runUI(ctx, ui.Options{
			BindAddress:          bindAddress,
			AuthMode:             ui.AuthMode(authMode),
			Reader:               reader,
			WaitForSync:          waitForSync,
			Projected:            proj.Projected,
			Actions:              acts,
			Entries:              proj.Entries,
			Subscribe:            proj.Subscribe,
			SubscribeDownloads:   proj.SubscribeDownloads,
			Library:              proj.Library,
			SubscribeLibrary:     proj.SubscribeLibrary,
			Unmatched:            proj.Unmatched,
			SubscribeUnmatched:   proj.SubscribeUnmatched,
			ImportLists:          proj.ImportLists,
			SubscribeImportLists: proj.SubscribeImportLists,
			Logging:              *lo,
			Tracing:              tracingFor(to, "ui"),
		})
	}
	return cmd
}
