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

package remediationtest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/test/starttest"
)

// Env is one envtest apiserver with the loop's environment over it.
type Env struct {
	Config *rest.Config
	// Admin is an uncached client whose own writes read as manager
	// "remediationtest" in managedFields; Get reads through it.
	Admin client.Client
	Bus   *membus.Bus
	// Loop is the environment every Reconciler this Env builds shares: a
	// change to it (Loop.IO, Loop.Bus) reaches reconcilers already built.
	Loop     *remediation.Env
	Recorder *k8sevents.FakeRecorder
	// OnStatusPatch, when set, runs inside the loop client's status write
	// before it is sent, so a test can observe what exists at the moment of
	// the apply. Its argument names the object written.
	OnStatusPatch func(client.Object)

	c   *countingClient
	lag *laggingReader
}

// Bluray1080p is a Bluray-1080p quality.
var Bluray1080p = commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}

// Start starts an envtest apiserver (skipping without KUBEBUILDER_ASSETS)
// and the loop's environment over it: an uncached reader that emulates the
// manager cache's indexes (NewRawClient) behind a lagging reader a test can
// freeze, a writer that counts status writes, an ensured membus, a
// four-worker I/O executor and a records pacer.
func Start(t testing.TB) *Env {
	t.Helper()
	env := starttest.StartEnv(t)
	return newEnv(t, env.Config)
}

func newEnv(t testing.TB, cfg *rest.Config) *Env {
	t.Helper()
	scheme := k8s.MustNewScheme()
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	loopClient, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	e := &Env{
		Config:   cfg,
		Admin:    client.WithFieldOwner(admin, "remediationtest"),
		Bus:      bus,
		Recorder: k8sevents.NewFakeRecorder(256),
	}
	raw := NewRawClient(t, client.WithFieldOwner(loopClient, k8s.DefaultFieldOwner))
	e.c = &countingClient{Client: raw, env: e}
	e.lag = &laggingReader{Reader: raw, frozen: map[types.NamespacedName]client.Object{}}
	e.Loop = &remediation.Env{
		Reader: e.lag, APIReader: e.Admin, Bus: bus,
		IO:    remediation.NewIOExecutor(4),
		Pacer: records.NewPacer(records.DefaultRates(), time.Now),
	}
	return e
}

// Client is the loop's writer: the raw client, its status writes counted.
func (e *Env) Client() client.Client { return e.c }

// Reconciler is the loop over planners with the default Config.
func (e *Env) Reconciler(t testing.TB, planners ...remediation.Bound) *remediation.Reconciler {
	t.Helper()
	return e.ReconcilerWith(t, remediation.Config{}, planners...)
}

// ReconcilerWith is the loop over planners with cfg.
func (e *Env) ReconcilerWith(t testing.TB, cfg remediation.Config, planners ...remediation.Bound) *remediation.Reconciler {
	t.Helper()
	r, err := remediation.NewReconciler(e.c, e.Loop, e.Recorder, cfg, planners)
	require.NoError(t, err)
	return r
}

// ReconcilerSharing is the loop over planners pacing through r's
// status-write limiter.
func (e *Env) ReconcilerSharing(t testing.TB, r *remediation.Reconciler, planners ...remediation.Bound) *remediation.Reconciler {
	t.Helper()
	r2 := e.Reconciler(t, planners...)
	remediation.ShareLimiter(r2, r)
	return r2
}

// ReconcilerFor is Reconciler without a testing.T; it panics on an error.
func (e *Env) ReconcilerFor(planners ...remediation.Bound) *remediation.Reconciler {
	r, err := remediation.NewReconciler(e.c, e.Loop, e.Recorder, remediation.Config{}, planners)
	if err != nil {
		panic(err)
	}
	return r
}

// Pass runs one file key's pass through r.
func (e *Env) Pass(t testing.TB, r *remediation.Reconciler, nn types.NamespacedName) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), remediation.Key{Kind: remediation.KindMediaFile, Namespace: nn.Namespace, Name: nn.Name})
	require.NoError(t, err)
	return res
}

// File creates namespace ns, the QualityProfile "hd", a Movie "heat" and a
// MediaFile name at path, its spec applied as the import domain does.
func (e *Env) File(t testing.TB, ns, name, path string) types.NamespacedName {
	t.Helper()
	ctx := context.Background()
	MustNamespace(t, ctx, e.Admin, ns)
	MustQualityProfile(t, ctx, e.Admin, "hd")
	MustMovie(t, ctx, e.Admin, ns, "heat", "hd")
	ImportCreatesMediaFileFor(t, ctx, e.Admin, ns, name, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
		path, 1000, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), Bluray1080p)
	return types.NamespacedName{Namespace: ns, Name: name}
}

// Get reads nn through Admin, managedFields kept.
func (e *Env) Get(t testing.TB, nn types.NamespacedName) *catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, e.Admin.Get(context.Background(), nn, &mf))
	return &mf
}

// StatusPatches is how many status writes the loop's client has sent.
func (e *Env) StatusPatches() int { return int(e.c.statusWrites.Load()) }

// Freeze makes the loop's reader serve obj for nn, as a cache that has not
// caught up would; Thaw ends it.
func (e *Env) Freeze(nn types.NamespacedName, obj client.Object) { e.lag.freeze(nn, obj) }

// Thaw ends Freeze.
func (e *Env) Thaw(nn types.NamespacedName) { e.lag.thaw(nn) }

// HangStats replaces Loop.IO with an executor of workers whose stat blocks
// on a path under prefix until release, each call timing out after timeout.
func (e *Env) HangStats(t testing.TB, prefix string, workers int, timeout time.Duration) (release func()) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	e.Loop.IO = remediation.NewIOExecutorWith(workers, timeout, func(path string) (fs.FileInfo, error) {
		if strings.HasPrefix(path, prefix) {
			<-ch
		}
		return os.Stat(path)
	})
	return release
}

// Drain returns and clears rec's buffered Events.
func Drain(rec *k8sevents.FakeRecorder) []string {
	var out []string
	for {
		select {
		case ev := <-rec.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// StartController starts a real manager running the loop over planners as
// controller "mediafile", on a fresh membus.
func StartController(t testing.TB, planners ...remediation.Bound) *Env {
	t.Helper()
	return StartControllerOn(t, nil, planners...)
}

// StartControllerOn is StartController with bus as the loop's bus (nil is
// the Env's membus).
func StartControllerOn(t testing.TB, bus events.Bus, planners ...remediation.Bound) *Env {
	t.Helper()
	return StartManager(t, func(mgr ctrl.Manager, b events.Bus) error {
		if err := remediation.RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
			return err
		}
		if bus == nil {
			bus = b
		}
		loop := &remediation.Env{
			Reader: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bus: bus,
			IO:    remediation.NewIOExecutor(4),
			Pacer: records.NewPacer(records.DefaultRates(), time.Now),
		}
		r, err := remediation.NewReconciler(mgr.GetClient(), loop, k8sevents.NewFakeRecorder(256), remediation.Config{Concurrency: 2}, planners)
		if err != nil {
			return err
		}
		return r.SetupWithManager(mgr)
	})
}

// StartManager starts an envtest apiserver and a real manager on it --
// controller name validation skipped, metrics and probes off -- after setup
// registered what the test runs, and waits for its cache to sync. The
// returned Env's Admin, Get and File work against it.
func StartManager(t testing.TB, setup func(ctrl.Manager, events.Bus) error) *Env {
	t.Helper()
	env := starttest.StartEnv(t)
	e := newEnv(t, env.Config)
	opts := ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: new(true)},
		Cache:                  cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
	}
	opts.Client.FieldOwner = k8s.DefaultFieldOwner
	mgr, err := ctrl.NewManager(env.Config, opts)
	require.NoError(t, err)
	require.NoError(t, setup(mgr, e.Bus))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("manager: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Errorf("manager did not stop")
		}
	})
	syncCtx, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "the manager's cache never synced")
	return e
}

// countingClient is the loop's writer: it counts every status write and
// runs Env.OnStatusPatch before sending one.
type countingClient struct {
	client.Client
	env          *Env
	statusWrites atomic.Int64
}

// Status implements client.StatusClient.
func (c *countingClient) Status() client.SubResourceWriter {
	return &countingStatus{SubResourceWriter: c.Client.Status(), c: c}
}

// SubResource implements client.SubResourceClientConstructor.
func (c *countingClient) SubResource(sub string) client.SubResourceClient {
	inner := c.Client.SubResource(sub)
	if sub != "status" {
		return inner
	}
	return &countingSubResource{SubResourceClient: inner, w: &countingStatus{SubResourceWriter: inner, c: c}}
}

type countingSubResource struct {
	client.SubResourceClient
	w *countingStatus
}

func (s *countingSubResource) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return s.w.Update(ctx, obj, opts...)
}

func (s *countingSubResource) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return s.w.Patch(ctx, obj, patch, opts...)
}

func (s *countingSubResource) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return s.w.Apply(ctx, obj, opts...)
}

type countingStatus struct {
	client.SubResourceWriter
	c *countingClient
}

func (s *countingStatus) observe(obj client.Object) {
	s.c.statusWrites.Add(1)
	if f := s.c.env.OnStatusPatch; f != nil {
		f(obj)
	}
}

func (s *countingStatus) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	s.observe(obj)
	return s.SubResourceWriter.Update(ctx, obj, opts...)
}

func (s *countingStatus) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	s.observe(obj)
	return s.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func (s *countingStatus) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	var named client.Object
	if ac, ok := obj.(interface {
		GetName() *string
		GetNamespace() *string
	}); ok {
		mf := &catalogv1alpha1.MediaFile{}
		if n := ac.GetName(); n != nil {
			mf.Name = *n
		}
		if ns := ac.GetNamespace(); ns != nil {
			mf.Namespace = *ns
		}
		named = mf
	}
	s.observe(named)
	return s.SubResourceWriter.Apply(ctx, obj, opts...)
}

// laggingReader serves a frozen object for a key, as a cache that has not
// caught up with a write would.
type laggingReader struct {
	client.Reader
	mu     sync.Mutex
	frozen map[types.NamespacedName]client.Object
}

func (r *laggingReader) freeze(nn types.NamespacedName, obj client.Object) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen[nn] = obj.DeepCopyObject().(client.Object)
}

func (r *laggingReader) thaw(nn types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.frozen, nn)
}

// Get implements client.Reader.
func (r *laggingReader) Get(ctx context.Context, key client.ObjectKey, out client.Object, opts ...client.GetOption) error {
	r.mu.Lock()
	obj, ok := r.frozen[key]
	r.mu.Unlock()
	if mf, isMF := out.(*catalogv1alpha1.MediaFile); ok && isMF {
		if src, ok := obj.(*catalogv1alpha1.MediaFile); ok {
			src.DeepCopyInto(mf)
			return nil
		}
	}
	return r.Reader.Get(ctx, key, out, opts...)
}
