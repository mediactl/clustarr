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
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// neverSynced is a cache whose sync never completes, as a 15,630-Episode
// list on a stalled apiserver can take the 10-minute CacheSyncTimeout.
type neverSynced struct{ cache.Cache }

func (neverSynced) WaitForCacheSync(ctx context.Context) bool { <-ctx.Done(); return false }

// §9.4: the Server is a *manager.Server, which controller-runtime starts in
// its HTTP-servers group before the caches, so a manager whose cache cannot
// sync still serves the API (and nothing in the Others group has started).
func TestTheServerAnswersBeforeTheCachesSync(t *testing.T) {
	cfg, c := startEnv(t)
	ctx := context.Background()
	namespace := createNamespace(t, c, "extmetrics-early")
	data, err := extmetrics.NewSecretData("external-metrics", namespace, time.Now())
	require.NoError(t, err)
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "external-metrics-tls"},
		Type:       corev1.SecretTypeTLS, Data: data,
	}))
	proxy := newTestCA(t, time.Now())
	require.NoError(t, c.Create(ctx, authConfigMap(namespace, "front-proxy", proxy.certPEM, `[]`)))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		NewCache: func(cfg *rest.Config, o cache.Options) (cache.Cache, error) {
			inner, err := cache.New(cfg, o)
			return neverSynced{Cache: inner}, err
		},
	})
	require.NoError(t, err)
	var othersStarted atomic.Bool
	require.NoError(t, mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		othersStarted.Store(true)
		<-ctx.Done()
		return nil
	})))
	series, err := extmetrics.ServableSeries(agentdomain.Domains(), events.Default())
	require.NoError(t, err)
	srv, err := extmetrics.NewServer(extmetrics.ServerOptions{
		Namespace: namespace, BindAddress: "127.0.0.1:0", SecretName: "external-metrics-tls",
		AuthConfigMap: types.NamespacedName{Namespace: namespace, Name: "front-proxy"}, Series: series,
	}, mgr.GetAPIReader(), &fakeStates{def: 3, at: time.Now()})
	require.NoError(t, err)
	r, err := srv.Runnable()
	require.NoError(t, err)
	require.NoError(t, mgr.Add(r))

	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(runCtx) }()

	cert := proxy.clientCert(t, "front-proxy-client")
	require.Eventually(t, func() bool {
		code, _, err := getTLS(srv.Addr(), data[extmetrics.KeyCACert], "external-metrics."+namespace+".svc", &cert, hpaUser, extmetrics.APIPrefix)
		return err == nil && code == http.StatusOK
	}, 20*time.Second, 200*time.Millisecond)
	assert.False(t, othersStarted.Load(), "the caches never synced, so no other runnable may have started")
}
