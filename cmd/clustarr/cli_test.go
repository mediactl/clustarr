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
	"bytes"
	"context"
	"log/slog"
	"net"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/captionarr"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/importarr"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
	"github.com/mediactl/clustarr/squasharr"
	"github.com/mediactl/clustarr/ui"
)

// execute runs the command tree with args and returns its stdout.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVersionCommand(t *testing.T) {
	out, err := execute(t, "version")
	if err != nil {
		t.Fatalf("clustarr version: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "clustarr "+version.String()) {
		t.Errorf("output %q does not contain %q", out, "clustarr "+version.String())
	}
	if !strings.Contains(out, runtime.Version()) {
		t.Errorf("output %q does not name the Go toolchain", out)
	}
}

func TestVersionCommandTakesNoArguments(t *testing.T) {
	if _, err := execute(t, "version", "extra"); err == nil {
		t.Fatal("clustarr version accepted a positional argument")
	}
}

func TestRootListsEveryService(t *testing.T) {
	out, err := execute(t, "--help")
	if err != nil {
		t.Fatalf("clustarr --help: %v", err)
	}
	for _, want := range []string{
		"catalogarr", "importarr", "indexarr", "grabarr", "squasharr", "captionarr", "ui", "all", "version",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not mention %q:\n%s", want, out)
		}
	}
}

// stub replaces a service entrypoint with a recorder and restores it after the
// test, so a command line can be executed without a cluster.
func stub[T any](t *testing.T, target *func(context.Context, T) error) *T {
	t.Helper()
	var got T
	original := *target
	*target = func(_ context.Context, o T) error {
		got = o
		return nil
	}
	t.Cleanup(func() { *target = original })
	return &got
}

func TestCatalogarrManagerOptions(t *testing.T) {
	got := stub(t, &runCatalogarr)

	if _, err := execute(t, "catalogarr",
		"--role", "worker",
		"--namespace", "clustarr",
		"--leader-elect",
		"--nats-url", "nats://localhost:4222",
		"--metrics-bind-address", ":9443",
		"--watch-namespace", "media,media-staging",
	); err != nil {
		t.Fatalf("clustarr catalogarr: %v", err)
	}

	if got.Role != catalogarr.RoleWorker {
		t.Errorf("role = %q", got.Role)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the parsed options are invalid: %v", err)
	}

	opts := got.ManagerOptions()
	// The worker role must never wait for the lease, whatever --leader-elect
	// says: §3 runs the workers on every replica.
	if opts.LeaderElection {
		t.Error("--leader-elect turned on leader election for a worker role")
	}
	if opts.LeaderElectionID != catalogarr.LeaderElectionID {
		t.Errorf("LeaderElectionID = %q, want %q", opts.LeaderElectionID, catalogarr.LeaderElectionID)
	}
	if opts.Metrics.BindAddress != ":9443" {
		t.Errorf("metrics bind address = %q", opts.Metrics.BindAddress)
	}
	if len(opts.Cache.DefaultNamespaces) != 2 {
		t.Errorf("cache namespaces = %v, want two", opts.Cache.DefaultNamespaces)
	}
	if opts.Scheme == nil {
		t.Error("the manager has no scheme")
	}

	// The controller role does take the lease.
	got = stub(t, &runCatalogarr)
	if _, err := execute(t, "catalogarr",
		"--role", "controller", "--namespace", "clustarr", "--leader-elect",
	); err != nil {
		t.Fatalf("clustarr catalogarr --role controller: %v", err)
	}
	if !got.ManagerOptions().LeaderElection {
		t.Error("the controller role did not take the leader lease")
	}
}

func TestIndexarrManagerOptions(t *testing.T) {
	got := stub(t, &runIndexarr)

	if _, err := execute(t, "indexarr",
		"--namespace", "clustarr",
		"--index-path", "/index/releases.db",
	); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if got.Role != indexarr.RoleAll {
		t.Errorf("role = %q", got.Role)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the parsed options are invalid: %v", err)
	}
	// §3 pins indexarr to one replica with a Recreate strategy, so there is
	// nothing to elect and a lease would only delay its own restart.
	if got.ManagerOptions().LeaderElection {
		t.Error("indexarr took a leader lease")
	}
	if got.IndexPath != "/index/releases.db" {
		t.Errorf("index path = %q", got.IndexPath)
	}
}

// TestIndexarrFacadeSettings covers the Torznab facade's two flags. The
// API-key Secret is named, never keyed: --facade-api-key-secret defaults to
// indexarr.DefaultFacadeAPIKeySecret (config/'s name) and then to
// $CLUSTARR_FACADE_API_KEY_SECRET (the chart's, which carries the release
// fullname). The facade fails closed, so an enabled facade with no namespace
// to keep that Secret in is refused before anything starts.
func TestIndexarrFacadeSettings(t *testing.T) {
	got := stub(t, &runIndexarr)
	if _, err := execute(t, "indexarr", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if got.FacadeAPIKeySecret != indexarr.DefaultFacadeAPIKeySecret || got.FacadeBindAddress != ":8080" {
		t.Errorf("facade = %q / secret %q, want :8080 / %q",
			got.FacadeBindAddress, got.FacadeAPIKeySecret, indexarr.DefaultFacadeAPIKeySecret)
	}
	if !got.FacadeEnabled() {
		t.Error("the facade is disabled by default")
	}

	t.Setenv(facadeAPIKeySecretEnv, "media-clustarr-indexarr-facade")
	t.Setenv(facadeBindAddressEnv, ":9797")
	if _, err := execute(t, "indexarr", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if got.FacadeAPIKeySecret != "media-clustarr-indexarr-facade" || got.FacadeBindAddress != ":9797" {
		t.Errorf("facade = %q / secret %q, want the environment's :9797 / media-clustarr-indexarr-facade",
			got.FacadeBindAddress, got.FacadeAPIKeySecret)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the parsed options are invalid: %v", err)
	}

	// No namespace: nowhere to keep the key, so an enabled facade is refused
	// and a disabled one is not.
	t.Setenv(namespaceEnv, "")
	if _, err := execute(t, "indexarr"); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if err := got.Validate(); err == nil {
		t.Error("an enabled facade with no namespace for its API-key Secret was accepted")
	}
	if _, err := execute(t, "indexarr", "--facade-bind-address", "0"); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("a disabled facade still demanded a namespace: %v", err)
	}
	if _, err := execute(t, "indexarr", "--namespace", "clustarr", "--facade-api-key-secret", ""); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if err := got.Validate(); err == nil {
		t.Error("an enabled facade with no API-key Secret was accepted")
	}
}

// TestGrabarrDataClaimComesFromTheEnvironment: the DownloadClient controller
// stamps --data-claim onto every engine workload, config/ relies on its
// default and the chart sets $CLUSTARR_DATA_CLAIM, because the chart's claim
// carries the release fullname. See TestGrabarrEnginesMountAClaimTheInstallerCreates
// for the same fact read from what each installer renders.
func TestGrabarrDataClaimComesFromTheEnvironment(t *testing.T) {
	got := stub(t, &runGrabarr)
	t.Setenv(engineImageEnv, "ghcr.io/mediactl/clustarr/media:dev")
	if _, err := execute(t, "grabarr", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr grabarr: %v", err)
	}
	if got.DataClaimName != "clustarr-data" {
		t.Errorf("DataClaimName = %q, want config/'s clustarr-data", got.DataClaimName)
	}

	t.Setenv(dataClaimEnv, "media-clustarr-data")
	if _, err := execute(t, "grabarr", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr grabarr: %v", err)
	}
	if got.DataClaimName != "media-clustarr-data" {
		t.Errorf("DataClaimName = %q, want $%s's media-clustarr-data", got.DataClaimName, dataClaimEnv)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the parsed options are invalid: %v", err)
	}

	if _, err := execute(t, "grabarr", "--namespace", "clustarr", "--data-claim", ""); err != nil {
		t.Fatalf("clustarr grabarr: %v", err)
	}
	if err := got.Validate(); err == nil {
		t.Error("a grabarr controller with no data claim to stamp was accepted")
	}
}

func TestIndexarrRejectsLeaderElection(t *testing.T) {
	got := stub(t, &runIndexarr)
	if _, err := execute(t, "indexarr", "--namespace", "clustarr", "--leader-elect"); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if err := got.Validate(); err == nil {
		t.Fatal("indexarr accepted --leader-elect")
	}
}

func TestGrabarrManagerOptions(t *testing.T) {
	got := stub(t, &runGrabarr)

	if _, err := execute(t, "grabarr",
		"--role", "torrent-engine",
		"--engine", "qbit-0",
		"--namespace", "clustarr",
		"--leader-elect",
	); err != nil {
		t.Fatalf("clustarr grabarr: %v", err)
	}
	if got.Role != grabarr.RoleTorrentEngine || got.Engine != "qbit-0" {
		t.Errorf("role = %q, engine = %q", got.Role, got.Engine)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the parsed options are invalid: %v", err)
	}
	if got.ManagerOptions().LeaderElection {
		t.Error("an engine took a leader lease; each ordinal owns a disjoint set of Downloads")
	}
}

func TestGrabarrEngineRoleNeedsAnIdentity(t *testing.T) {
	got := stub(t, &runGrabarr)
	if _, err := execute(t, "grabarr", "--role", "usenet-engine", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr grabarr: %v", err)
	}
	if err := got.Validate(); err == nil {
		t.Fatal("an engine role without --engine was accepted")
	}
}

func TestSquasharrManagerOptionsAndSlots(t *testing.T) {
	got := stub(t, &runSquasharr)

	if _, err := execute(t, "squasharr",
		"--role", "controller",
		"--slots", "cpu=4,nvidia=2,intel=0",
		"--namespace", "clustarr",
		"--leader-elect",
		"--worker-image", "ghcr.io/mediactl/clustarr/media:dev",
		"--worker-image-cuda", "ghcr.io/mediactl/clustarr/media-cuda:dev",
	); err != nil {
		t.Fatalf("clustarr squasharr: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the parsed options are invalid: %v", err)
	}
	want := map[string]int32{"cpu": 4, "nvidia": 2, "intel": 0}
	for hardware, budget := range want {
		if got.Slots[hardware] != budget {
			t.Errorf("slots[%q] = %d, want %d", hardware, got.Slots[hardware], budget)
		}
	}
	if !got.ManagerOptions().LeaderElection {
		t.Error("the squasharr controller did not take the leader lease")
	}
	if got.WorkerImage != "ghcr.io/mediactl/clustarr/media:dev" || got.WorkerImageCUDA != "ghcr.io/mediactl/clustarr/media-cuda:dev" {
		t.Errorf("worker images = %q / %q, want the --worker-image/--worker-image-cuda values", got.WorkerImage, got.WorkerImageCUDA)
	}
	// Unset, the Jobs run as config/'s squasharr-worker and mount its claim.
	if got.WorkerServiceAccount != squasharr.DefaultWorkerServiceAccount {
		t.Errorf("WorkerServiceAccount = %q, want the default %q", got.WorkerServiceAccount, squasharr.DefaultWorkerServiceAccount)
	}
	if got.DataClaimName != "clustarr-data" {
		t.Errorf("DataClaimName = %q, want clustarr-data", got.DataClaimName)
	}
}

// The squasharr Deployment configures its Jobs by environment, not argv:
// config/manager sets the two images, and the chart also sets the worker
// ServiceAccount and data claim, whose names carry the release fullname.
// Each variable must reach its flag, or the chart's Jobs would run as an
// account that does not exist.
func TestSquasharrWorkerSettingsComeFromTheEnvironment(t *testing.T) {
	t.Setenv(workerImageEnv, "registry.example/media:1")
	t.Setenv(workerImageCUDAEnv, "registry.example/media-cuda:1")
	t.Setenv(workerServiceAccountEnv, "release-clustarr-squasharr-worker")
	t.Setenv(dataClaimEnv, "release-clustarr-data")
	got := stub(t, &runSquasharr)
	if _, err := execute(t, "squasharr", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr squasharr: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the environment-configured options are invalid: %v", err)
	}
	for name, pair := range map[string][2]string{
		workerImageEnv:          {got.WorkerImage, "registry.example/media:1"},
		workerImageCUDAEnv:      {got.WorkerImageCUDA, "registry.example/media-cuda:1"},
		workerServiceAccountEnv: {got.WorkerServiceAccount, "release-clustarr-squasharr-worker"},
		dataClaimEnv:            {got.DataClaimName, "release-clustarr-data"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("$%s: got %q, want %q", name, pair[0], pair[1])
		}
	}

	// And the controller refuses to start without an image to stamp.
	t.Setenv(workerImageEnv, "")
	if _, err := execute(t, "squasharr", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr squasharr: %v", err)
	}
	if err := got.Validate(); err == nil {
		t.Error("a squasharr controller with no worker image was accepted")
	}
}

func TestSquasharrRejectsABadSlotFlag(t *testing.T) {
	stub(t, &runSquasharr)
	if _, err := execute(t, "squasharr", "--slots", "gpu=1"); err == nil {
		t.Fatal("an unknown hardware class was accepted")
	}
	if _, err := execute(t, "squasharr", "--slots", "cpu"); err == nil {
		t.Fatal("a slot entry with no budget was accepted")
	}
}

func TestCaptionarrManagerOptions(t *testing.T) {
	got := stub(t, &runCaptionarr)

	if _, err := execute(t, "captionarr",
		"--role", "worker",
		"--namespace", "clustarr",
		"--leader-elect",
		"--data-dir", "/data",
	); err != nil {
		t.Fatalf("clustarr captionarr: %v", err)
	}
	if got.Role != captionarr.RoleWorker {
		t.Errorf("role = %q", got.Role)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the parsed options are invalid: %v", err)
	}
	// §12 runs two fetch workers so fetches proceed in parallel; the shared
	// KV token bucket is what bounds them, not a lease.
	if got.ManagerOptions().LeaderElection {
		t.Error("a captionarr worker took a leader lease")
	}
}

func TestUnknownRoleIsRejected(t *testing.T) {
	got := stub(t, &runCatalogarr)
	if _, err := execute(t, "catalogarr", "--role", "nonsense", "--namespace", "clustarr"); err != nil {
		t.Fatalf("clustarr catalogarr: %v", err)
	}
	if err := got.Validate(); err == nil {
		t.Fatal("an unknown --role was accepted")
	}
}

func TestEverySubcommandBuildsManagerOptions(t *testing.T) {
	// One place that proves every controller-runtime-backed service can
	// render manager options without a cluster, a kubeconfig or a NATS
	// server. ui is absent: it builds no ctrl.Options, having no manager.
	cases := map[string]func() ctrl.Options{
		"catalogarr": func() ctrl.Options {
			o := catalogarr.DefaultOptions()
			o.Namespace = "clustarr"
			return o.ManagerOptions()
		},
		"importarr": func() ctrl.Options {
			o := importarr.DefaultOptions()
			o.Namespace = "clustarr"
			return o.ManagerOptions()
		},
		"indexarr": func() ctrl.Options {
			o := indexarr.DefaultOptions()
			o.Namespace = "clustarr"
			return o.ManagerOptions()
		},
		"grabarr": func() ctrl.Options {
			o := grabarr.DefaultOptions()
			o.Namespace = "clustarr"
			return o.ManagerOptions()
		},
		"squasharr": func() ctrl.Options {
			o := squasharr.DefaultOptions()
			o.Namespace = "clustarr"
			return o.ManagerOptions()
		},
		"captionarr": func() ctrl.Options {
			o := captionarr.DefaultOptions()
			o.Namespace = "clustarr"
			return o.ManagerOptions()
		},
	}
	wantIDs := map[string]string{
		"catalogarr": catalogarr.LeaderElectionID,
		"importarr":  importarr.LeaderElectionID,
		"indexarr":   indexarr.LeaderElectionID,
		"grabarr":    grabarr.LeaderElectionID,
		"squasharr":  squasharr.LeaderElectionID,
		"captionarr": captionarr.LeaderElectionID,
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			opts := build()
			if opts.Scheme == nil {
				t.Fatal("no scheme")
			}
			if opts.LeaderElectionID != wantIDs[name] {
				t.Errorf("LeaderElectionID = %q, want %q", opts.LeaderElectionID, wantIDs[name])
			}
			if opts.LeaderElectionID != name+".clustarr.io" {
				t.Errorf("LeaderElectionID = %q does not follow §2's <service>.clustarr.io", opts.LeaderElectionID)
			}
			if opts.HealthProbeBindAddress != k8s.DefaultHealthProbeBindAddress {
				t.Errorf("health probe address = %q", opts.HealthProbeBindAddress)
			}
		})
	}
}

func TestOffsetAddress(t *testing.T) {
	cases := []struct {
		addr string
		n    int
		want string
	}{
		{":8443", 0, ":8443"},
		{":8443", 3, ":8446"},
		{"127.0.0.1:8081", 2, "127.0.0.1:8083"},
		{"0", 4, "0"},
		{"", 4, ""},
	}
	for _, c := range cases {
		got, err := offsetAddress(c.addr, c.n)
		if err != nil {
			t.Errorf("offsetAddress(%q, %d): %v", c.addr, c.n, err)
			continue
		}
		if got != c.want {
			t.Errorf("offsetAddress(%q, %d) = %q, want %q", c.addr, c.n, got, c.want)
		}
	}
	if _, err := offsetAddress("noport", 1); err == nil {
		t.Error("an address with no port was accepted")
	}
	if _, err := offsetAddress(":http", 1); err == nil {
		t.Error("an address with a named port was accepted")
	}
	if _, err := offsetAddress(":65535", 2); err == nil {
		t.Error("an offset past the port range was accepted")
	}
}

// TestAllGivesEachServiceItsOwnPorts also covers review round 1's finding 1b:
// `clustarr all` must thread the root's --log-*/--tracing-* flags into every
// service it starts (previously they were silently accepted and had no
// effect, since each service's Options.Logging/Tracing were left zero-value)
// and must stamp every one of them with the single, honest
// Tracing.ServiceName "clustarr" -- pkg/obs/tracing.Setup now installs at
// most one TracerProvider per process, so a per-service name would just be
// whichever service's Run happened to call Setup first.
func TestAllGivesEachServiceItsOwnPorts(t *testing.T) {
	// Six managers with their own metrics/health ports in one process;
	// without distinct ports most of them would fail to bind. ui is stubbed
	// too -- it has no ports of its own to offset, and only stubbing it
	// keeps `all` from starting a real HTTP server that would otherwise
	// block runAll's WaitGroup until the test's context is torn down.
	// runAll starts every service concurrently, so the recorder has to be
	// safe for concurrent use.
	var (
		mu           sync.Mutex
		seenMetrics  = map[string]string{}
		seenProbes   = map[string]string{}
		seenLogging  = map[string]logging.Options{}
		seenTracing  = map[string]tracing.Options{}
		seenValidate = map[string]func() error{}
	)
	// validate is recorded as a closure over the service's OWN Options type,
	// not the embedded k8s.Options: the stub below replaces Run, and Run is
	// the only thing that would otherwise have called Validate, so a service
	// whose per-service defaults `all` failed to apply (importarr's DataPath)
	// would sail through this test. See TestAllPassesEveryServiceValidation.
	record := func(name string, o k8s.Options, lo logging.Options, to tracing.Options, validate func() error) {
		mu.Lock()
		defer mu.Unlock()
		seenMetrics[name] = o.MetricsBindAddress
		seenProbes[name] = o.HealthProbeBindAddress
		seenLogging[name] = lo
		seenTracing[name] = to
		seenValidate[name] = validate
	}
	restore := []func(){}

	origCatalog, origImport, origIndex := runCatalogarr, runImportarr, runIndexarr
	origGrab, origSquash, origCaption, origUI := runGrabarr, runSquasharr, runCaptionarr, runUI
	runCatalogarr = func(_ context.Context, o catalogarr.Options) error {
		record("catalogarr", o.Options, o.Logging, o.Tracing, o.Validate)
		return nil
	}
	runImportarr = func(_ context.Context, o importarr.Options) error {
		record("importarr", o.Options, o.Logging, o.Tracing, o.Validate)
		return nil
	}
	var indexOpts indexarr.Options
	runIndexarr = func(_ context.Context, o indexarr.Options) error {
		mu.Lock()
		indexOpts = o
		mu.Unlock()
		record("indexarr", o.Options, o.Logging, o.Tracing, o.Validate)
		return nil
	}
	runGrabarr = func(_ context.Context, o grabarr.Options) error {
		record("grabarr", o.Options, o.Logging, o.Tracing, o.Validate)
		return nil
	}
	runSquasharr = func(_ context.Context, o squasharr.Options) error {
		record("squasharr", o.Options, o.Logging, o.Tracing, o.Validate)
		return nil
	}
	var captionRole captionarr.Role
	runCaptionarr = func(_ context.Context, o captionarr.Options) error {
		mu.Lock()
		captionRole = o.Role
		mu.Unlock()
		record("captionarr", o.Options, o.Logging, o.Tracing, o.Validate)
		return nil
	}
	var uiOpts ui.Options
	runUI = func(_ context.Context, o ui.Options) error {
		mu.Lock()
		uiOpts = o
		mu.Unlock()
		record("ui", k8s.Options{}, o.Logging, o.Tracing, o.Validate)
		return nil
	}
	restore = append(restore, func() {
		runCatalogarr, runImportarr, runIndexarr = origCatalog, origImport, origIndex
		runGrabarr, runSquasharr, runCaptionarr, runUI = origGrab, origSquash, origCaption, origUI
	})
	t.Cleanup(func() {
		for _, f := range restore {
			f()
		}
	})

	if _, err := execute(t, "all",
		"--namespace", "clustarr",
		"--log-level", "warn",
		"--tracing-enabled",
		"--tracing-sample-ratio", "0.5",
		"--ui-bind-address", "127.0.0.1:18080",
	); err != nil {
		t.Fatalf("clustarr all: %v", err)
	}
	// captionarr runs its fetch worker too (X14): with the controller role
	// alone, `clustarr all` published fetch tasks nothing consumed.
	if !captionRole.RunsControllers() || !captionRole.RunsWorkers() {
		t.Errorf("captionarr: Role = %q, want one that runs the controllers and the fetch worker", captionRole)
	}
	// ui binds --ui-bind-address (X14); before it, `clustarr all`'s ui
	// always bound :8080, with no flag.
	if uiOpts.BindAddress != "127.0.0.1:18080" {
		t.Errorf("ui: BindAddress = %q, want --ui-bind-address's 127.0.0.1:18080", uiOpts.BindAddress)
	}

	// ui is recorded too (for Validate below) but owns no metrics or probe
	// port to offset, so it is not part of the distinct-address check.
	delete(seenMetrics, "ui")
	delete(seenProbes, "ui")
	if len(seenMetrics) != 6 {
		t.Fatalf("started %d services with their own ports, want 6: %v", len(seenMetrics), seenMetrics)
	}
	assertDistinct(t, "metrics", seenMetrics)
	assertDistinct(t, "health probe", seenProbes)

	// The regression this case exists for: `clustarr all` built importarr's
	// Options as a bare struct literal, leaving DataPath at "" -- which
	// Validate rejects, and which runAll turns into "every other service is
	// cancelled". Run is stubbed above, so nothing else in this test would
	// ever call Validate.
	if len(seenValidate) != 7 {
		t.Fatalf("recorded Validate for %d services, want 7: %v", len(seenValidate), keysOf(seenValidate))
	}
	for name, validate := range seenValidate {
		if err := validate(); err != nil {
			t.Errorf("%s: Options.Validate() = %v, want nil: `clustarr all` must give every "+
				"service options it would itself accept", name, err)
		}
	}

	for name, lo := range seenLogging {
		if lo.Level != slog.LevelWarn {
			t.Errorf("%s: Logging.Level = %v, want the --log-level warn given on the command line", name, lo.Level)
		}
	}
	for name, to := range seenTracing {
		if !to.Enabled {
			t.Errorf("%s: Tracing.Enabled = false, want true (--tracing-enabled)", name)
		}
		if to.SampleRatio != 0.5 {
			t.Errorf("%s: Tracing.SampleRatio = %v, want 0.5 (--tracing-sample-ratio)", name, to.SampleRatio)
		}
		if to.ServiceName != "clustarr" {
			t.Errorf("%s: Tracing.ServiceName = %q, want \"clustarr\": one process, one TracerProvider",
				name, to.ServiceName)
		}
	}
	// indexarr's Torznab facade and ui are the two HTTP servers `all` runs
	// that are not offset per service, and both default to :8080. Whichever
	// bound second would fail, and runAll would cancel the whole stack.
	uiAddr := uiOpts.BindAddress
	if uiAddr == "" {
		uiAddr = ui.DefaultBindAddress
	}
	if !indexOpts.FacadeEnabled() {
		t.Errorf("indexarr: the Torznab facade is disabled under `clustarr all --namespace clustarr`")
	}
	if portOf(t, indexOpts.FacadeBindAddress) == portOf(t, uiAddr) {
		t.Errorf("indexarr's facade (%q) and ui (%q) share a port in `clustarr all`",
			indexOpts.FacadeBindAddress, uiAddr)
	}

	if uiOpts.Logging.Level != slog.LevelWarn {
		t.Errorf("ui: Logging.Level = %v, want warn", uiOpts.Logging.Level)
	}
	if !uiOpts.Tracing.Enabled || uiOpts.Tracing.SampleRatio != 0.5 || uiOpts.Tracing.ServiceName != "clustarr" {
		t.Errorf("ui: Tracing = %+v, want Enabled=true SampleRatio=0.5 ServiceName=\"clustarr\"", uiOpts.Tracing)
	}
}

// portOf returns addr's port.
func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	return port
}

func assertDistinct(t *testing.T, what string, addrs map[string]string) {
	t.Helper()
	byAddr := map[string]string{}
	for service, addr := range addrs {
		if other, clash := byAddr[addr]; clash {
			t.Errorf("%s address %q is shared by %s and %s", what, addr, other, service)
		}
		byAddr[addr] = service
	}
}

// keysOf returns m's keys sorted, for a deterministic failure message.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
