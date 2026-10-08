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
	"crypto/tls"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const hpaUser = "system:serviceaccount:kube-system:horizontal-pod-autoscaler"

// clock is the Server's reload clock; TLS verification keeps real time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type tlsFixture struct {
	srv     *extmetrics.Server
	c       client.Client
	clock   *clock
	serving []byte // the serving CA's PEM; nil until the Secret exists
	proxy   testCA
}

func servingSecret(t *testing.T) (*corev1.Secret, []byte) {
	t.Helper()
	data, err := extmetrics.NewSecretData("external-metrics", ns, time.Now())
	require.NoError(t, err)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "external-metrics-tls"},
		Type:       corev1.SecretTypeTLS, Data: data,
	}, data[extmetrics.KeyCACert]
}

// newTLSFixture serves on loopback from a fake API reader holding the
// front-proxy ConfigMap and, withSecret, the TLS Secret.
func newTLSFixture(t *testing.T, withSecret bool) *tlsFixture {
	t.Helper()
	proxy := newTestCA(t, time.Now())
	objs := []client.Object{authConfigMap(extmetrics.AuthConfigMapNamespace, extmetrics.AuthConfigMapName, proxy.certPEM, `["front-proxy-client"]`)}
	var servingCA []byte
	if withSecret {
		sec, ca := servingSecret(t)
		objs = append(objs, sec)
		servingCA = ca
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()
	clk := &clock{t: time.Now()}
	series, err := extmetrics.ServableSeries(agentdomain.Domains(), events.Default())
	require.NoError(t, err)
	srv, err := extmetrics.NewServer(extmetrics.ServerOptions{
		Namespace: ns, BindAddress: "127.0.0.1:0", SecretName: "external-metrics-tls", Series: series, Now: clk.Now,
	}, c, &fakeStates{def: 37, at: time.Now()})
	require.NoError(t, err)
	r, err := srv.Runnable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &tlsFixture{srv: srv, c: c, clock: clk, serving: servingCA, proxy: proxy}
}

func (f *tlsFixture) get(cert *tls.Certificate, user, path string) (int, []byte, error) {
	return getTLS(f.srv.Addr(), f.serving, "external-metrics."+ns+".svc", cert, user, path)
}

// §9.9's authn table, over a real TLS handshake.
func TestTheServerAuthenticatesTheFrontProxy(t *testing.T) {
	f := newTLSFixture(t, true)
	allowed := f.proxy.clientCert(t, "front-proxy-client")
	intruder := f.proxy.clientCert(t, "intruder")
	foreign := newTestCA(t, time.Now()).clientCert(t, "front-proxy-client")
	list := "/apis/external.metrics.k8s.io/v1beta1/namespaces/" + ns + "/clustarr_consumer_lag"

	code, body, err := f.get(nil, hpaUser, list)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, code, "no client certificate")
	assert.NotContains(t, string(body), `"items"`)

	_, _, err = f.get(&foreign, hpaUser, list)
	assert.Error(t, err, "a certificate from another CA fails the handshake")

	code, body, err = f.get(&intruder, hpaUser, list)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, code, "a CN outside requestheader-allowed-names")
	assert.NotContains(t, string(body), `"37"`)

	code, _, err = f.get(&allowed, "", list)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, code, "an allowed CN without X-Remote-User")

	code, body, err = f.get(&allowed, hpaUser, list)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code, string(body))
	assert.Contains(t, string(body), `"value":"37"`)
}

// Until the TLS Secret is there the server refuses the handshake, which
// the aggregator reads as unavailable; once it appears the next handshake
// (past the 1 s retry floor) serves.
func TestTheServerRefusesHandshakesUntilItsMaterialLoads(t *testing.T) {
	f := newTLSFixture(t, false)
	sec, servingCA := servingSecret(t)
	f.serving = servingCA
	allowed := f.proxy.clientCert(t, "front-proxy-client")
	_, _, err := f.get(&allowed, hpaUser, extmetrics.APIPrefix)
	require.Error(t, err)

	require.NoError(t, f.c.Create(context.Background(), sec))
	f.clock.Add(2 * time.Second)
	code, _, err := f.get(&allowed, hpaUser, extmetrics.APIPrefix)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)
}

// The front-proxy CA is re-read once the material is older than Reload
// (60 s): a rotated CA is trusted and the old one is not.
func TestTheServerReloadsTheFrontProxyCA(t *testing.T) {
	f := newTLSFixture(t, true)
	old := f.proxy.clientCert(t, "front-proxy-client")
	code, _, err := f.get(&old, hpaUser, extmetrics.APIPrefix)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)

	rotated := newTestCA(t, time.Now())
	var cm corev1.ConfigMap
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: extmetrics.AuthConfigMapNamespace, Name: extmetrics.AuthConfigMapName}, &cm))
	cm.Data["requestheader-client-ca-file"] = string(rotated.certPEM)
	require.NoError(t, f.c.Update(context.Background(), &cm))
	fresh := rotated.clientCert(t, "front-proxy-client")

	_, _, err = f.get(&fresh, hpaUser, extmetrics.APIPrefix)
	assert.Error(t, err, "inside the reload interval the rotated CA is not trusted yet")

	f.clock.Add(extmetrics.DefaultReload + time.Second)
	code, _, err = f.get(&fresh, hpaUser, extmetrics.APIPrefix)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)
	_, _, err = f.get(&old, hpaUser, extmetrics.APIPrefix)
	assert.Error(t, err, "the old CA is no longer trusted")
}

func TestNewServerRefusesAnIncompleteConfig(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	for name, o := range map[string]extmetrics.ServerOptions{
		"no namespace":    {BindAddress: ":6443", SecretName: "s"},
		"disabled":        {Namespace: ns, BindAddress: "0", SecretName: "s"},
		"no secret":       {Namespace: ns, BindAddress: ":6443"},
		"no bind address": {Namespace: ns, SecretName: "s"},
	} {
		_, err := extmetrics.NewServer(o, c, &fakeStates{})
		assert.Error(t, err, name)
	}
}
