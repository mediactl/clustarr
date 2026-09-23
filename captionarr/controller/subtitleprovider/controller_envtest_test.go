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

package subtitleprovider_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/controller/subtitleprovider"
	"github.com/mediactl/clustarr/captionarr/throttle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// testKV mirrors captionarr/throttle/state_test.go's identical helper: a
// clustarr-provider-throttle bucket bound to an in-memory bus, independent of
// the envtest apiserver newTestClient starts. The provider controller reads
// this bucket and the CRD's own client separately -- exactly as it does in
// production, where NATS and the apiserver are two different backends.
func testKV(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default()))
	return bus.KV(events.BucketProviderThrottle)
}

func createSecret(t *testing.T, ctx context.Context, c client.Client, ns, name string, data map[string]string) {
	t.Helper()
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		StringData: data,
	}
	require.NoError(t, c.Create(ctx, s))
}

func createProvider(t *testing.T, ctx context.Context, c client.Client, ns, name string, spec subtitlev1alpha1.SubtitleProviderSpec) *subtitlev1alpha1.SubtitleProvider {
	t.Helper()
	sp := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       spec,
	}
	require.NoError(t, c.Create(ctx, sp))
	return sp
}

func ensureNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))
}

// TestReconcileEmbeddedProviderIsReadyWithNoCredentials proves the
// no-credentials-required path: embedded needs no Secret at all and must
// come up Ready on its very first reconcile, with a steady-state requeue
// scheduled to keep its KV-derived fields from going stale.
func TestReconcileEmbeddedProviderIsReadyWithNoCredentials(t *testing.T) {
	c := newTestClient(t)
	kv := testKV(t)
	ctx := context.Background()
	const ns = "subtitleprovider-embedded"
	ensureNamespace(t, ctx, c, ns)

	sp := createProvider(t, ctx, c, ns, "embedded", subtitlev1alpha1.SubtitleProviderSpec{
		Type: subtitlev1alpha1.SubtitleProviderEmbedded, Enabled: true,
	})

	r := subtitleprovider.NewReconciler(c, kv, k8sevents.NewFakeRecorder(10))
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name, Namespace: ns}})
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0), "an enabled, implemented provider must schedule a steady-state requeue")

	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, &got))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated))
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionThrottled))
	assert.True(t, got.Status.HIVerifiable)

	// Ownership: PatchProvider applies under k8s.ManagerCaptionarr against the
	// status subresource -- assert managedFields directly, per CLAUDE.md's
	// "an over-claim is silent" rule.
	var sawStatusOwner bool
	for _, mfEntry := range got.ManagedFields {
		if mfEntry.Manager == string(k8s.ManagerCaptionarr) && mfEntry.Subresource == "status" {
			sawStatusOwner = true
		}
	}
	assert.True(t, sawStatusOwner, "k8s.ManagerCaptionarr must own SubtitleProvider.status")
}

// TestReconcileOpenSubtitlesComWithoutASecretIsNotAuthenticated proves a
// provider type that DOES need credentials, with no spec.secretRef at all,
// reports Authenticated=False and Ready=False -- and that Reconcile returns
// no error: a missing Secret is a status outcome, not a reconcile failure.
func TestReconcileOpenSubtitlesComWithoutASecretIsNotAuthenticated(t *testing.T) {
	c := newTestClient(t)
	kv := testKV(t)
	ctx := context.Background()
	const ns = "subtitleprovider-nosecret"
	ensureNamespace(t, ctx, c, ns)

	sp := createProvider(t, ctx, c, ns, "opensubtitles", subtitlev1alpha1.SubtitleProviderSpec{
		Type: subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, Enabled: true,
	})

	r := subtitleprovider.NewReconciler(c, kv, k8sevents.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name, Namespace: ns}})
	require.NoError(t, err)

	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, &got))
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated))
	cond := k8s.FindCondition(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated)
	require.NotNil(t, cond)
	assert.Equal(t, k8s.ReasonDependencyNotReady, cond.Reason)
}

// TestReconcileOpenSubtitlesComWithAValidSecretIsReady is the mirror-image
// happy path: every required key present authenticates and, with no KV
// throttle recorded, comes up Ready.
func TestReconcileOpenSubtitlesComWithAValidSecretIsReady(t *testing.T) {
	c := newTestClient(t)
	kv := testKV(t)
	ctx := context.Background()
	const ns = "subtitleprovider-validsecret"
	ensureNamespace(t, ctx, c, ns)

	createSecret(t, ctx, c, ns, "os-creds", map[string]string{"apiKey": "k", "username": "u", "password": "p"})
	sp := createProvider(t, ctx, c, ns, "opensubtitles", subtitlev1alpha1.SubtitleProviderSpec{
		Type: subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, Enabled: true,
		SecretRef: &corev1.LocalObjectReference{Name: "os-creds"},
	})

	r := subtitleprovider.NewReconciler(c, kv, k8sevents.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name, Namespace: ns}})
	require.NoError(t, err)

	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, &got))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated))
}

// TestReconcileDisabledProviderIsNotReadyEvenWhenAuthenticated proves Ready
// factors in spec.enabled independently of Authenticated.
func TestReconcileDisabledProviderIsNotReadyEvenWhenAuthenticated(t *testing.T) {
	c := newTestClient(t)
	kv := testKV(t)
	ctx := context.Background()
	const ns = "subtitleprovider-disabled"
	ensureNamespace(t, ctx, c, ns)

	sp := createProvider(t, ctx, c, ns, "embedded", subtitlev1alpha1.SubtitleProviderSpec{
		Type: subtitlev1alpha1.SubtitleProviderEmbedded, Enabled: true,
	})
	// Enabled is `bool` with `json:"enabled,omitempty"` and a
	// +kubebuilder:default=true CRD default (matching TranscodeProfileSpec's
	// identical Default field elsewhere in this tree). A typed client.Create
	// or client.Update carrying Enabled: false marshals it as an ABSENT key
	// (omitempty drops a bool zero value), so the apiserver's own default
	// silently coerces it back to true -- a real Kubernetes/codegen gotcha,
	// not a bug in the controller under test. A hand-built JSON merge patch
	// bypasses Go's typed marshalling and sends the literal false.
	require.NoError(t, c.Patch(ctx, sp, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"enabled":false}}`))))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, sp))
	require.False(t, sp.Spec.Enabled, "test fixture setup must actually persist enabled:false before reconciling")

	r := subtitleprovider.NewReconciler(c, kv, k8sevents.NewFakeRecorder(10))
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name, Namespace: ns}})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "a disabled provider needs no steady-state poll")

	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, &got))
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated),
		"Authenticated reports the real credential state regardless of spec.enabled")
	cond := k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, k8s.ReasonDisabled, cond.Reason)
}

// TestReconcileUnsupportedProviderTypeNeverErrorsOrAuthenticates is ruling
// R5's own test: subdl has no client. Reconcile must return no error (not an
// error loop) and report Ready=False with a clear reason.
func TestReconcileUnsupportedProviderTypeNeverErrorsOrAuthenticates(t *testing.T) {
	c := newTestClient(t)
	kv := testKV(t)
	ctx := context.Background()
	const ns = "subtitleprovider-unsupported"
	ensureNamespace(t, ctx, c, ns)

	sp := createProvider(t, ctx, c, ns, "subdl", subtitlev1alpha1.SubtitleProviderSpec{
		Type: subtitlev1alpha1.SubtitleProviderSubDL, Enabled: true,
	})

	r := subtitleprovider.NewReconciler(c, kv, k8sevents.NewFakeRecorder(10))
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name, Namespace: ns}})
	require.NoError(t, err, "an unimplemented provider type must never fail the reconcile")
	assert.Zero(t, res.RequeueAfter, "nothing external can resolve an unimplemented type; no poll is scheduled")

	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, &got))
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	readyCond := k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady)
	require.NotNil(t, readyCond)
	assert.Equal(t, subtitleprovider.ReasonNotImplemented, readyCond.Reason)

	authCond := k8s.FindCondition(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionAuthenticated)
	require.NotNil(t, authCond)
	assert.Equal(t, metav1.ConditionUnknown, authCond.Status)
	assert.False(t, got.Status.HIVerifiable)
}

// TestReconcileProjectsThrottleStateFromKV proves the load-bearing half of
// ruling R2: a throttle a worker recorded into the shared KV bucket must be
// visible on the object's status, and the requeue must be timed to the
// throttle's own expiry rather than the full steady-state tick.
func TestReconcileProjectsThrottleStateFromKV(t *testing.T) {
	c := newTestClient(t)
	kv := testKV(t)
	ctx := context.Background()
	const ns = "subtitleprovider-throttled"
	ensureNamespace(t, ctx, c, ns)

	sp := createProvider(t, ctx, c, ns, "embedded", subtitlev1alpha1.SubtitleProviderSpec{
		Type: subtitlev1alpha1.SubtitleProviderEmbedded, Enabled: true,
	})

	now := time.Now().UTC()
	cause := &subtitles.ProviderError{Provider: "embedded", Kind: subtitles.KindServiceUnavailable}
	_, err := throttle.RecordError(ctx, kv, string(subtitlev1alpha1.SubtitleProviderEmbedded), string(sp.UID), cause, now)
	require.NoError(t, err)

	r := subtitleprovider.NewReconciler(c, kv, k8sevents.NewFakeRecorder(10))
	r.Clock = clockwork.NewFakeClockAt(now)
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name, Namespace: ns}})
	require.NoError(t, err)

	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, &got))
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionThrottled))
	require.NotNil(t, got.Status.ThrottledUntil)
	assert.True(t, got.Status.ThrottledUntil.After(now))
	assert.NotEmpty(t, got.Status.ThrottleReason)

	assert.Greater(t, res.RequeueAfter, time.Duration(0))
	assert.LessOrEqual(t, res.RequeueAfter, 20*time.Minute, // ServiceUnavailable's table duration is 20m
		"a throttled provider must requeue at or before its own throttle expiry, not the full steady tick")
}

// TestReconcileNeverWritesJWTIntoStatus is a defensive regression test for
// throttle.State's own doc comment: JWT is "deliberately NEVER projected
// into SubtitleProvider.status ... credentials do not belong on a CRD".
// SubtitleProviderStatusApplyConfiguration structurally has no field for it,
// so this asserts against the object's raw JSON rather than a Go field that
// cannot exist to be wrong.
func TestReconcileNeverWritesJWTIntoStatus(t *testing.T) {
	c := newTestClient(t)
	kv := testKV(t)
	ctx := context.Background()
	const ns = "subtitleprovider-jwt"
	ensureNamespace(t, ctx, c, ns)

	sp := createProvider(t, ctx, c, ns, "opensubtitles", subtitlev1alpha1.SubtitleProviderSpec{
		Type: subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, Enabled: true,
	})

	const secretJWT = "eyJ-totally-secret-token-value"
	expiry := time.Now().Add(24 * time.Hour)
	_, err := throttle.SetAuth(ctx, kv, string(sp.UID), secretJWT, expiry)
	require.NoError(t, err)

	r := subtitleprovider.NewReconciler(c, kv, k8sevents.NewFakeRecorder(10))
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name, Namespace: ns}})
	require.NoError(t, err)

	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name, Namespace: ns}, &got))
	require.NotNil(t, got.Status.TokenExpiresAt, "TokenExpiresAt IS projected -- it is a timestamp, not a secret")

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secretJWT, "the cached JWT must never reach the object")
}
