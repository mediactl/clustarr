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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/importarr"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr"
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

	// One case per role that is worth standing up, in an order that respects
	// a constraint controller-runtime imposes on the whole PROCESS:
	// controller names are unique per binary, not per manager
	// ("controller with name movie already exists"), because they label
	// per-controller metrics in one process-wide registry. So a single test
	// binary can register the catalog controllers exactly once. Production is
	// unaffected -- each service has one manager -- and `clustarr all` works
	// precisely because catalogarr's names (movie, series, episode,
	// mediafile, rootfolder, qualityprofile, delayprofile, metadataprovider,
	// search) and importarr's (libraryscan, rootfolderschedule,
	// importexclusion) are disjoint. Running catalogarr/all and importarr/all
	// in this one binary is what proves that.
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
		{name: "catalogarr/worker", run: func(ctx context.Context, o k8s.Options) error {
			d := catalogarr.DefaultOptions()
			d.Options, d.Role = o, catalogarr.RoleWorker
			return catalogarr.Run(ctx, d)
		}},
		{name: "catalogarr/metadata", run: func(ctx context.Context, o k8s.Options) error {
			d := catalogarr.DefaultOptions()
			d.Options, d.Role = o, catalogarr.RoleMetadata
			return catalogarr.Run(ctx, d)
		}},
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
		{name: "grabarr/controller", run: func(ctx context.Context, o k8s.Options) error {
			d := grabarr.DefaultOptions()
			d.Options = o
			d.DataDir = t.TempDir()
			// There is no default: [grabarr.Options.Validate] requires it
			// for the controller role, since the DownloadClient reconciler
			// stamps it onto every engine workload it creates.
			d.EngineImage = "ghcr.io/mediactl/clustarr/media:dev"
			return grabarr.Run(ctx, d)
		}},
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
		// The worker role is absent on purpose, not by omission: it is a
		// Job pod's entrypoint, starts no manager and serves no probes (the
		// Job has none), so it has no /readyz to reach. Its proof is that
		// its exit code reaches the process --
		// TestSquasharrWorkerExitCodeReachesTheProcess.
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
		{name: "catalogarr/all", run: func(ctx context.Context, o k8s.Options) error {
			d := catalogarr.DefaultOptions()
			d.Options, d.Role = o, catalogarr.RoleAll
			return catalogarr.Run(ctx, d)
		}},
		{name: "importarr/all (non-leader)", run: func(ctx context.Context, o k8s.Options) error {
			d := importarr.DefaultOptions()
			d.Options, d.Role = o, importarr.RoleAll
			d.DataPath = t.TempDir()
			d.LeaderElect = true
			return importarr.Run(ctx, d)
		}},
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
			},
			run: allServiceRun(t, "indexarr"),
		},
	}

	// The lease importarr/all must fail to acquire, held by another identity
	// for a day so the takeover can never happen inside the test.
	holdLease(t, env.Config, "default", importarr.LeaderElectionID)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				tc.prepare(t)
			}
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
	for _, svc := range allServices(&lo, &to) {
		if svc.name == name {
			return svc.run
		}
	}
	t.Fatalf("`clustarr all` has no %q service; allServices was renamed or reordered", name)
	return nil
}
