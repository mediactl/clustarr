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

package indexer_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/controller/indexer"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// testCfg is the shared control plane. It is nil when KUBEBUILDER_ASSETS is
// unset, in which case every envtest in this package SKIPS -- and a suite
// that finishes in milliseconds skipped rather than passed.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) string {
	t.Helper()
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
	return ns
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}

// capsServer answers every request with one fixed document and status. No
// test in this package reaches the network.
func capsServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newReconciler wires a bus as well as a client, because the reconciler seeds
// the RSS poll chain (ruling R36) and a nil bus would make every test in this
// file exercise the one path that does not publish. membus is enough for the
// tests that only need the seed not to explode; the ones that assert on a
// PENDING schedule use newJetStreamReconciler, because membus has no
// deduplication window and no rollup.
func newReconciler(t *testing.T, c client.Client) (*indexer.Reconciler, *k8sevents.FakeRecorder) {
	t.Helper()
	rec := k8sevents.NewFakeRecorder(10)
	return indexer.NewReconciler(c, rec, ratelimit.New(ratelimit.Config{}), newMemBus(t)), rec
}

func newMemBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func reconcileOnce(t *testing.T, r *indexer.Reconciler, name types.NamespacedName) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), reconcile.Request{NamespacedName: name})
}

func mustGet(t *testing.T, c client.Client, name types.NamespacedName) indexv1alpha1.Indexer {
	t.Helper()
	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(context.Background(), name, &got))
	return got
}

func conditionOf(t *testing.T, idx indexv1alpha1.Indexer, condType string) metav1.Condition {
	t.Helper()
	c := k8s.FindCondition(idx.Status.Conditions, condType)
	require.NotNil(t, c, "condition %s is absent", condType)
	return *c
}

func TestReconcileProbesCapsAndBecomesReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-ready")
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)

	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "nzbgeek", Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet, APIPath: "/api"},
		},
	}))

	name := types.NamespacedName{Namespace: ns, Name: "nzbgeek"}
	r, _ := newReconciler(t, c)
	res, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.Equal(t, 15*time.Minute, res.RequeueAfter)

	got := mustGet(t, c, name)
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionReady))
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionHealthy))
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionAuthenticated))
	require.True(t, k8s.IsConditionFalse(got.Status.Conditions, indexv1alpha1.IndexerConditionRateLimited))
	require.Equal(t, commonv1alpha1.ProtocolUsenet, got.Status.Protocol)
	require.Equal(t, indexer.PrivacyPublic, got.Status.Privacy)
	require.Equal(t, "nzbgeek-session", got.Status.SessionSecretRef)
	require.Equal(t, got.Generation, got.Status.ObservedGeneration)

	require.NotNil(t, got.Status.Caps)
	require.True(t, idxstatus.SupportsMode(*got.Status.Caps, "tvsearch"))
	require.True(t, idxstatus.SupportsMode(*got.Status.Caps, "movie"))
	require.False(t, idxstatus.SupportsMode(*got.Status.Caps, "book"), "an unavailable mode must not be advertised")
	require.False(t, idxstatus.SupportsMode(*got.Status.Caps, "tv-search"), "R5: the CRD stores the t= vocabulary")
	require.True(t, got.Status.Caps.SupportsRawSearch)
	require.EqualValues(t, 100, got.Status.Caps.LimitsMax)
	require.EqualValues(t, 50, got.Status.Caps.LimitsDefault)
	require.Len(t, got.Status.Caps.Categories, 3)
	require.EqualValues(t, 2000, got.Status.Caps.Categories[0].ID)
	require.Len(t, got.Status.Caps.Categories[0].Sub, 2)
	require.EqualValues(t, 2030, got.Status.Caps.Categories[0].Sub[0].ID)

	for _, cond := range got.Status.Conditions {
		require.Equal(t, got.Generation, cond.ObservedGeneration, "%s carries no observedGeneration", cond.Type)
	}
}

// TestTransientProbeFailureDoesNotReleaseCaps drives the Indexer to its real
// steady state, then breaks the upstream. Server-side apply replaces a field
// manager's ownership set per apply: an early-return path that builds a
// partial status RELEASES everything the happy path set, and the early return
// is always a transient failure -- so a healthy object gets gutted by a blip.
//
// The steady state is built by BOTH managers first. A test that creates a
// blank Indexer, triggers the failure and asserts cannot observe a release,
// because there was nothing to release.
func TestTransientProbeFailureDoesNotReleaseCaps(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-ssa")

	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "upstream on fire", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, readFixture(t, "testdata/caps.xml"))
	}))
	t.Cleanup(srv.Close)

	name := types.NamespacedName{Namespace: ns, Name: "flaky"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
		},
	}))

	r, _ := newReconciler(t, c)

	// 1. Steady state.
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	steady := mustGet(t, c, name)
	require.NotNil(t, steady.Status.Caps)
	require.NotEmpty(t, steady.Status.Caps.Categories)
	wantCaps := steady.Status.Caps.DeepCopy()

	// 2. The OTHER manager's fields, applied exactly as D1-5/D1-7 will.
	until := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerIndexarrWorker,
		indexac.Indexer(name.Name, ns).WithStatus(indexac.IndexerStatus().
			WithEscalationLevel(3).
			WithDisabledUntil(until).
			WithInitialFailureAt(until).
			WithLastFailureAt(until).
			WithLastFailure("earlier trouble").
			WithQueriesInWindow(7).
			WithGrabsInWindow(2).
			WithLastRssNewCount(11).
			WithIndexedReleases(4242)))
	require.NoError(t, err)

	// 3. The blip. Force a re-probe: the caps TTL would otherwise skip it.
	fail.Store(true)
	live := mustGet(t, c, name)
	live.Spec.Priority = 30 // a generation bump invalidates the caps memo
	require.NoError(t, c.Update(ctx, &live))
	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err, "a transient upstream failure is not a reconcile error")

	// 4. Nothing was released.
	after := mustGet(t, c, name)
	require.False(t, k8s.IsConditionTrue(after.Status.Conditions, indexv1alpha1.IndexerConditionReady))

	require.NotNil(t, after.Status.Caps, "caps were RELEASED by the failure path")
	require.Equal(t, wantCaps, after.Status.Caps)
	require.Equal(t, commonv1alpha1.ProtocolTorrent, after.Status.Protocol, "protocol was RELEASED by the failure path")
	require.Equal(t, indexer.PrivacyPublic, after.Status.Privacy, "privacy was RELEASED by the failure path")
	require.Equal(t, "flaky-session", after.Status.SessionSecretRef, "sessionSecretRef was RELEASED by the failure path")
	require.Equal(t, after.Generation, after.Status.ObservedGeneration)

	require.EqualValues(t, 3, after.Status.EscalationLevel, "the reconciler released indexarr-worker's fields")
	require.EqualValues(t, 7, after.Status.QueriesInWindow)
	require.EqualValues(t, 2, after.Status.GrabsInWindow)
	require.EqualValues(t, 11, after.Status.LastRssNewCount)
	require.EqualValues(t, 4242, after.Status.IndexedReleases)
	require.Equal(t, "earlier trouble", after.Status.LastFailure)
	require.NotNil(t, after.Status.InitialFailureAt)
	require.NotNil(t, after.Status.LastFailureAt)
	require.NotNil(t, after.Status.DisabledUntil)
}

// The second SSA regression, and the one co-ownership can hide.
//
// spec.enabled: false is an early return that never reaches the caps probe
// or the protocol/privacy resolution. Everything the happy path wrote must
// still be on the object afterwards. This is run against an Indexer already
// in its steady state, and the steady state is reached by the SAME manager,
// so there is no co-owner to leave the fields standing: a release here is
// visible.
func TestDisablingAnIndexerReleasesNothing(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-disabled")
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)

	name := types.NamespacedName{Namespace: ns, Name: "off"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet, APIPath: "/api"},
		},
	}))

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	steady := mustGet(t, c, name)
	require.NotNil(t, steady.Status.Caps)
	wantCaps := steady.Status.Caps.DeepCopy()

	steady.Spec.Enabled = ptr.To(false)
	require.NoError(t, c.Update(ctx, &steady))
	res, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter, "only a spec edit can re-enable it")

	after := mustGet(t, c, name)
	ready := conditionOf(t, after, indexv1alpha1.IndexerConditionReady)
	require.Equal(t, metav1.ConditionFalse, ready.Status)
	require.Equal(t, k8s.ReasonDisabled, ready.Reason)

	require.NotNil(t, after.Status.Caps, "the disabled path RELEASED caps")
	require.Equal(t, wantCaps, after.Status.Caps)
	require.Equal(t, commonv1alpha1.ProtocolUsenet, after.Status.Protocol, "the disabled path RELEASED protocol")
	require.Equal(t, indexer.PrivacyPublic, after.Status.Privacy, "the disabled path RELEASED privacy")
	require.Equal(t, "off-session", after.Status.SessionSecretRef, "the disabled path RELEASED sessionSecretRef")
	require.Equal(t, after.Generation, after.Status.ObservedGeneration)

	// The other three conditions must still be there too: conditions are a
	// listType=map and an apply that sends only Ready releases the rest.
	for _, condType := range []string{
		indexv1alpha1.IndexerConditionHealthy,
		indexv1alpha1.IndexerConditionAuthenticated,
		indexv1alpha1.IndexerConditionRateLimited,
	} {
		require.NotNil(t, k8s.FindCondition(after.Status.Conditions, condType),
			"the disabled path RELEASED the %s condition", condType)
	}
}

// A definition-backed Indexer cannot resolve status.protocol, and the CRD
// marks the field enum [torrent, usenet]: sending an explicit "" is an
// apiserver REJECTION of the whole apply, not a no-op. require.NoError on
// the Reconcile is what proves the trap is avoided.
func TestDefinitionBackedIndexerIsDeferredWithoutAProtocol(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-definition")

	name := types.NamespacedName{Namespace: ns, Name: "leetx"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:    "https://1337x.invalid",
			Definition: ptr.To("1337x"),
		},
	}))

	r, _ := newReconciler(t, c)
	res, err := reconcileOnce(t, r, name)
	require.NoError(t, err, "an empty status.protocol would be rejected by the enum")
	require.Zero(t, res.RequeueAfter)

	got := mustGet(t, c, name)
	ready := conditionOf(t, got, indexv1alpha1.IndexerConditionReady)
	require.Equal(t, metav1.ConditionUnknown, ready.Status)
	require.Equal(t, indexer.ReasonDefinitionNotImplemented, ready.Reason)
	require.Empty(t, got.Status.Protocol)
	require.Equal(t, "leetx-session", got.Status.SessionSecretRef)
}

// A spec with neither generic nor definition cannot be created through the
// apiserver at all: the type-level CEL rule is the real guard. resolveSource
// re-checks it in Go for the case where it is bypassed, and that path is
// asserted at the unit level in controller_test.go, where a fake client can
// produce an object the apiserver would refuse.
func TestASpecWithNoSourceIsRejectedByCEL(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-cel")

	err := c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "nosource", Namespace: ns},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: "https://x.invalid"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one of definition, definitionRef or generic must be set")
}

// An unusable spec.baseURL is terminal too: no amount of retrying fixes it,
// and a hot loop on a typo burns an apiserver.
func TestAnUnusableBaseURLIsTerminal(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-badurl")

	name := types.NamespacedName{Namespace: ns, Name: "bad"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "not-a-url",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent},
		},
	}))
	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.Error(t, err)
	require.True(t, errors.Is(err, reconcile.TerminalError(nil)), "a bad baseURL must not requeue forever")

	got := mustGet(t, c, name)
	ready := conditionOf(t, got, indexv1alpha1.IndexerConditionReady)
	require.Equal(t, metav1.ConditionFalse, ready.Status)
	require.Equal(t, k8s.ReasonInvalidSpec, ready.Reason)
}

func TestMissingSecretIsADependencyNotAFailure(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-secret")

	name := types.NamespacedName{Namespace: ns, Name: "needs-creds"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:   "https://tracker.invalid",
			Generic:   &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent},
			SecretRef: &corev1.LocalObjectReference{Name: "absent"},
		},
	}))

	r, _ := newReconciler(t, c)
	res, err := reconcileOnce(t, r, name)
	require.NoError(t, err, "a Secret the operator has not created yet is not a reconcile error")
	require.Equal(t, 15*time.Minute, res.RequeueAfter)

	got := mustGet(t, c, name)
	ready := conditionOf(t, got, indexv1alpha1.IndexerConditionReady)
	require.Equal(t, metav1.ConditionFalse, ready.Status)
	require.Equal(t, k8s.ReasonDependencyNotReady, ready.Reason)
	require.Equal(t, metav1.ConditionUnknown, conditionOf(t, got, indexv1alpha1.IndexerConditionAuthenticated).Status)
	require.Equal(t, "needs-creds-session", got.Status.SessionSecretRef)
}

func TestAnAPIKeyMakesTheIndexerPrivate(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-private")
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)

	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: ns},
		Data:       map[string][]byte{"apikey": []byte("s3cret")},
	}))

	name := types.NamespacedName{Namespace: ns, Name: "keyed"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:   srv.URL,
			Generic:   &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet, APIPath: "/api"},
			SecretRef: &corev1.LocalObjectReference{Name: "creds"},
		},
	}))

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.Equal(t, indexer.PrivacyPrivate, mustGet(t, c, name).Status.Privacy)
}

func TestRetryAfterDrivesTheRequeueAndCapsSurvive(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-429")

	var limited atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if limited.Load() {
			w.Header().Set("Retry-After", "90")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, readFixture(t, "testdata/caps.xml"))
	}))
	t.Cleanup(srv.Close)

	name := types.NamespacedName{Namespace: ns, Name: "busy"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
		},
	}))

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	wantCaps := mustGet(t, c, name).Status.Caps.DeepCopy()

	limited.Store(true)
	live := mustGet(t, c, name)
	live.Spec.Priority = 31
	require.NoError(t, c.Update(ctx, &live))
	res, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, res.RequeueAfter, "the server's Retry-After is authoritative")

	after := mustGet(t, c, name)
	require.True(t, k8s.IsConditionTrue(after.Status.Conditions, indexv1alpha1.IndexerConditionRateLimited))
	require.True(t, k8s.IsConditionFalse(after.Status.Conditions, indexv1alpha1.IndexerConditionReady))
	require.Equal(t, wantCaps, after.Status.Caps, "a 429 released the caps")
	require.EqualValues(t, 0, after.Status.EscalationLevel, "the reconciler must not write the escalation set")
}

func TestNewznabErrorCodeRejectsTheCredentials(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-auth")
	// Newznab answers errors with HTTP 200 and an <error> element.
	srv := capsServer(t, `<?xml version="1.0"?><error code="100" description="Incorrect user credentials"/>`, http.StatusOK)

	name := types.NamespacedName{Namespace: ns, Name: "rejected"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet, APIPath: "/api"},
		},
	}))

	r, rec := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	got := mustGet(t, c, name)
	auth := conditionOf(t, got, indexv1alpha1.IndexerConditionAuthenticated)
	require.Equal(t, metav1.ConditionFalse, auth.Status)
	require.Equal(t, indexer.ReasonCredentialsRejected, auth.Reason)
	require.True(t, k8s.IsConditionFalse(got.Status.Conditions, indexv1alpha1.IndexerConditionReady))

	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, "Warning")
		require.Contains(t, ev, indexer.ReasonCredentialsRejected)
	default:
		t.Fatal("no Warning Event was recorded for rejected credentials")
	}
}

func TestAFutureDisabledUntilBacksOff(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-backoff")
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)

	name := types.NamespacedName{Namespace: ns, Name: "waiting"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
		},
	}))

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	// The worker's half, applied under its own manager, exactly as
	// RecordFailure's result will be.
	until := metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerIndexarrWorker,
		indexac.Indexer(name.Name, ns).WithStatus(indexac.IndexerStatus().
			WithEscalationLevel(5).WithDisabledUntil(until)))
	require.NoError(t, err)

	res, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.InDelta(t, time.Until(until.Time)+time.Second, res.RequeueAfter, float64(5*time.Second))

	got := mustGet(t, c, name)
	healthy := conditionOf(t, got, indexv1alpha1.IndexerConditionHealthy)
	require.Equal(t, metav1.ConditionFalse, healthy.Status)
	require.Equal(t, indexer.ReasonBackingOff, healthy.Reason)
	require.True(t, k8s.IsConditionFalse(got.Status.Conditions, indexv1alpha1.IndexerConditionReady))
	require.EqualValues(t, 5, got.Status.EscalationLevel, "the reconciler released the worker's escalation level")
}

func TestLimitsExhaustedSetsRateLimitedWithoutClearingReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-limits")
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)

	name := types.NamespacedName{Namespace: ns, Name: "budgeted"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet, APIPath: "/api"},
			Limits:  &indexv1alpha1.Limits{QueryLimit: ptr.To(int32(100))},
		},
	}))

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerIndexarrWorker,
		indexac.Indexer(name.Name, ns).WithStatus(indexac.IndexerStatus().WithQueriesInWindow(100)))
	require.NoError(t, err)

	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)

	got := mustGet(t, c, name)
	limitedCond := conditionOf(t, got, indexv1alpha1.IndexerConditionRateLimited)
	require.Equal(t, metav1.ConditionTrue, limitedCond.Status)
	require.Contains(t, limitedCond.Message, "queries 100/100 per day")
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionReady),
		"a daily budget is an Indexer's expected steady state; it must not flap Ready")
}

// The caps TTL is what keeps a 15-minute tick from spending an indexer's
// query budget on a capability set that changes roughly never.
func TestCapsAreMemoisedUntilTheGenerationChanges(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-ttl")

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, readFixture(t, "testdata/caps.xml"))
	}))
	t.Cleanup(srv.Close)

	name := types.NamespacedName{Namespace: ns, Name: "memo"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
		},
	}))

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.EqualValues(t, 1, hits.Load())

	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.EqualValues(t, 1, hits.Load(), "the tick must not re-probe a warm memo at an unchanged generation")

	live := mustGet(t, c, name)
	live.Spec.Priority = 32
	require.NoError(t, c.Update(ctx, &live))
	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.EqualValues(t, 2, hits.Load(), "a spec change must re-probe")
}

// A request for an Indexer that is not there -- deleted between the watch
// event and the Get, or never created -- reconciles cleanly and does not
// requeue. Indexer carries no finalizer, so this is in fact the ordinary
// delete path.
//
// What happens to the two caches on a delete is NOT covered here: the caps
// memo is pruned and the per-host limiter bucket deliberately is not, and
// both are asserted in TestDeletionPrunesTheCapsMemoButNotTheSharedBucket
// (controller_test.go), which needs a fake client to hold the object open
// long enough for the DeletionTimestamp branch to run.
func TestAMissingIndexerReconcilesCleanly(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-delete")

	r, _ := newReconciler(t, c)
	res, err := reconcileOnce(t, r, types.NamespacedName{Namespace: ns, Name: "never-existed"})
	require.NoError(t, err, "a NotFound is not an error")
	require.Zero(t, res.RequeueAfter)
}
