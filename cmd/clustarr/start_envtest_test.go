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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/captionarr"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/catalogarr/history"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/importarr"
	importlistworker "github.com/mediactl/clustarr/importarr/worker/importlist"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/indexarr/bundle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr"
	"github.com/mediactl/clustarr/ui"
)

// TestServiceStartsServesProbesAndStopsOnSignal is the M0 acceptance check for
// the binary, extended by Task C12a into the role-flag check: a service comes
// up against a real apiserver and a real JetStream server WITH every
// controller and worker its --role selects actually registered, answers
// /healthz and /readyz, and returns cleanly when its context is cancelled --
// which is exactly what SIGTERM does through ctrl.SetupSignalHandler.
//
// The roles matter because registration is where wiring fails: a duplicate
// controller name, a field index registered twice, a Runnable added after the
// manager started. All of those are hard errors from Run, and none of them is
// reachable from a unit test. Readiness is the second half: since C12a it
// covers the informer caches as well as the JetStream ping for catalogarr,
// and /data for importarr's worker roles, so a 200 from /readyz is a
// statement about the caches too.
//
// It needs the envtest control-plane binaries and skips without them, like
// pkg/crdcheck; `make test` sets KUBEBUILDER_ASSETS.
func TestServiceStartsServesProbesAndStopsOnSignal(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	if _, err := env.Start(); err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	// ctrl.GetConfig reads KUBECONFIG, so point it at the test apiserver.
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, env.KubeConfig, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)

	natsURL := startEmbeddedNATS(t)

	// facadeAddr is where the indexarr case's facade listens; its prepare
	// picks it, its verify dials it.
	var facadeAddr string

	// nvFake is the fake MusicBrainz, Open Library, ComicVine and Audnexus
	// the catalogarr/all case's metadata gateway reaches; its prepare
	// starts it, its verify reads what it was asked.
	var nvFake *fakeMetadataProviders

	// uiAddr is where the running ui case listens. ui has no probe port of
	// its own -- /healthz and /readyz share its one bind address -- so each
	// ui case binds the table's probe address, stores it here, and its
	// verify reads it back once /readyz has answered.
	var uiAddr atomic.Value

	// captionData is captionarr's --data-dir in both of its cases: the one
	// media volume the controller role lists and the worker role reads, as
	// the two Deployments share one claim. Every MediaFile path stays a
	// logical /data path that maps into it.
	captionData := t.TempDir()

	// One case per role that is worth standing up, in an order that respects
	// a constraint controller-runtime imposes on the whole PROCESS:
	// controller names are unique per binary, not per manager
	// ("controller with name movie already exists"), because they label
	// per-controller metrics in one process-wide registry. So a single test
	// binary can register the catalog controllers exactly once. Production is
	// unaffected -- each service has one manager -- and `clustarr all` works
	// precisely because catalogarr's names (movie, series, episode, artist,
	// album, author, book, audiobook, comic, issue, mediafile, rootfolder,
	// qualityprofile, delayprofile, metadataprovider, search) and
	// importarr's (libraryscan, rootfolderschedule, importexclusion,
	// importlist, fileimport-retrigger) are disjoint. Running catalogarr/all
	// and importarr/all in this one binary is what proves that.
	//
	// The roles that register no named controller -- worker and metadata,
	// whose consumers are manager Runnables -- can therefore run alongside
	// the "all" cases, and do.
	cases := []struct {
		name string
		// prepare runs inside the subtest, before run. It exists for the
		// indexarr case, which must redirect os.UserCacheDir somewhere
		// disposable without changing the code path being tested.
		prepare func(t *testing.T)
		run     func(ctx context.Context, o k8s.Options) error
		// verify runs once /readyz is green, before shutdown. A service
		// whose controllers are registered but never reconcile still goes
		// Ready, so this is where a case proves its reconcilers run.
		verify func(t *testing.T)
	}{
		{
			name: "catalogarr/worker",
			run: func(ctx context.Context, o k8s.Options) error {
				d := catalogarr.DefaultOptions()
				d.Options, d.Role = o, catalogarr.RoleWorker
				return catalogarr.Run(ctx, d)
			},
			// Gap fix Y3: the redownload consumer is subscribed in the
			// running worker, not only built.
			verify: func(t *testing.T) { verifyRedownload(t, natsURL) },
		},
		{name: "catalogarr/metadata", run: func(ctx context.Context, o k8s.Options) error {
			d := catalogarr.DefaultOptions()
			d.Options, d.Role = o, catalogarr.RoleMetadata
			return catalogarr.Run(ctx, d)
		}},
		// --role history has no case of its own since X14: it registers
		// the clustarr.io/replay handler, a controller per annotatable kind
		// ("replay-movie", ...), and controller names are unique per
		// process -- so, like every role with named controllers, it can run
		// once per binary. catalogarr/all below is its superset and runs
		// verifyHistory: the sink, the DLQ projector and the replay handler,
		// each doing its first piece of work on the real bus.
		{name: "importarr/worker", run: func(ctx context.Context, o k8s.Options) error {
			d := importarr.DefaultOptions()
			d.Options, d.Role = o, importarr.RoleWorker
			// The worker roles gate readiness on a writable /data
			// (amendment §A1.6); a dev box has no such mount.
			d.DataPath = t.TempDir()
			return importarr.Run(ctx, d)
		}},
		// grabarr had NO presence in this table before plan task D2-8: its
		// setupControllers/setupEngine were literal no-ops
		// ("registers nothing yet") until this task, so `clustarr grabarr`
		// itself had never been started against a real apiserver in any
		// test. These three cases are that proof for the role families
		// D2-8 wired: the controller Deployment (downloadclient, its
		// blocklist sweeper, download) and both engines -- re-attach, each
		// Reconciler and, per D2-8b, each previously-unregistered Reaper.
		//
		// The first version of the two engine cases used mgr.GetClient()
		// for the pre-mgr.Start DownloadClient read, following
		// grabarr/engine/usenet/doc.go's own prescribed wiring snippet
		// literally -- and the torrent-engine case failed immediately with
		// "the cache is not started, can not read objects": the cache-backed
		// client does not lazily start the one informer such a read needs.
		// Both setup functions now read through a client built straight
		// against the apiserver instead (see [directClient]/GetAPIReader in
		// grabarr/run.go). Without this table exercising the real Run path,
		// that would have shipped as a doc-comment-verified but
		// never-executed wiring snippet -- inert in the exact way this task
		// exists to catch.
		{
			name: "grabarr/controller",
			run: func(ctx context.Context, o k8s.Options) error {
				d := grabarr.DefaultOptions()
				d.Options = o
				d.DataDir = t.TempDir()
				// There is no default: [grabarr.Options.Validate] requires it
				// for the controller role, since the DownloadClient reconciler
				// stamps it onto every engine workload it creates.
				d.EngineImage = "ghcr.io/mediactl/clustarr/media:dev"
				// The chart's claim name under release "media", not
				// config/'s default: plan task G1-5's --data-claim must
				// reach the engine workload, and only a non-default value
				// can show that it does.
				d.DataClaimName = "media-clustarr-data"
				// Likewise the chart's engine ServiceAccount (X14): only a
				// non-default value shows --engine-service-account reaching
				// the pod spec.
				d.EngineServiceAccount = "media-clustarr-grabarr-engine"
				return grabarr.Run(ctx, d)
			},
			verify: func(t *testing.T) {
				c, err := client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
				if err != nil {
					t.Fatalf("build client: %v", err)
				}
				ctx := context.Background()
				dc := &downloadv1alpha1.DownloadClient{
					ObjectMeta: metav1.ObjectMeta{Name: "claim-probe", Namespace: "default"},
					Spec: downloadv1alpha1.DownloadClientSpec{
						Protocol: commonv1alpha1.ProtocolTorrent,
						Torrent:  &downloadv1alpha1.TorrentSpec{EnableDHT: ptr.To(false)},
					},
				}
				if err := c.Create(ctx, dc); err != nil {
					t.Fatalf("create DownloadClient: %v", err)
				}
				var claim string
				var sts appsv1.StatefulSet
				waitFor(t, "the DownloadClient controller to create claim-probe-engine", func() bool {
					if c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "claim-probe-engine"}, &sts) != nil {
						return false
					}
					for _, v := range sts.Spec.Template.Spec.Volumes {
						if v.PersistentVolumeClaim != nil {
							claim = v.PersistentVolumeClaim.ClaimName
						}
					}
					return true
				})
				if claim != "media-clustarr-data" {
					t.Errorf("the engine StatefulSet mounts claim %q, want --data-claim's media-clustarr-data: "+
						"grabarr/run.go did not pass Options.DataClaimName to the DownloadClient reconciler", claim)
				}
				// X14: the engine pod runs as the account the installers
				// bind, and gets the namespace and bus it needs to start.
				pod := sts.Spec.Template.Spec
				if pod.ServiceAccountName != "media-clustarr-grabarr-engine" {
					t.Errorf("the engine pod runs as ServiceAccount %q, want --engine-service-account's "+
						"media-clustarr-grabarr-engine: grabarr/run.go did not pass Options.EngineServiceAccount",
						pod.ServiceAccountName)
				}
				env := map[string]corev1.EnvVar{}
				for _, e := range pod.Containers[0].Env {
					env[e.Name] = e
				}
				if e, ok := env["POD_NAMESPACE"]; !ok || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil {
					t.Errorf("the engine container has no downward-API POD_NAMESPACE: %+v", env["POD_NAMESPACE"])
				}
				if env["NATS_URL"].Value != natsURL {
					t.Errorf("the engine container's NATS_URL = %q, want the controller's own %q",
						env["NATS_URL"].Value, natsURL)
				}
				if err := c.Delete(ctx, dc); err != nil {
					t.Errorf("delete DownloadClient: %v", err)
				}
			},
		},
		{
			name: "grabarr/torrent-engine",
			prepare: func(t *testing.T) {
				c, err := client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
				if err != nil {
					t.Fatalf("build client: %v", err)
				}
				// An ephemeral local port, not TorrentSpec's own
				// kubebuilder:default=42069: a fixed well-known port risks
				// colliding with another process already bound to it on
				// whatever machine runs this suite.
				_, portStr, err := net.SplitHostPort(freeAddress(t))
				if err != nil {
					t.Fatalf("split free address: %v", err)
				}
				port, err := strconv.Atoi(portStr)
				if err != nil {
					t.Fatalf("parse port: %v", err)
				}
				dc := &downloadv1alpha1.DownloadClient{
					ObjectMeta: metav1.ObjectMeta{Name: "torrents", Namespace: "default"},
					Spec: downloadv1alpha1.DownloadClientSpec{
						Protocol: commonv1alpha1.ProtocolTorrent,
						Torrent: &downloadv1alpha1.TorrentSpec{
							ListenPort: int32(port), //nolint:gosec // bounded by net.Listen's own ephemeral range
							// No DHT bootstrap: this suite has no
							// Internet egress, matching
							// reconciler_real_test.go's identical reason.
							EnableDHT: ptr.To(false),
						},
					},
				}
				if err := c.Create(context.Background(), dc); err != nil {
					t.Fatalf("create DownloadClient: %v", err)
				}
			},
			run: func(ctx context.Context, o k8s.Options) error {
				d := grabarr.DefaultOptions()
				d.Options = o
				d.Role = grabarr.RoleTorrentEngine
				d.Engine = "torrents-0"
				d.DataDir = t.TempDir()
				return grabarr.Run(ctx, d)
			},
		},
		{
			name: "grabarr/usenet-engine",
			prepare: func(t *testing.T) {
				c, err := client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
				if err != nil {
					t.Fatalf("build client: %v", err)
				}
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd-creds", Namespace: "default"},
					Data:       map[string][]byte{"username": []byte("u"), "password": []byte("p")},
				}
				if err := c.Create(context.Background(), secret); err != nil {
					t.Fatalf("create secret: %v", err)
				}
				dc := &downloadv1alpha1.DownloadClient{
					ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd", Namespace: "default"},
					Spec: downloadv1alpha1.DownloadClientSpec{
						Protocol: commonv1alpha1.ProtocolUsenet,
						Replicas: 1,
						Usenet: &downloadv1alpha1.UsenetSpec{
							// pkg/download/usenet.New builds its provider
							// pool lazily -- see its own doc comment -- so
							// this host is never actually dialled by
							// building the client with no in-flight jobs to
							// re-attach; only Add would reach the network,
							// and nothing in this case calls it.
							Providers: []downloadv1alpha1.NNTPProvider{{
								Name:      "stub",
								Host:      "127.0.0.1",
								SecretRef: corev1.LocalObjectReference{Name: "sabnzbd-creds"},
							}},
						},
					},
				}
				if err := c.Create(context.Background(), dc); err != nil {
					t.Fatalf("create DownloadClient: %v", err)
				}
			},
			run: func(ctx context.Context, o k8s.Options) error {
				d := grabarr.DefaultOptions()
				d.Options = o
				d.Role = grabarr.RoleUsenetEngine
				d.Engine = "sabnzbd-0"
				d.DataDir = t.TempDir()
				d.ScratchDir = t.TempDir()
				return grabarr.Run(ctx, d)
			},
		},
		// squasharr had no presence in this table before plan task E-4:
		// its setupControllers was a literal no-op through E-0..E-3 while
		// the TranscodeProfile and TranscodeJob reconcilers sat in their own
		// packages, fully tested and reachable from nowhere. Readiness alone
		// would not prove the wiring -- a manager with no controllers goes
		// Ready too -- so verify watches each reconciler do its first piece
		// of work: the profile controller hashing a profile, and the job
		// controller moving a TranscodeJob to Pending.
		//
		// There is no worker role: the pool pods run cmd/squasharr-worker,
		// which starts no manager and serves no probes, and whose exit
		// codes cmd/squasharr-worker's own tests hold to the process.
		{
			name: "squasharr/controller",
			run: func(ctx context.Context, o k8s.Options) error {
				d := squasharr.DefaultOptions()
				d.Options = o
				d.DataDir = t.TempDir()
				d.WorkerImage = "ghcr.io/mediactl/clustarr/media:dev"
				return squasharr.Run(ctx, d)
			},
			verify: func(t *testing.T) {
				c, err := client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
				if err != nil {
					t.Fatalf("build client: %v", err)
				}
				ctx := context.Background()
				profile := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "readyz-probe"}}
				if err := c.Create(ctx, profile); err != nil {
					t.Fatalf("create TranscodeProfile: %v", err)
				}
				tj := &transcodev1alpha1.TranscodeJob{
					ObjectMeta: metav1.ObjectMeta{Name: "readyz-probe", Namespace: "default"},
					Spec: transcodev1alpha1.TranscodeJobSpec{
						MediaFileRef: "no-such-file", ProfileRef: "readyz-probe",
						SourcePath: "/data/media/movies/x.mkv", SourceProbeHash: "0123456789abcdef",
					},
				}
				if err := c.Create(ctx, tj); err != nil {
					t.Fatalf("create TranscodeJob: %v", err)
				}
				waitFor(t, "the TranscodeProfile controller to hash readyz-probe", func() bool {
					var p transcodev1alpha1.TranscodeProfile
					return c.Get(ctx, client.ObjectKeyFromObject(profile), &p) == nil && p.Status.Hash != ""
				})
				waitFor(t, "the TranscodeJob controller to move readyz-probe to Pending", func() bool {
					var got transcodev1alpha1.TranscodeJob
					return c.Get(ctx, client.ObjectKeyFromObject(tj), &got) == nil &&
						got.Status.Phase == transcodev1alpha1.TranscodeJobPhasePending
				})
			},
		},
		// captionarr had no presence in this table before plan task F-6:
		// setupControllers and setupWorkers were literal no-ops while the
		// SubtitleProfile, SubtitleProvider and SubtitleRequest reconcilers
		// and the fetch worker sat in their own packages, fully tested and
		// reachable from nowhere. Each verify watches every piece do its
		// first observable work, and the two cases are one pipeline: the
		// controller case plans a request and publishes its fetch task to
		// the real JetStream stream, where it waits -- the controller role
		// consumes nothing -- until the worker case's consumer picks it up.
		// So the worker case depends on the controller case before it, which
		// is also what proves the subject, the consumer filter and the task
		// schema line up across the two roles.
		{
			name: "captionarr/controller",
			run: func(ctx context.Context, o k8s.Options) error {
				d := captionarr.DefaultOptions()
				d.Options, d.Role = o, captionarr.RoleController
				d.DataDir = captionData
				return captionarr.Run(ctx, d)
			},
			verify: func(t *testing.T) { verifyCaptionarrController(t, env.Config, captionData) },
		},
		{
			name: "captionarr/worker",
			run: func(ctx context.Context, o k8s.Options) error {
				d := captionarr.DefaultOptions()
				d.Options, d.Role = o, captionarr.RoleWorker
				d.DataDir = captionData
				return captionarr.Run(ctx, d)
			},
			verify: func(t *testing.T) { verifyCaptionarrWorker(t, env.Config) },
		},
		// The three "all" cases come last and are each the superset of
		// their service's roles: controllers, workers and (for catalogarr)
		// the metadata gateway, in one manager. Together they are `clustarr
		// all` minus the services that still register nothing -- which since
		// Task D1-8 no longer includes indexarr: it registers three
		// controllers, the RSS consumer, three RPC verbs and the release
		// index's retention sweep.
		//
		// catalogarr/all runs WITHOUT leader election, so its controllers
		// actually start and the case proves registration end to end.
		// importarr/all runs WITH it, and loses: the lease is pre-held by
		// another identity (see leaderElect below), which is the rollout
		// scenario -- a surge pod coming up beside an incumbent that still
		// owns the lease. Its /readyz must go green anyway. Before Task
		// C12a's review it could not: k8s.CacheSyncChecker added a bare
		// manager.RunnableFunc, which controller-runtime puts behind the
		// lease, so a non-leader replica never became Ready and every
		// rollout deadlocked. Every other case here sets LeaderElect false
		// and passes vacuously, which is why that shipped.
		//
		// It is also the one case that can prove catalogarr's controllers
		// WORK, for the same reason importarr/all is importarr's: controller
		// names are unique per process. Plan task G2-5 needs exactly that for
		// the seven non-video reconcilers G2-2 and G2-3 built and nothing
		// registered, and their first work runs through the metadata gateway
		// this role also starts -- so prepare points the gateway at fake
		// providers before it builds its registry.
		{
			name: "catalogarr/all",
			prepare: func(t *testing.T) {
				nvFake = startFakeMetadataProviders(t)
				prepareNonVideoCatalog(t, env.Config, nvFake)
			},
			run: func(ctx context.Context, o k8s.Options) error {
				d := catalogarr.DefaultOptions()
				d.Options, d.Role = o, catalogarr.RoleAll
				return catalogarr.Run(ctx, d)
			},
			verify: func(t *testing.T) {
				verifyNonVideoCatalog(t, env.Config, nvFake)
				verifyHistory(t, env.Config, natsURL)
			},
		},
		//
		// Once it is Ready as a non-leader, verify releases the lease and
		// the case becomes the leader: that is the only way this binary can
		// watch importarr's controllers WORK -- controller names are unique
		// per process, so there cannot be a second importarr case with
		// controllers -- and plan task G1-5 needs exactly that, for the
		// ImportList controller and list worker G1-3 built and nothing
		// registered, as does plan task G2-5 for fileimport's Retrigger.
		{
			name: "importarr/all (non-leader)",
			run: func(ctx context.Context, o k8s.Options) error {
				d := importarr.DefaultOptions()
				d.Options, d.Role = o, importarr.RoleAll
				d.DataPath = t.TempDir()
				d.LeaderElect = true
				return importarr.Run(ctx, d)
			},
			verify: func(t *testing.T) {
				verifyImportList(t, env.Config, natsURL)
				verifyRetrigger(t, env.Config)
			},
		},
		// indexarr is driven through `clustarr all`'s OWN closure rather than
		// a locally built Options, and that is the entire point of the case.
		//
		// Nothing in this tree called indexarr.Run before this: its role
		// gates, relindex.Open and k8s.AddProbes had zero execution coverage,
		// and Task D1-8 shipped a `clustarr all` that could not start. Open
		// MkdirAlls under the PVC's mountPath (/var/lib/clustarr/index),
		// which an unprivileged user cannot create, indexarr.Run returns that
		// error, and runAll's cancel() then stops ALL SEVEN services.
		//
		// Building Options here instead would have proved nothing -- a test
		// that sets its own writable IndexPath cannot see a missing override
		// in all.go. allServices is the production wiring, so deleting the
		// `d.IndexPath = devIndexPath()` line fails this case.
		{
			name: "indexarr/all (as `clustarr all` builds it)",
			prepare: func(t *testing.T) {
				// devIndexPath resolves through os.UserCacheDir, which reads
				// XDG_CACHE_HOME on Linux and HOME elsewhere. Redirecting
				// both keeps the test from writing into the developer's real
				// cache directory while leaving the code path identical --
				// and leaves the case failing if the override is removed,
				// because /var/lib is still not writable.
				dir := t.TempDir()
				t.Setenv("XDG_CACHE_HOME", dir)
				t.Setenv("HOME", dir)
				// `clustarr all` binds the Torznab facade on
				// $CLUSTARR_FACADE_BIND_ADDRESS, else :9696; a fixed port
				// would collide with whatever this machine already runs.
				facadeAddr = freeAddress(t)
				t.Setenv(facadeBindAddressEnv, facadeAddr)
				// A one-definition Cardigann bundle, loaded the way `clustarr
				// all` loads one: from $CLUSTARR_CARDIGANN_DEFINITIONS_DIR
				// (X14, --cardigann-definitions-dir).
				bundleDir := t.TempDir()
				def, err := os.ReadFile("../../testdata/cardigann/1337x.yml")
				if err != nil {
					t.Fatalf("read the bundle fixture: %v", err)
				}
				if err := os.WriteFile(filepath.Join(bundleDir, "1337x.yml"), def, 0o600); err != nil {
					t.Fatalf("write the bundle: %v", err)
				}
				t.Setenv(cardigannDefinitionsDirEnv, bundleDir)
			},
			run: allServiceRun(t, "indexarr"),
			// The Torznab facade (plan task G1-2) was built, tested with
			// httptest and bound to nothing: indexarr.Options had carried a
			// FacadeBindAddress since M0 that no code read. So the proof is a
			// dial of the real port, not a flag that parses.
			verify: func(t *testing.T) {
				verifyFacade(t, env.Config, facadeAddr)
				verifyBundle(t, env.Config)
			},
		},
		// ui had no presence in this table before plan task G3-5, and two
		// halves of it were unreachable in production. The Library,
		// Unmatched and Import Lists pages (G3-3, G3-4) read accessors and
		// streams that neither command wired, so all three rendered no rows;
		// and ui.Options.Actions (G3-1) was set nowhere, so every monitor,
		// search, rescan, assign and settings button answered 503. Both
		// defaults are legal and silent -- a nil field is "no rows" or
		// ErrNoWriter -- so ui/'s own tests, which build ui.Options by hand,
		// could not see either. verifyUI reads each page, watches each new
		// stream push a change, and performs a patch and a create through a
		// running process.
		//
		// The first case executes the real `clustarr ui` command line; the
		// second runs `clustarr all`'s own ui closure, bound through
		// --ui-bind-address (allServices' uiAddr) to a free port rather than
		// ui's default :8080, which this machine may already be using. Two
		// cases because the two commands build ui.Options separately.
		{
			name: "ui (as `clustarr ui` builds it)",
			run: func(ctx context.Context, o k8s.Options) error {
				uiAddr.Store(o.HealthProbeBindAddress)
				root := NewRootCommand()
				root.SetArgs([]string{"ui", "--bind-address", o.HealthProbeBindAddress, "--auth-mode", "anonymous"})
				return root.ExecuteContext(ctx)
			},
			verify: func(t *testing.T) { verifyUI(t, env.Config, uiAddr.Load().(string), "cmd") },
		},
		{
			name: "ui (as `clustarr all` builds it)",
			run: func(ctx context.Context, o k8s.Options) error {
				uiAddr.Store(o.HealthProbeBindAddress)
				// --ui-bind-address, the flag `clustarr all` binds ui with,
				// is allServices' third argument.
				var lo logging.Options
				var to tracing.Options
				for _, svc := range allServices(&lo, &to, o.HealthProbeBindAddress, ui.AuthModeAnonymous) {
					if svc.name == "ui" {
						return svc.run(ctx, o)
					}
				}
				return errors.New("`clustarr all` has no ui service")
			},
			verify: func(t *testing.T) { verifyUI(t, env.Config, uiAddr.Load().(string), "all") },
		},
	}

	// The lease importarr/all must fail to acquire, held by another identity
	// for a day so the takeover can never happen inside the test.
	holdLease(t, env.Config, "default", importarr.LeaderElectionID)

	// Every case runs its service as the identity it ships as, under only
	// the RBAC its installers bind (X14): see identityKubeconfigs.
	var identities []string
	for _, tc := range cases {
		identities = append(identities, caseIdentity(tc.name))
	}
	kubeconfigs := identityKubeconfigs(t, env, identities)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				tc.prepare(t)
			}
			// After prepare, which (like every verify) acts as the
			// cluster admin through env.Config.
			t.Setenv("KUBECONFIG", kubeconfigs[caseIdentity(tc.name)])
			probeAddr := freeAddress(t)

			o := k8s.DefaultOptions()
			o.Namespace = "default"
			// Each case's own closure re-enables it where it wants it.
			o.LeaderElect = false
			o.MetricsBindAddress = k8s.DisabledBindAddress
			o.HealthProbeBindAddress = probeAddr
			o.NATSURL = natsURL
			o.BusSingleNode = true
			o.GracefulShutdownTimeout = 10 * time.Second

			ctx, cancel := context.WithCancel(context.Background())
			// A verify that fails calls t.Fatalf before the cancel() below,
			// which would leave this service running into the next case --
			// and, at the end, leave envtest's apiserver unable to stop.
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, o) }()

			// A registration failure returns from Run immediately, long
			// before any probe answers, so surface it rather than waiting
			// out the probe deadline.
			select {
			case err := <-done:
				cancel()
				t.Fatalf("Run returned before serving probes: %v", err)
			case <-time.After(100 * time.Millisecond):
			}

			// /healthz is a ping and comes up with the probe listener.
			waitForProbe(t, "http://"+probeAddr+"/healthz")

			// /readyz additionally pings JetStream (§13) and, since Task
			// C12a, waits on the informer caches -- and on a writable /data
			// for importarr's worker roles.
			waitForProbe(t, "http://"+probeAddr+"/readyz")

			if tc.verify != nil {
				tc.verify(t)
			}

			// SIGTERM cancels the signal-handler context; cancelling here is
			// the same path.
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Run returned %v on a clean shutdown", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("Run did not return within 30s of its context being cancelled")
			}

			// The probe listener is gone once the manager has stopped.
			if _, err := http.Get("http://" + probeAddr + "/healthz"); err == nil { //nolint:noctx // liveness of a closed listener
				t.Error("the health probe endpoint is still listening after shutdown")
			}
		})
	}
}

// TestServiceFailsFastOnBadOptions proves the validation runs before anything
// touches the cluster: no kubeconfig and no NATS server are needed for these.
func TestServiceFailsFastOnBadOptions(t *testing.T) {
	ctx := context.Background()

	o := catalogarr.DefaultOptions()
	o.Role = "nonsense"
	if err := catalogarr.Run(ctx, o); err == nil {
		t.Error("an unknown role reached the manager")
	}

	o = catalogarr.DefaultOptions()
	o.NATSURL = ""
	if err := catalogarr.Run(ctx, o); err == nil {
		t.Error("a missing --nats-url reached the manager")
	}

	o = catalogarr.DefaultOptions()
	o.LeaderElect = true
	o.Namespace = ""
	o.LeaderElectionNamespace = ""
	if err := catalogarr.Run(ctx, o); err == nil {
		t.Error("leader election with nowhere to put the Lease reached the manager")
	}
}

func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-cmd-test",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   t.TempDir(),
		NoLog:      true,
		NoSigs:     true,
	})
	if err != nil {
		t.Fatalf("new NATS server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// freeAddress asks the kernel for an unused port and hands back the address.
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return addr
}

// waitFor polls cond until it holds or 30s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitForProbe(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // bounded by the loop deadline
		if err != nil {
			last = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body := resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
		if body == http.StatusOK {
			return
		}
		last = fmt.Errorf("status %d", body)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never returned 200: %v", url, errors.Join(last))
}

// holdLease pre-creates a coordination.k8s.io Lease owned by someone else, so
// a manager configured for leader election with that ID can never acquire it.
//
// It is how this suite reproduces the half of a rolling update that actually
// deadlocked: the new pod is up, the old pod still holds the lease, and the
// new pod's readiness has to pass on its own merits.
func holdLease(t *testing.T, cfg *rest.Config, namespace, id string) {
	t.Helper()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	renew := metav1.NewMicroTime(time.Now().Add(24 * time.Hour))
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: namespace},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To("incumbent-pod"),
			LeaseDurationSeconds: ptr.To(int32(86400)),
			AcquireTime:          &renew,
			RenewTime:            &renew,
		},
	}
	if err := c.Create(context.Background(), lease); err != nil {
		t.Fatalf("hold the %s lease: %v", id, err)
	}
}

// allServiceRun returns `clustarr all`'s own closure for one service, so a
// test drives the production wiring rather than a restatement of it.
//
// allServices takes the root command's shared observability options; zero
// values are what `clustarr all` uses when no --log-*/--tracing-* flag is
// given, and neither reaches the assertions here.
func allServiceRun(t *testing.T, name string) func(ctx context.Context, o k8s.Options) error {
	t.Helper()
	var lo logging.Options
	var to tracing.Options
	for _, svc := range allServices(&lo, &to, ui.DefaultBindAddress, ui.AuthModeAnonymous) {
		if svc.name == name {
			return svc.run
		}
	}
	t.Fatalf("`clustarr all` has no %q service; allServices was renamed or reordered", name)
	return nil
}

// verifyHistory publishes one domain event and one dead letter on the real
// bus and waits for RoleHistory's consumers to act on them: the sink
// turning the event into an events.k8s.io Event on the CR it concerns, the
// DLQ projector annotating the dead-lettered CR (ruling R1) with the
// sequence to replay -- which it can name only with the DLQ reader
// catalogarr/run.go hands it -- and the replay handler (X14) republishing
// that dead letter when the CR is annotated clustarr.io/replay=<seq>.
func verifyHistory(t *testing.T, cfg *rest.Config, natsURL string) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	bus, nc, err := k8s.ConnectBus(natsURL, "start-test")
	if err != nil {
		t.Fatalf("connect the bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close(); nc.Close() })

	// The sink: an ImportListSynced event regarding default/history-probe.
	synced := schema.ImportListSynced{
		ListRef: schema.Ref{Namespace: "default", Name: "history-probe", UID: "history-probe-uid"},
		Fetched: 3, Added: 1, At: time.Now(),
	}
	schemaName, data, err := schema.Encode(synced)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := bus.Publish(ctx, events.CatalogImportListSyncedSubject("history-probe-uid"), &events.Envelope{
		ID: "start-test-history-sink", Type: "catalog.ImportListSynced", Schema: schemaName,
		Source: "start-test", Key: "default/history-probe", Time: time.Now(), Data: data,
	}); err != nil {
		t.Fatalf("publish the domain event: %v", err)
	}
	waitFor(t, "the history sink to project the domain event onto an Event", func() bool {
		var list eventsv1.EventList
		if c.List(ctx, &list, client.InNamespace("default")) != nil {
			return false
		}
		for _, e := range list.Items {
			if e.Regarding.Kind == "ImportList" && e.Regarding.Name == "history-probe" &&
				e.ReportingController == "catalogarr-history" {
				return true
			}
		}
		return false
	})

	// The DLQ projector: a dead-lettered SearchTask for a Movie that exists.
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "history-probe", Namespace: "default"},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies",
		},
	}
	if err := c.Create(ctx, movie); err != nil {
		t.Fatalf("create Movie: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), movie) })
	schemaName, data, err = schema.Encode(schema.SearchTask{
		MediaRef: commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "history-probe"},
		Reason:   schema.SearchReasonMissing,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	const origSubject = "clustarr.work.catalogarr.search.high.default_history-probe"
	if _, err := bus.Publish(ctx, events.DLQSubject("catalogarr", "search", "history-probe"), &events.Envelope{
		ID: "dlq:start-test:history-probe", Type: "catalog.search", Schema: schemaName,
		Source: "start-test", Key: "default/history-probe", Time: time.Now(), Data: data,
		Headers: map[string]string{
			events.HeaderDLQSubject:  origSubject,
			events.HeaderDLQReason:   "max deliveries exceeded (5): start test",
			events.HeaderDLQConsumer: "catalogarr-search-high",
			events.HeaderDLQAttempts: "5",
		},
	}); err != nil {
		t.Fatalf("publish the dead letter: %v", err)
	}
	// The annotation names whichever dead letter for this Movie came last.
	// That is usually the one published above, but this runs beside the
	// Movie controller and the metadata gateway (catalogarr/all), and with
	// no TMDB provider configured the gateway dead-letters the Movie's own
	// MetadataTask too -- a real dead letter, which resolves to the same
	// Movie and proves the projector just as well. The sequence is what
	// matters: the projector can name it only with the DLQ reader
	// catalogarr/run.go hands it.
	var seq string
	waitFor(t, "the DLQ projector to annotate the dead-lettered Movie with its sequence", func() bool {
		var got catalogv1alpha1.Movie
		if c.Get(ctx, client.ObjectKeyFromObject(movie), &got) != nil {
			return false
		}
		seq = got.Annotations[history.AnnotationDeadLetterSeq]
		return got.Annotations[history.AnnotationDeadLettered] != "" && seq != ""
	})

	// The replay handler: the operator's `kubectl annotate movie
	// history-probe clustarr.io/replay=<seq>`. Its proof is the Replayed
	// Event and the request consumed; the dead-lettered marker itself may
	// come straight back, since a replayed MetadataTask fails again here.
	if err := c.Get(ctx, client.ObjectKeyFromObject(movie), movie); err != nil {
		t.Fatalf("get Movie: %v", err)
	}
	patch := client.MergeFrom(movie.DeepCopy())
	movie.Annotations[history.AnnotationReplay] = seq
	if err := c.Patch(ctx, movie, patch); err != nil {
		t.Fatalf("annotate the Movie for replay: %v", err)
	}
	waitFor(t, "the replay handler to consume the replay request", func() bool {
		var got catalogv1alpha1.Movie
		if c.Get(ctx, client.ObjectKeyFromObject(movie), &got) != nil {
			return false
		}
		_, pending := got.Annotations[history.AnnotationReplay]
		return !pending
	})
	waitFor(t, "the replay handler's Replayed Event on the Movie", func() bool {
		var list eventsv1.EventList
		if c.List(ctx, &list, client.InNamespace("default")) != nil {
			return false
		}
		for _, e := range list.Items {
			if e.Regarding.Kind == "Movie" && e.Regarding.Name == movie.Name &&
				e.ReportingController == "clustarr-replay" && e.Reason == "Replayed" {
				return true
			}
		}
		return false
	})
}

// verifyRedownload proves spec §8.3's failed-Download consumer (gap fix Y3,
// catalogarr/worker/redownload) runs in `catalogarr --role worker`: a lease
// held by a Download is freed once grabarr's failed event for that Download
// reaches the bus. The Movie does not exist, so the consumer frees the lease
// and publishes no search -- the lease is its first observable piece of
// work, and it can only disappear if something subscribed
// catalogarr-redownload.
func verifyRedownload(t *testing.T, natsURL string) {
	t.Helper()
	ctx := context.Background()
	bus, nc, err := k8s.ConnectBus(natsURL, "start-test")
	if err != nil {
		t.Fatalf("connect the bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close(); nc.Close() })

	const failed = "redownload-probe-failed"
	movie := commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "redownload-probe"}
	lease := events.LeaseKey(events.MediaKey(string(movie.Kind), "default", movie.Name))
	kv := bus.KV(events.BucketLeases)
	if _, err := kv.Create(ctx, lease, []byte(failed)); err != nil {
		t.Fatalf("take the probe lease: %v", err)
	}

	schemaName, data, err := schema.Encode(schema.DownloadEvent{
		DownloadRef: schema.Ref{Namespace: "default", Name: failed, UID: "redownload-probe-uid"},
		Media:       movie,
		Action:      events.ActionFailed,
		Reason:      string(downloadv1alpha1.DownloadFailureMissingArticles),
		At:          time.Now(),
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := bus.Publish(ctx, events.DownloadEventSubject(events.ActionFailed, "redownload-probe-uid"), &events.Envelope{
		ID: "redownload-probe-uid:failed", Type: "download.DownloadEvent", Schema: schemaName,
		Source: "start-test", Key: "default/" + failed, Time: time.Now(), Data: data,
	}); err != nil {
		t.Fatalf("publish the failed event: %v", err)
	}
	waitFor(t, "the redownload consumer to free the failed Download's lease", func() bool {
		_, err := kv.Get(ctx, lease)
		return errors.Is(err, events.ErrKeyNotFound)
	})
}

// verifyImportList takes importarr/all from non-leader to leader and waits
// for the ImportList controller to schedule a sync (status.nextSyncAt) and
// the list worker to consume it (its Result checkpoint in clustarr-progress).
// The list points at a closed local port, so the sync fails at once and
// offline -- a failed sync still checkpoints, which is the worker's first
// observable piece of work.
func verifyImportList(t *testing.T, cfg *rest.Config, natsURL string) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: importarr.LeaderElectionID, Namespace: "default"}}
	if err := c.Delete(ctx, lease); err != nil {
		t.Fatalf("release the held %s lease: %v", importarr.LeaderElectionID, err)
	}

	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Name: "startup-probe", Namespace: "default"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:    []string{"movie"},
			Custom:   &catalogv1alpha1.CustomList{URL: "http://127.0.0.1:1/list.json"},
			Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
		},
	}
	if err := c.Create(ctx, il); err != nil {
		t.Fatalf("create ImportList: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), il) })

	waitForLong(t, "the ImportList controller (now leader) to schedule a sync", func() bool {
		var got catalogv1alpha1.ImportList
		return c.Get(ctx, client.ObjectKeyFromObject(il), &got) == nil && got.Status.NextSyncAt != nil
	})

	bus, nc, err := k8s.ConnectBus(natsURL, "start-test")
	if err != nil {
		t.Fatalf("connect the bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close(); nc.Close() })
	key := importlistworker.ResultKey(string(il.UID))
	var res importlistworker.Result
	waitFor(t, "the list worker to checkpoint the sync it was sent", func() bool {
		e, err := bus.KV(events.BucketProgress).Get(ctx, key)
		if err != nil {
			return false
		}
		res, err = importlistworker.DecodeResult(e.Value)
		return err == nil
	})
	if res.Error == "" {
		t.Errorf("the list worker reported success fetching a closed port: %+v", res)
	}
}

// verifyFacade dials the Torznab facade `clustarr all` bound: the API key
// indexarr generated into its Secret, a 401 without it, a 200 aggregate
// search with it, and a definition-backed grab that reaches the Cardigann
// download path (plan task G1-5's other wiring) rather than being refused
// as "not configured".
func verifyFacade(t *testing.T, cfg *rest.Config, addr string) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: indexarr.DefaultFacadeAPIKeySecret}, &sec); err != nil {
		t.Fatalf("indexarr did not generate the facade's API-key Secret: %v", err)
	}
	key := string(sec.Data[indexarr.FacadeAPIKeyField])
	if len(key) != 64 {
		t.Fatalf("generated API key has %d characters, want 64 hex", len(key))
	}

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get("http://" + addr + path) //nolint:noctx // bounded by the server's own timeouts
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return resp.StatusCode, string(body)
	}

	if code, _ := get("/search/api?t=search&q=probe"); code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated aggregate search got %d, want 401: the facade must fail closed", code)
	}
	if code, body := get("/search/api?t=search&q=probe&apikey=" + key); code != http.StatusOK || !strings.Contains(body, "<rss") {
		t.Errorf("an authenticated aggregate search got %d %q, want 200 and an RSS document", code, body)
	}

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "facade-cardigann", Namespace: "default"},
		Spec: indexv1alpha1.IndexerSpec{
			DefinitionRef: ptr.To("no-such-definition"),
			BaseURL:       "http://127.0.0.1:1/",
		},
	}
	if err := c.Create(ctx, idx); err != nil {
		t.Fatalf("create Indexer: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), idx) })
	var code int
	var body string
	waitFor(t, "the facade to see the definition-backed Indexer", func() bool {
		code, body = get("/facade-cardigann/download?guid=g&url=http%3A%2F%2F127.0.0.1%3A1%2Fx&apikey=" + key)
		return code != http.StatusNotFound
	})
	if strings.Contains(body, "Cardigann download path is not configured") {
		t.Errorf("a definition-backed grab was refused as not configured: download.Service.Definitions is unset (%d %q)",
			code, body)
	}
	if code != http.StatusBadGateway || !strings.Contains(body, "build client for default/facade-cardigann") {
		t.Errorf("a definition-backed grab got %d %q, want 502 from building the Cardigann engine", code, body)
	}
}

// verifyBundle waits for indexarr's Cardigann bundle loader (X14; the
// consumer X8a's cardigann.LoadBundle was built for) to turn the bundle the
// case mounted into a labelled IndexerDefinition, and for the
// IndexerDefinition controller to parse it -- status.id is what an Indexer's
// spec.definition resolves against.
func verifyBundle(t *testing.T, cfg *rest.Config) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	waitFor(t, "the bundle loader's IndexerDefinition 1337x, parsed", func() bool {
		var d indexv1alpha1.IndexerDefinition
		return c.Get(ctx, client.ObjectKey{Name: "1337x"}, &d) == nil &&
			d.Labels[bundle.LabelBundled] == "true" && d.Status.ID == "1337x"
	})
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), &indexv1alpha1.IndexerDefinition{ObjectMeta: metav1.ObjectMeta{Name: "1337x"}})
	})
}

// waitForLong is waitFor with room for a leader election: a released lease
// is acquired on the elector's next retry, and only then do the controllers
// start their informers.
func waitForLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// captionProbe names every object the captionarr cases create: the
// SubtitleProfile (cluster-scoped), and in "default" the SubtitleProvider,
// the MediaFile, and the SubtitleRequest the profile controller ensures for
// it, which shares the MediaFile's name.
const (
	captionProbe        = "captionarr-probe"
	captionProbeLogical = "/data/movies/Captionarr Probe (2020)/Captionarr Probe (2020).mkv"
)

// verifyCaptionarrController gives each of the controller role's three
// reconcilers its first piece of work and waits for it: the SubtitleProvider
// controller projecting a status (through providerset.Validate), the
// SubtitleProfile controller ensuring a SubtitleRequest for a probed video
// MediaFile, and the SubtitleRequest controller planning that request --
// which needs the bus (a fetch task is published and its dispatch stamped)
// and --data-dir (the file exists only under dataDir, at the logical path's
// place, so reading spec.path literally leaves the request Blocked).
func verifyCaptionarrController(t *testing.T, cfg *rest.Config, dataDir string) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	// The provider: gestdown needs no credentials, so its first reconcile
	// projects Ready=True -- and, being TV-only, it is skipped for the movie
	// below, which the worker case reports.
	provider := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: captionProbe, Namespace: "default"},
		Spec:       subtitlev1alpha1.SubtitleProviderSpec{Type: subtitlev1alpha1.SubtitleProviderGestdown, Enabled: ptr.To(true)},
	}
	if err := c.Create(ctx, provider); err != nil {
		t.Fatalf("create SubtitleProvider: %v", err)
	}
	profile := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: captionProbe},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Default:   true,
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "de", Language: "de"}},
		},
	}
	if err := c.Create(ctx, profile); err != nil {
		t.Fatalf("create SubtitleProfile: %v", err)
	}

	local := filepath.Join(dataDir, strings.TrimPrefix(captionProbeLogical, "/data"))
	if err := os.MkdirAll(filepath.Dir(local), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(local, []byte("not really a movie"), 0o600); err != nil {
		t.Fatalf("write the video: %v", err)
	}
	st, err := os.Stat(local)
	if err != nil {
		t.Fatalf("stat the video: %v", err)
	}
	// The Movie the MediaFile backs. captionarr plans subtitles only for a
	// file whose item still exists (X11b: a removeAndKeep import list keeps
	// the MediaFile record and deletes only the Movie, and such a file is no
	// longer managed), so without it the profile never ensures a request.
	// verifyCaptionarrWorker deletes it with the rest.
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: captionProbe, Namespace: "default"},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 604, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies",
		},
	}
	if err := c.Create(ctx, movie); err != nil {
		t.Fatalf("create Movie: %v", err)
	}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: captionProbe, Namespace: "default"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef:  commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: captionProbe},
			Path:      captionProbeLogical,
			SizeBytes: st.Size(),
			ModTime:   metav1.NewTime(st.ModTime()),
		},
	}
	if err := c.Create(ctx, mf); err != nil {
		t.Fatalf("create MediaFile: %v", err)
	}
	// catalogarr's probe, standing in: the hash the fetch worker will
	// recompute from the file on disk, and an English audio track with no
	// embedded subtitle, so German is the one wanted language.
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.MediaFile(captionProbe, "default").
		WithStatus(catalogac.MediaFileStatus().
			WithProbeHash(mediainfo.ProbeHash(captionProbeLogical, st.Size(), st.ModTime())).
			WithMediaInfo(commonv1alpha1.MediaInfo{
				Container: "mkv",
				Audio:     []commonv1alpha1.AudioStream{{Index: 1, Codec: "aac", Language: "eng"}},
			}))); err != nil {
		t.Fatalf("probe the MediaFile: %v", err)
	}

	waitFor(t, "the SubtitleProvider controller to project a Ready status", func() bool {
		var got subtitlev1alpha1.SubtitleProvider
		return c.Get(ctx, client.ObjectKeyFromObject(provider), &got) == nil &&
			got.Status.ObservedGeneration == got.Generation &&
			k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady)
	})
	waitFor(t, "the SubtitleProfile controller to ensure a SubtitleRequest for the video", func() bool {
		var got subtitlev1alpha1.SubtitleRequest
		return c.Get(ctx, types.NamespacedName{Namespace: "default", Name: captionProbe}, &got) == nil &&
			got.Spec.ProfileRef == captionProbe && got.Spec.MediaFileRef == captionProbe
	})
	var sr subtitlev1alpha1.SubtitleRequest
	waitFor(t, "the SubtitleRequest controller to plan the request and dispatch German", func() bool {
		if c.Get(ctx, types.NamespacedName{Namespace: "default", Name: captionProbe}, &sr) != nil {
			return false
		}
		for _, it := range sr.Status.Items {
			if it.LangKey == "de" && it.Attempts.Count == 1 && it.NextSearchAt != nil {
				return true
			}
		}
		return false
	})
	if sr.Status.Phase != subtitlev1alpha1.SubtitleRequestPhaseSearching {
		planned := k8s.FindCondition(sr.Status.Conditions, subtitlev1alpha1.SubtitleRequestConditionPlanned)
		t.Errorf("SubtitleRequest phase = %q (Planned: %+v), want Searching", sr.Status.Phase, planned)
	}
	var sp subtitlev1alpha1.SubtitleProfile
	if err := c.Get(ctx, client.ObjectKeyFromObject(profile), &sp); err != nil {
		t.Fatalf("get SubtitleProfile: %v", err)
	}
	if sp.Status.MatchingFiles != 1 {
		t.Errorf("SubtitleProfile status.matchingFiles = %d, want 1", sp.Status.MatchingFiles)
	}
}

// verifyCaptionarrWorker waits for the fetch worker to consume the task the
// controller case published and record its first result on the item. No
// enabled provider can search a movie here (gestdown is TV-only), so the
// result is "unavailable", naming the provider it skipped -- which is the
// proof the worker's providerset builder listed it. Then it removes every
// object the two captionarr cases made.
func verifyCaptionarrWorker(t *testing.T, cfg *rest.Config) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, o := range []client.Object{
			&subtitlev1alpha1.SubtitleRequest{ObjectMeta: metav1.ObjectMeta{Name: captionProbe, Namespace: "default"}},
			&catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: captionProbe, Namespace: "default"}},
			&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: captionProbe, Namespace: "default"}},
			&subtitlev1alpha1.SubtitleProvider{ObjectMeta: metav1.ObjectMeta{Name: captionProbe, Namespace: "default"}},
			&subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: captionProbe}},
		} {
			_ = c.Delete(ctx, o)
		}
	})

	var item subtitlev1alpha1.SubtitleItem
	waitFor(t, "the fetch worker to consume the controller's task and record German's result", func() bool {
		var sr subtitlev1alpha1.SubtitleRequest
		if c.Get(ctx, types.NamespacedName{Namespace: "default", Name: captionProbe}, &sr) != nil {
			return false
		}
		for _, it := range sr.Status.Items {
			if it.LangKey == "de" && it.State != "" {
				item = it
				return true
			}
		}
		return false
	})
	if item.State != subtitlev1alpha1.SubtitleItemUnavailable {
		t.Errorf("item de state = %q (lastError %q), want %q", item.State, item.LastError, subtitlev1alpha1.SubtitleItemUnavailable)
	}
	if !strings.Contains(item.LastError, captionProbe) {
		t.Errorf("item de lastError = %q, want it to name the skipped provider %q -- "+
			"the worker's providerset builder never listed it", item.LastError, captionProbe)
	}
}
