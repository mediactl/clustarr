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
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/captionarr"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/importarr"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr"
	"github.com/mediactl/clustarr/ui"
)

// allProcessServiceName is every service's Tracing.ServiceName under `clustarr
// all`. pkg/obs/tracing.Setup installs the process-wide TracerProvider once
// (sync.Once, guarding against otel's global, unsynchronized
// SetTracerProvider); whichever of the seven goroutines below calls it first
// wins for the life of the process, so naming the resource after any one
// service's own name would be an arbitrary, misleading pick. "clustarr" is
// the one name that is true regardless of which goroutine wins the race.
const allProcessServiceName = "clustarr"

// devEngineImage is `clustarr all`'s fallback for grabarr's --engine-image
// when $CLUSTARR_ENGINE_IMAGE is unset, matching the tag every
// config/manager/*.yaml manifest already uses for local/kind development.
// It is deliberately NOT grabarr.DefaultOptions' own default -- the package
// doc on downloadclient.Reconciler.EngineImage is explicit that a real
// cluster deployment must set this itself ("guessing an image tag would
// silently run the wrong engine") -- but `all` is the one entry point that
// is BY DEFINITION dev/kind only (this function's own doc comment), the same
// reasoning devIndexPath below already applies to indexarr's IndexPath.
const devEngineImage = "ghcr.io/mediactl/clustarr/media:dev"

// devFacadeBindAddress is `clustarr all`'s address for indexarr's Torznab
// facade when $CLUSTARR_FACADE_BIND_ADDRESS is unset. It is not indexarr's
// own :8080 because ui keeps that address in this one process (see
// allServices); :9696 is Prowlarr's port.
const devFacadeBindAddress = ":9696"

// allServices is what `clustarr all` starts, in the order it starts them.
//
// Each entry gets its own port offset because seven managers in one process
// would otherwise race for one metrics port and one probe port -- ui is the
// exception: it has no controller-runtime manager and so no metrics or
// health port to offset, and binds --ui-bind-address. grabarr and squasharr
// run their controller role only: the engines and the transcode worker are
// separate pods in a real deployment, and running them here would need
// volumes and images this mode does not have. captionarr runs its controllers
// and its fetch worker (RoleAll), so subtitles are fetched here too.
//
// lo and to are the root command's shared --log-*/--tracing-* options
// (see bindObservabilityFlags): every service gets the same lo, and the same
// to except for ServiceName, which is forced to allProcessServiceName for
// the reason given on that constant. uiAddr is --ui-bind-address and
// uiAuthMode is --ui-auth-mode, ui's explicit authentication mode (§A3.5).
func allServices(lo *logging.Options, to *tracing.Options, uiAddr string, uiAuthMode ui.AuthMode) []struct {
	name string
	run  func(ctx context.Context, o k8s.Options) error
} {
	tr := *to
	tr.ServiceName = allProcessServiceName

	return []struct {
		name string
		run  func(ctx context.Context, o k8s.Options) error
	}{
		// Every service starts from its own DefaultOptions() and only then
		// overrides what `all` owns. A bare struct literal looks equivalent
		// but silently drops every default the service defines for itself:
		// importarr's DataPath is the one that bit us, because
		// importarr.Options.Validate rejects an empty --data-path and
		// runAll cancels the whole stack on the first failure, so `clustarr
		// all` exited immediately with "importarr: --data-path is
		// required". Keep the DefaultOptions() pattern even where a service
		// has no extra defaults today.
		{"catalogarr", func(ctx context.Context, o k8s.Options) error {
			d := catalogarr.DefaultOptions()
			d.Options = o
			d.Role = catalogarr.RoleAll
			d.Logging = *lo
			d.Tracing = tr
			return runCatalogarr(ctx, d)
		}},
		{"importarr", func(ctx context.Context, o k8s.Options) error {
			d := importarr.DefaultOptions()
			d.Options = o
			d.Role = importarr.RoleAll
			// The same environment newImportarrCommand's two flags default
			// from, so a dev stack pointed at a Trakt/Plex fixture reaches
			// it the same way a Deployment does.
			d.TraktBaseURL = envOr(traktBaseURLEnv, "")
			d.PlexBaseURL = envOr(plexBaseURLEnv, "")
			d.Logging = *lo
			d.Tracing = tr
			return runImportarr(ctx, d)
		}},
		{"indexarr", func(ctx context.Context, o k8s.Options) error {
			d := indexarr.DefaultOptions()
			d.Options = o
			// indexarr.DefaultIndexPath is /var/lib/clustarr/index, which is
			// the PVC's mountPath in config/manager/indexarr.yaml and is
			// pinned to it byte-for-byte by TestDefaultIndexPathMatchesTheManifest.
			// `all` is the DEV entry point and has no such mount, so
			// relindex.Open's MkdirAll under /var/lib fails for an
			// unprivileged user -- and indexarr.Run returns that error, which
			// runAll below turns into a cancel() that stops ALL SEVEN
			// services. `clustarr all` would not start at all.
			//
			// The fix belongs here rather than in indexarr.Options, and the
			// distinction matters: in-cluster, Open failing on an unwritable
			// PVC is exactly the behaviour we want, because a silent fallback
			// would let indexarr come up Ready and serve searches from an
			// index that vanishes on restart, hiding a broken mount
			// indefinitely. Only the dev entry point chooses a dev path.
			d.IndexPath = devIndexPath()
			// The Torznab facade cannot take its default :8080 here: ui,
			// below, already binds :8080 in this same process, and runAll
			// would cancel the whole stack on the second bind. The dev
			// default is Prowlarr's :9696, the port every Torznab client
			// already expects an indexer aggregator on.
			d.FacadeBindAddress = envOr(facadeBindAddressEnv, devFacadeBindAddress)
			d.FacadeAPIKeySecret = envOr(facadeAPIKeySecretEnv, d.FacadeAPIKeySecret)
			// A dev stack loads Cardigann definitions the way a Deployment
			// does: from the directory $CLUSTARR_CARDIGANN_DEFINITIONS_DIR
			// names, else the embedded corpus unless
			// $CLUSTARR_CARDIGANN_BUNDLED turns it off.
			d.CardigannDefinitionsDir = envOr(cardigannDefinitionsDirEnv, "")
			d.CardigannBundled = envBoolOr(cardigannBundledEnv, true)
			// The facade keeps its API key in a Secret in indexarr's own
			// namespace (indexarr.Options.Validate), and a dev shell with no
			// $POD_NAMESPACE and no --namespace has none to name. `all` is
			// the one entry point where that is ordinary rather than a
			// misconfiguration, so it runs without the facade instead of
			// refusing to start every service; pass --namespace to get it.
			if o.Namespace == "" {
				d.FacadeBindAddress = k8s.DisabledBindAddress
			}
			d.Logging = *lo
			d.Tracing = tr
			return runIndexarr(ctx, d)
		}},
		{"grabarr", func(ctx context.Context, o k8s.Options) error {
			d := grabarr.DefaultOptions()
			d.Options = o
			// `all` runs the controller role only (see this function's own
			// doc comment), which needs an engine image to stamp onto the
			// StatefulSet/Deployment it creates even though this process
			// never runs an engine itself. $CLUSTARR_ENGINE_IMAGE wins when
			// set, the same as newGrabarrCommand's --engine-image flag;
			// devEngineImage is the fallback a real cluster deployment never
			// takes, because every config/manager/*.yaml manifest sets the
			// env var explicitly.
			d.EngineImage = envOr(engineImageEnv, devEngineImage)
			d.Logging = *lo
			d.Tracing = tr
			return runGrabarr(ctx, d)
		}},
		{"squasharr", func(ctx context.Context, o k8s.Options) error {
			d := squasharr.DefaultOptions()
			d.Options = o
			// The controller role stamps a worker image onto every
			// transcode Job it creates, exactly as grabarr's stamps an
			// engine image above; the same env-then-dev-default fallback.
			d.WorkerImage = envOr(workerImageEnv, devEngineImage)
			d.WorkerImageCUDA = envOr(workerImageCUDAEnv, "")
			d.Logging = *lo
			d.Tracing = tr
			return runSquasharr(ctx, d)
		}},
		{"captionarr", func(ctx context.Context, o k8s.Options) error {
			d := captionarr.DefaultOptions()
			d.Options = o
			// Controllers AND the fetch worker: the controller role alone
			// plans each SubtitleRequest and publishes its fetch tasks to a
			// queue nothing in this process consumes, so `clustarr all`
			// never fetched a subtitle. RoleAll runs setupWorkers too, which
			// gives the provider builder the shared throttle KV, so the
			// OpenSubtitles login is shared exactly as across worker pods.
			d.Role = captionarr.RoleAll
			d.Logging = *lo
			d.Tracing = tr
			return runCaptionarr(ctx, d)
		}},
		{"ui", func(ctx context.Context, _ k8s.Options) error {
			// ui has no k8s.Options of its own -- no manager, no CRD, no
			// ports to offset -- so it binds --ui-bind-address rather than
			// an offset of the probe port (design spec §2: `clustarr all`
			// runs every service). It still
			// takes the real ctx: runAll cancels ctx on any other service's
			// failure, and ui.Run's own shutdown depends on that
			// cancellation to stop its HTTP server. The same ctx bounds the
			// cluster reader buildUICluster may build, so it stops on the
			// same cancellation too.
			//
			// Every cluster-derived field, in the same order as
			// newUICommand's; ui_options_wiring_test.go executes both
			// commands and fails on any func, pointer or interface field of
			// ui.Options left nil.
			reader, waitForSync, acts := buildUICluster(ctx)
			proj := buildUIProjection(ctx, reader)
			return runUI(ctx, ui.Options{
				BindAddress:          uiAddr,
				AuthMode:             uiAuthMode,
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
				Tracing:              tr,
			})
		}},
	}
}

func newAllCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Run every service in one process, for kind and development",
		Long: "all starts catalogarr, importarr, indexarr, grabarr, squasharr, captionarr and ui\n" +
			"in a single process. It is meant for kind and local development, not for a\n" +
			"cluster: §3 gives each service its own Deployment, RBAC and leader election, and\n" +
			"the engines and transcode workers that run as separate pods are not started here.\n\n" +
			"Each service's metrics and probe listeners are offset by one port from the\n" +
			"addresses given, in the order above; ui has none of its own to offset and binds\n" +
			"--ui-bind-address. Every service shares the root command's --log-* and\n" +
			"--tracing-* flags, and every span is recorded under the single service name\n" +
			"\"clustarr\": one process has one OpenTelemetry TracerProvider, so a per-service\n" +
			"name here would just be whichever service happened to start first. Run services\n" +
			"separately for per-service trace attribution.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	var uiAddr string
	var uiAuthMode string
	cmd.Flags().StringVar(&uiAddr, "ui-bind-address", ui.DefaultBindAddress,
		"Address ui's HTTP server listens on. It is not offset like the managers' ports: ui has one "+
			"listener, serving its pages, /healthz and /readyz together.")
	cmd.Flags().StringVar(&uiAuthMode, "ui-auth-mode", "",
		"ui's authentication mode (`clustarr ui`'s --auth-mode), chosen explicitly: ui refuses to "+
			"serve without one, and that failure stops every service in this process. The only mode "+
			"is anonymous, which serves every request without a login (design amendment §A3.5).")

	// Leader election buys nothing in a single process that already runs one
	// of each controller, and would only add a Lease per service to clean up.
	if err := cmd.Flags().MarkHidden("leader-elect"); err != nil {
		panic(err)
	}

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		base := *common
		base.LeaderElect = false
		base.BusSingleNode = true

		services := allServices(lo, to, uiAddr, ui.AuthMode(uiAuthMode))
		optionsFor := make([]k8s.Options, len(services))
		for i, svc := range services {
			o := base
			var err error
			if o.MetricsBindAddress, err = offsetAddress(base.MetricsBindAddress, i); err != nil {
				return fmt.Errorf("%s: %w", svc.name, err)
			}
			if o.HealthProbeBindAddress, err = offsetAddress(base.HealthProbeBindAddress, i); err != nil {
				return fmt.Errorf("%s: %w", svc.name, err)
			}
			if o.PprofBindAddress, err = offsetAddress(base.PprofBindAddress, i); err != nil {
				return fmt.Errorf("%s: %w", svc.name, err)
			}
			optionsFor[i] = o
		}

		return runAll(cmd.Context(), services, optionsFor)
	}
	return cmd
}

// runAll starts every service and returns when they have all stopped.
//
// The first failure cancels the rest: a half-running stack in kind is worse
// than a clean exit, because the missing service's absence shows up later as
// an unexplained timeout in whatever was being tested.
func runAll(
	ctx context.Context,
	services []struct {
		name string
		run  func(ctx context.Context, o k8s.Options) error
	},
	options []k8s.Options,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	log := ctrl.LoggerFrom(ctx).WithName("all")

	for i, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.run(ctx, options[i])
			if err == nil {
				return
			}
			log.Error(err, "service stopped", "service", svc.name)
			mu.Lock()
			if first == nil {
				first = fmt.Errorf("%s: %w", svc.name, err)
			}
			mu.Unlock()
			cancel()
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return first
}

// devIndexPath is `clustarr all`'s SQLite release index: a writable location
// on a developer's machine, since `all` has no PVC.
//
// os.UserCacheDir rather than os.TempDir so a dev index survives a restart --
// re-syncing every indexer's RSS on every `clustarr all` is slow and hammers
// the trackers. TempDir is the fallback for the case UserCacheDir actually
// fails, which is HOME (or XDG_CACHE_HOME) being unset: a container, a cron
// job, a CI runner. It is never silently preferred.
//
// This is deliberately NOT a fallback inside indexarr.Options. See the call
// site: in-cluster, an unwritable PVC must fail loudly.
func devIndexPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "clustarr", "index", "releases.db")
}
