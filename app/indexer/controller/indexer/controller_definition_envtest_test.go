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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func cardigannYAML(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "data", "cardigann", name))
	require.NoError(t, err)
	return string(raw)
}

// createDefinition makes a cluster-scoped IndexerDefinition. Names are
// cluster-wide, so every test passes its own.
func createDefinition(t *testing.T, c client.Client, name, fixture string) {
	t.Helper()
	d := &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: cardigannYAML(t, fixture)},
	}
	require.NoError(t, c.Create(context.Background(), d))
	t.Cleanup(func() { _ = c.Delete(context.Background(), d) })
}

// loginTracker serves login-form.yml's tracker: the login page with its CSRF
// token, and a submit that sets a session cookie only for the right
// credentials. onSubmit, when set, runs INSIDE the submit -- while the
// reconciler is mid-login -- which is how a test interleaves a real second
// writer into the read-slow-apply window.
type loginTracker struct {
	srv      *httptest.Server
	submits  atomic.Int32
	onSubmit func()
}

func newLoginTracker(t *testing.T) *loginTracker {
	t.Helper()
	lt := &loginTracker{}
	page := cardigannYAML(t, "login-form.html")
	lt.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// login-form.yml's login.test, which Login runs after every
			// login method: the logout link shows to a live session only.
			if r.URL.Path == "/dashboard" {
				if ck, err := r.Cookie("uid"); err == nil && ck.Value == "sess-7f3a" {
					_, _ = io.WriteString(w, `<html><body><a class="logout" href="/logout">logout</a></body></html>`)
					return
				}
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, page)
		case http.MethodPost:
			lt.submits.Add(1)
			if lt.onSubmit != nil {
				lt.onSubmit()
			}
			_ = r.ParseForm()
			if r.PostForm.Get("csrf_token") != "tok-abc123" ||
				r.PostForm.Get("username") != "alice" || r.PostForm.Get("password") != "hunter22" {
				_, _ = io.WriteString(w, `<html><body><div class="error">Invalid username or password</div></body></html>`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "uid", Value: "sess-7f3a"})
			_, _ = io.WriteString(w, `<html><body><a class="logout" href="/logout">logout</a></body></html>`)
		}
	}))
	t.Cleanup(lt.srv.Close)
	return lt
}

func loginIndexer(t *testing.T, c client.Client, ns, name, defName, baseURL, password string) types.NamespacedName {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-creds", Namespace: ns},
		Data:       map[string][]byte{"username": []byte("alice"), "password": []byte(password)},
	}))
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:       baseURL,
			DefinitionRef: ptr.To(defName),
			SecretRef:     &corev1.LocalObjectReference{Name: name + "-creds"},
		},
	}))
	return types.NamespacedName{Namespace: ns, Name: name}
}

// A form-login definition logs in, persists the session to BOTH the
// clustarr-indexer-sessions KV bucket and the owned Secret, and resolves
// protocol, privacy and caps from the definition rather than a probe.
func TestADefinitionBackedIndexerLogsInAndPersistsItsSession(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-cardigann-login")
	createDefinition(t, c, "synthetic-form-login-ok", "login-form.yml")
	tracker := newLoginTracker(t)
	name := loginIndexer(t, c, ns, "formy", "synthetic-form-login-ok", tracker.srv.URL, "hunter22")

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	got := mustGet(t, c, name)
	require.Equal(t, metav1.ConditionTrue, conditionOf(t, got, indexv1alpha1.IndexerConditionReady).Status,
		conditionOf(t, got, indexv1alpha1.IndexerConditionReady).Message)
	require.Equal(t, metav1.ConditionTrue, conditionOf(t, got, indexv1alpha1.IndexerConditionAuthenticated).Status)
	require.Equal(t, "torrent", string(got.Status.Protocol))
	require.Equal(t, indexer.PrivacyPrivate, got.Status.Privacy)
	require.NotNil(t, got.Status.Caps)
	require.Equal(t, map[string][]string{"search": {"q"}}, got.Status.Caps.Modes)
	require.Equal(t, int32(2000), got.Status.Caps.Categories[0].ID)
	require.Equal(t, "formy-session", got.Status.SessionSecretRef)

	// The owned Secret: controller-owned by THIS Indexer, carrying the
	// session and the cookie header the generic fetcher reads.
	var sec corev1.Secret
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "formy-session"}, &sec))
	require.Len(t, sec.OwnerReferences, 1)
	require.Equal(t, got.UID, sec.OwnerReferences[0].UID)
	require.True(t, ptr.Deref(sec.OwnerReferences[0].Controller, false))
	require.Equal(t, "uid=sess-7f3a", string(sec.Data[indexer.SessionSecretKeyCookie]))
	sess, err := cardigann.UnmarshalSession(sec.Data[indexer.SessionSecretKeySession])
	require.NoError(t, err)
	require.Equal(t, "uid=sess-7f3a", sess.CookieHeader())
	require.False(t, sess.Expired(time.Now()))
	managers := map[string]bool{}
	for _, mf := range sec.ManagedFields {
		managers[mf.Manager] = true
	}
	require.True(t, managers[string(k8s.ManagerIndexarr)], "the session Secret is not under the indexarr manager: %v", managers)

	// The KV mirror, under the UID key.
	entry, err := r.Bus.KV(events.BucketIndexerSessions).Get(ctx, indexer.SessionKey(got.UID))
	require.NoError(t, err)
	fromKV, err := cardigann.UnmarshalSession(entry.Value)
	require.NoError(t, err)
	require.Equal(t, "uid=sess-7f3a", fromKV.CookieHeader())

	// A valid session is reused, not renewed every tick.
	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)
	require.Equal(t, int32(1), tracker.submits.Load(), "the reconciler logged in again with a valid session")
}

func TestARejectedLoginIsCredentialsRejectedAndWritesNoSession(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-cardigann-badlogin")
	createDefinition(t, c, "synthetic-form-login-bad", "login-form.yml")
	tracker := newLoginTracker(t)
	name := loginIndexer(t, c, ns, "formy", "synthetic-form-login-bad", tracker.srv.URL, "wrong")

	r, rec := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	got := mustGet(t, c, name)
	auth := conditionOf(t, got, indexv1alpha1.IndexerConditionAuthenticated)
	require.Equal(t, metav1.ConditionFalse, auth.Status)
	require.Equal(t, indexer.ReasonCredentialsRejected, auth.Reason)
	require.Contains(t, auth.Message, "Invalid username or password")
	require.Equal(t, metav1.ConditionFalse, conditionOf(t, got, indexv1alpha1.IndexerConditionReady).Status)
	require.NotEmpty(t, rec.Events)

	err = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "formy-session"}, &corev1.Secret{})
	require.True(t, apierrors.IsNotFound(err), "a rejected login wrote a session Secret")
}

// The read-slow-apply rule. The login is seconds of network I/O, and a
// search fan-out can put the indexer into backoff meanwhile -- here a REAL
// second writer does exactly that, under the worker's manager, from inside
// the tracker's login handler. Deriving Healthy from the pre-login snapshot
// would report Healthy=True over an indexer the fan-out just disabled.
func TestConditionsAfterALoginAreDerivedFromAFreshRead(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-cardigann-fresh")
	createDefinition(t, c, "synthetic-form-login-fresh", "login-form.yml")
	tracker := newLoginTracker(t)
	name := loginIndexer(t, c, ns, "formy", "synthetic-form-login-fresh", tracker.srv.URL, "hunter22")

	until := metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))
	tracker.onSubmit = func() {
		live := mustGet(t, c, name)
		require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarrWorker, &live,
			func(ac *indexac.IndexerStatusApplyConfiguration) {
				ac.WithEscalationLevel(2).WithDisabledUntil(until).WithLastFailure("search.error: rate limited")
			}))
	}

	r, _ := newReconciler(t, c)
	res, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	got := mustGet(t, c, name)
	healthy := conditionOf(t, got, indexv1alpha1.IndexerConditionHealthy)
	require.Equal(t, metav1.ConditionFalse, healthy.Status,
		"Healthy was derived from the snapshot read before the login")
	require.Equal(t, indexer.ReasonBackingOff, healthy.Reason)
	require.NotNil(t, got.Status.DisabledUntil)
	require.Greater(t, res.RequeueAfter, 50*time.Minute, "the requeue ignored the backoff that landed mid-login")
}

// A definition that disappears leaves what the last good one resolved to:
// status.protocol, .privacy and every leaf of .caps are re-sent by the
// reconciler's complete declaration, not released on the transient path.
func TestAVanishedDefinitionReleasesNothing(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-cardigann-vanish")

	d := &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic-search-error-vanish"},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: cardigannYAML(t, "search-error.yml")},
	}
	require.NoError(t, c.Create(ctx, d))
	name := types.NamespacedName{Namespace: ns, Name: "semi"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "https://semi.invalid", DefinitionRef: ptr.To(d.Name),
		},
	}))

	r, _ := newReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	before := mustGet(t, c, name)
	// A public-login-free definition is Ready without any network at all.
	require.Equal(t, metav1.ConditionTrue, conditionOf(t, before, indexv1alpha1.IndexerConditionReady).Status)
	require.Equal(t, indexer.PrivacySemiPrivate, before.Status.Privacy, "semi-private was not mapped to the CRD spelling")
	require.Equal(t, "torrent", string(before.Status.Protocol))
	require.NotNil(t, before.Status.Caps)
	require.Len(t, before.Status.Caps.Modes, 3)
	require.Len(t, before.Status.Caps.Categories, 2)

	require.NoError(t, c.Delete(ctx, d))
	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)
	after := mustGet(t, c, name)
	ready := conditionOf(t, after, indexv1alpha1.IndexerConditionReady)
	require.Equal(t, metav1.ConditionFalse, ready.Status)
	require.Equal(t, indexer.ReasonDefinitionNotFound, ready.Reason)
	require.Equal(t, before.Status.Protocol, after.Status.Protocol, "protocol was released")
	require.Equal(t, before.Status.Privacy, after.Status.Privacy, "privacy was released")
	require.Equal(t, before.Status.Caps.Modes, after.Status.Caps.Modes, "caps.modes was released")
	require.Equal(t, before.Status.Caps.Categories, after.Status.Caps.Categories, "caps.categories was released")
	require.Equal(t, before.Status.Caps.SupportsRawSearch, after.Status.Caps.SupportsRawSearch)
	require.Equal(t, before.Status.SessionSecretRef, after.Status.SessionSecretRef)
}
