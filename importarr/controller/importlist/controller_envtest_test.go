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

package importlist_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	importlist "github.com/mediactl/clustarr/importarr/controller/importlist"
	workerimportlist "github.com/mediactl/clustarr/importarr/worker/importlist"
)

func TestReconcileDisabledListSkipsScheduling(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-disabled")
	bus := newBus(t, ctx)

	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "watchlist"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:    []string{"movie"},
			Enabled:  ptr.To(false),
			StevenLu: &catalogv1alpha1.StevenLu{},
			Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "hd", RootFolderRef: "movies"},
		},
	}
	require.NoError(t, c.Create(ctx, il))

	r := &importlist.Reconciler{Client: c, Bus: bus}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: il.Name}})
	require.NoError(t, err)
	require.Positive(t, res.RequeueAfter)

	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: il.Name}, &got))
	ready := findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionReady)
	require.NotNil(t, ready)
	require.Equal(t, metav1.ConditionFalse, ready.Status)
	require.Equal(t, k8sReasonDisabled, ready.Reason)
	require.Equal(t, "importarr", managerFor(got.ManagedFields))
}

func TestReconcileSchedulesASyncAndReportsUnknownUntilAChekpointArrives(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-schedule")
	bus := newBus(t, ctx)

	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "watchlist"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:    []string{"movie"},
			StevenLu: &catalogv1alpha1.StevenLu{},
			Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "hd", RootFolderRef: "movies"},
		},
	}
	require.NoError(t, c.Create(ctx, il))

	r := &importlist.Reconciler{Client: c, Bus: bus}
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: il.Name}})
	require.NoError(t, err)

	task := recvListTask(t, ctx, bus, 5*time.Second)
	require.Equal(t, il.Name, task.ListRef.Name)
	require.Equal(t, ns, task.ListRef.Namespace)

	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: il.Name}, &got))
	require.NotNil(t, got.Status.NextSyncAt, "a due sync must set nextSyncAt")
	synced := findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionSynced)
	require.NotNil(t, synced)
	require.Equal(t, metav1.ConditionUnknown, synced.Status, "no worker checkpoint has arrived yet")
	require.Equal(t, "importarr", managerFor(got.ManagedFields))
}

func TestReconcileTraktDeviceFlowSurfacesTheUserCodeThenAuthorizesAndSchedules(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-trakt")
	bus := newBus(t, ctx)

	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/oauth/device/code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "devicecode123", "user_code": "ABCD-1234",
				"verification_url": "https://trakt.tv/activate", "expires_in": 600, "interval": 1,
			})
		case "/oauth/device/token":
			polls++
			if polls < 2 {
				w.WriteHeader(http.StatusBadRequest) // pending
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-abc", "refresh_token": "refresh-abc",
				"expires_in": 7776000, "created_at": time.Now().Unix(),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "trakt-creds"},
		Data:       map[string][]byte{"clientID": []byte("cid"), "clientSecret": []byte("csecret")},
	}
	require.NoError(t, c.Create(ctx, sec))

	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "watchlist"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:     []string{"movie"},
			SecretRef: &corev1.LocalObjectReference{Name: "trakt-creds"},
			Trakt:     &catalogv1alpha1.TraktList{ListType: catalogv1alpha1.TraktListTypeWatchlist, Username: "me"},
			Defaults:  catalogv1alpha1.ListDefaults{QualityProfileRef: "hd", RootFolderRef: "movies"},
		},
	}
	require.NoError(t, c.Create(ctx, il))

	r := &importlist.Reconciler{Client: c, Bus: bus, TraktBaseURL: srv.URL}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: il.Name}}

	// First reconcile: mints the device code, the user has not approved it
	// yet. The UI (task G3-4) reads exactly these two fields.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: il.Name}, &got))
	require.NotNil(t, got.Status.Auth)
	require.Equal(t, catalogv1alpha1.DeviceAuthStatePending, got.Status.Auth.State)
	require.Equal(t, "ABCD-1234", got.Status.Auth.UserCode)
	require.Equal(t, "https://trakt.tv/activate", got.Status.Auth.VerificationURL)

	// Second reconcile: the fake server still says pending (polls==1).
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: il.Name}, &got))
	require.Equal(t, catalogv1alpha1.DeviceAuthStatePending, got.Status.Auth.State)
	require.Equal(t, "ABCD-1234", got.Status.Auth.UserCode,
		"the user code must survive a pending poll, not be blanked by a partial status apply")

	// Third reconcile: the fake server now authorizes (polls==2), so this
	// same reconcile also schedules the first sync.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: il.Name}, &got))
	require.Equal(t, catalogv1alpha1.DeviceAuthStateAuthorized, got.Status.Auth.State)
	authenticated := findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionAuthenticated)
	require.NotNil(t, authenticated)
	require.Equal(t, metav1.ConditionTrue, authenticated.Status)

	task := recvListTask(t, ctx, bus, 5*time.Second)
	require.Equal(t, il.Name, task.ListRef.Name)

	// The token Secret is owned by, and named after, the ImportList.
	var tokenSecret corev1.Secret
	require.NoError(t, c.Get(ctx, types.NamespacedName{
		Namespace: ns, Name: workerimportlist.TraktTokenSecretName(il.Name),
	}, &tokenSecret))
	require.Equal(t, "access-abc", string(tokenSecret.Data["accessToken"]))
	require.Len(t, tokenSecret.OwnerReferences, 1)
	require.Equal(t, il.Name, tokenSecret.OwnerReferences[0].Name)
	require.Empty(t, tokenSecret.Data["deviceCode"], "the spent device code must be cleared once authorized")
}

const k8sReasonDisabled = "Disabled"

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
