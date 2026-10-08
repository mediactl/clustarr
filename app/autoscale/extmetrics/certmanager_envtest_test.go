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

package extmetrics_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func readAPIService(t *testing.T, c client.Client) *unstructured.Unstructured {
	t.Helper()
	u := apiServiceKey()
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(u), u))
	return u
}

// The installers own the APIService; the manager owns spec.caBundle and
// nothing else (R5 amendment, OD28). A foreign spec.service (KEDA,
// prometheus-adapter) gets no injection and a Warning.
func TestCAInjectorOwnsOnlyCABundle(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	namespace := createNamespace(t, c, "extmetrics-ca")
	rec := k8sevents.NewFakeRecorder(10)
	m := &extmetrics.CertManager{Client: c, Reader: c, Recorder: rec, Namespace: namespace,
		ServiceName: "external-metrics", SecretName: "external-metrics-tls"}

	t.Run("an installer-shaped APIService", func(t *testing.T) {
		require.NoError(t, c.Create(ctx, installerAPIService(namespace, "external-metrics"), client.FieldOwner("helm")))
		t.Cleanup(func() { _ = c.Delete(ctx, apiServiceKey()) })
		require.NoError(t, m.Sync(ctx))

		var sec corev1.Secret
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "external-metrics-tls"}, &sec))
		got := readAPIService(t, c)
		bundle, _, _ := unstructured.NestedString(got.Object, "spec", "caBundle")
		assert.Equal(t, base64.StdEncoding.EncodeToString(sec.Data[extmetrics.KeyCACert]), bundle)

		var mine []string
		for _, mf := range got.GetManagedFields() {
			if mf.Manager == string(k8s.ManagerAutoscale) {
				mine = append(mine, string(mf.FieldsV1.Raw))
			}
		}
		require.Len(t, mine, 1)
		assert.JSONEq(t, `{"f:spec":{"f:caBundle":{}}}`, mine[0], "clustarr-autoscale owns spec.caBundle and nothing else")
		prio, _, _ := unstructured.NestedInt64(got.Object, "spec", "groupPriorityMinimum")
		assert.Equal(t, int64(100), prio, "the installer's fields are untouched")

		rv := got.GetResourceVersion()
		require.NoError(t, m.Sync(ctx))
		assert.Equal(t, rv, readAPIService(t, c).GetResourceVersion(), "an injected bundle is not re-applied")
	})

	t.Run("a foreign APIService", func(t *testing.T) {
		require.NoError(t, c.Create(ctx, installerAPIService("keda", "keda-operator-metrics-apiserver"), client.FieldOwner("helm")))
		t.Cleanup(func() { _ = c.Delete(ctx, apiServiceKey()) })
		require.NoError(t, m.Sync(ctx))
		bundle, found, _ := unstructured.NestedString(readAPIService(t, c).Object, "spec", "caBundle")
		assert.False(t, found && bundle != "", "no caBundle is injected into another provider's APIService")
		select {
		case e := <-rec.Events:
			assert.True(t, strings.HasPrefix(e, "Warning "+extmetrics.ReasonAPIServiceForeign+" "), e)
		case <-time.After(5 * time.Second):
			t.Fatal("no ExternalMetricsAPIServiceForeign Warning")
		}
	})

	t.Run("no APIService at all", func(t *testing.T) {
		require.NoError(t, m.Sync(ctx), "autoscaling.enabled=false renders none; nothing to inject")
	})
}
