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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/app/indexer/bundle"
	"github.com/mediactl/clustarr/internal/cli/manager"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/starttest"
)

// managerCase is the manager's options for one start case: the identity's
// kubeconfig, the embedded NATS, free probe and External Metrics ports, and
// non-default engine settings, so only a value that reached the rendered
// engine can match them.
func managerCase(t *testing.T, cfg *rest.Config, natsURL string) (o manager.Options, probe, extmetricsAddr string) {
	t.Helper()
	o = manager.DefaultOptions()
	o.Namespace = "default"
	o.NATSURL, o.BusSingleNode = natsURL, true
	o.MetricsBindAddress = k8s.DisabledBindAddress
	o.HealthProbeBindAddress = starttest.FreeAddress(t)
	o.ExternalMetricsBindAddress = starttest.FreeAddress(t)
	o.NativeImage = "ghcr.io/mediactl/clustarr/native:dev"
	o.DataClaim = "media-clustarr-data"
	o.EngineServiceAccount = "media-clustarr-grab-engine"
	o.DataDir = t.TempDir()
	o.GracefulShutdownTimeout = 10 * time.Second
	o.RESTConfig, o.SkipNameValidation = cfg, true
	return o, o.HealthProbeBindAddress, o.ExternalMetricsBindAddress
}

// start runs manager.Run until the test ends, then cancels its context --
// what SIGTERM does through cli.Main's signal context -- and requires a clean
// return within 30 s.
func start(t *testing.T, o manager.Options) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx, o) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			assert.NoError(t, err, "Run on a clean shutdown")
		case <-time.After(30 * time.Second):
			t.Error("Run did not return within 30s of its context ending")
		}
	})
	select {
	case err := <-done:
		t.Fatalf("Run returned before serving probes: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestManagerStartsServesProbesAndStopsOnSignal runs the real manager.Run
// under the manager identity's generated role (spec §10.3.3): once while
// another replica holds the lease -- readiness never waits on leader-only
// work, and a non-leader writes nothing the autoscaler owns -- and once as
// the leader, proving the bundle loader, the DownloadClient controller's
// engine (the agent binary, the claim and ServiceAccount flags) and the
// External Metrics API (its Secret, the APIService caBundle, front-proxy
// authentication) are wired. Each case stops by its context, as on SIGTERM.
func TestManagerStartsServesProbesAndStopsOnSignal(t *testing.T) {
	env := starttest.StartEnv(t)
	natsURL := starttest.EmbeddedNATS(t)
	kcs := starttest.IdentityKubeconfigs(t, env, []string{"manager"})
	cfg := starttest.RESTConfig(t, kcs["manager"])
	admin, err := client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	createInstallerAPIService(t, admin)

	t.Run("non-leader", func(t *testing.T) {
		starttest.HoldLease(t, env.Config, "default", manager.LeaderElectionID)
		o, probe, extAddr := managerCase(t, cfg, natsURL)
		start(t, o)
		starttest.WaitForProbe(t, "http://"+probe+"/healthz")
		starttest.WaitForProbe(t, "http://"+probe+"/readyz")
		_, err := tls.Dial("tcp", extAddr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // only the handshake is observed
		require.Error(t, err, "no serving Secret yet: the External Metrics server must refuse the handshake")
		assertNoAutoscaleWrites(t, admin)
	})

	t.Run("leader", func(t *testing.T) {
		releaseLease(t, admin, "default", manager.LeaderElectionID)
		o, probe, extAddr := managerCase(t, cfg, natsURL)
		start(t, o)
		starttest.WaitForProbe(t, "http://"+probe+"/readyz")
		verifyBundle(t, admin)
		verifyGrabController(t, admin, natsURL)
		verifyExternalMetrics(t, admin, extAddr)
	})
}

// Validation runs before anything touches the cluster: no kubeconfig, no NATS.
func TestManagerFailsFastOnBadOptions(t *testing.T) {
	ctx := context.Background()

	o := manager.DefaultOptions()
	o.NativeImage = "ghcr.io/mediactl/clustarr/native:test"
	o.NATSURL = ""
	require.ErrorContains(t, manager.Run(ctx, o), "--nats-url", "a missing --nats-url reached the manager")

	o = manager.DefaultOptions()
	o.NativeImage = "ghcr.io/mediactl/clustarr/native:test"
	o.NATSURL = "nats://127.0.0.1:1"
	o.Namespace, o.LeaderElectionNamespace = "", ""
	require.ErrorContains(t, manager.Run(ctx, o), "leader election needs a namespace",
		"leader election with nowhere to put the Lease reached the manager")

	o = manager.DefaultOptions()
	o.Namespace = "default"
	o.NATSURL = "nats://127.0.0.1:1"
	require.ErrorContains(t, manager.Run(ctx, o), "--native-image", "a manager with no image to stamp reached the cluster")
}

// createInstallerAPIService is what both installers render: the APIService
// for external.metrics.k8s.io pointing at the manager's Service, with no
// caBundle -- the manager's CA injector fills it.
func createInstallerAPIService(t *testing.T, admin client.Client) {
	t.Helper()
	svc := &unstructured.Unstructured{}
	svc.SetGroupVersionKind(extmetrics.APIServiceGVK)
	svc.SetName(extmetrics.APIServiceName)
	require.NoError(t, unstructured.SetNestedMap(svc.Object, map[string]any{
		"group": "external.metrics.k8s.io", "version": "v1beta1",
		"groupPriorityMinimum": int64(100), "versionPriority": int64(100),
		"service": map[string]any{"namespace": "default", "name": "external-metrics", "port": int64(443)},
	}, "spec"))
	require.NoError(t, admin.Create(context.Background(), svc))
	t.Cleanup(func() { _ = admin.Delete(context.Background(), svc) })
}

// assertNoAutoscaleWrites: a non-leader creates no serving Secret, injects
// no caBundle and renders no HPA -- CertManager, the CA injector and the HPA
// reconciler are the leader's.
func assertNoAutoscaleWrites(t *testing.T, admin client.Client) {
	t.Helper()
	ctx := context.Background()
	// Long enough for a leader's first CertManager pass, which runs at start.
	time.Sleep(3 * time.Second)
	var s corev1.Secret
	err := admin.Get(ctx, client.ObjectKey{Namespace: "default", Name: "external-metrics-tls"}, &s)
	assert.True(t, apierrors.IsNotFound(err), "a non-leader created the serving Secret: %v", err)
	svc := &unstructured.Unstructured{}
	svc.SetGroupVersionKind(extmetrics.APIServiceGVK)
	require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: extmetrics.APIServiceName}, svc))
	ca, _, _ := unstructured.NestedString(svc.Object, "spec", "caBundle")
	assert.Empty(t, ca, "a non-leader injected the APIService caBundle")
	var hpas autoscalingv2.HorizontalPodAutoscalerList
	require.NoError(t, admin.List(ctx, &hpas, client.InNamespace("default")))
	assert.Empty(t, hpas.Items, "a non-leader rendered an HPA")
}

// releaseLease deletes the Lease starttest.HoldLease created, so the next
// manager takes it at its first try.
func releaseLease(t *testing.T, admin client.Client, namespace, id string) {
	t.Helper()
	err := admin.Delete(context.Background(), &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: namespace}})
	require.NoError(t, client.IgnoreNotFound(err))
}

// verifyBundle: the leader's bundle loader applies the embedded Cardigann
// corpus as IndexerDefinitions, and the IndexerDefinition controller parses
// one.
func verifyBundle(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	starttest.WaitForLong(t, "the bundle loader's IndexerDefinition 1337x, parsed", func() bool {
		var d indexv1alpha1.IndexerDefinition
		return c.Get(ctx, client.ObjectKey{Name: "1337x"}, &d) == nil &&
			d.Labels[bundle.LabelBundled] == "true" && d.Status.ID == "1337x"
	})
}

// verifyGrabController: a DownloadClient becomes an engine StatefulSet that
// runs the agent binary with one --domain (no shell), mounts --data-claim,
// runs as --engine-service-account and gets its namespace and the bus.
func verifyGrabController(t *testing.T, c client.Client, natsURL string) {
	t.Helper()
	ctx := context.Background()
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-probe", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Torrent:  &downloadv1alpha1.TorrentSpec{EnableDHT: ptr.To(false)},
		},
	}
	require.NoError(t, c.Create(ctx, dc))
	t.Cleanup(func() { _ = c.Delete(context.Background(), dc) })
	var sts appsv1.StatefulSet
	starttest.WaitForLong(t, "the DownloadClient controller to create claim-probe-engine", func() bool {
		return c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "claim-probe-engine"}, &sts) == nil
	})
	pod := sts.Spec.Template.Spec
	require.NotEmpty(t, pod.Containers)
	ct := pod.Containers[0]
	assert.Equal(t, []string{binpath.Agent}, ct.Command, "engines run the agent binary, not a shell")
	require.NotEmpty(t, ct.Args)
	assert.Equal(t, "--domain=torrent-engine", ct.Args[0])
	assert.Equal(t, "ghcr.io/mediactl/clustarr/native:dev", ct.Image, "--native-image reaches the engine")
	var claim string
	for _, v := range pod.Volumes {
		if v.PersistentVolumeClaim != nil {
			claim = v.PersistentVolumeClaim.ClaimName
		}
	}
	assert.Equal(t, "media-clustarr-data", claim, "--data-claim reaches the engine")
	assert.Equal(t, "media-clustarr-grab-engine", pod.ServiceAccountName, "--engine-service-account reaches the engine")
	envs := map[string]corev1.EnvVar{}
	for _, e := range ct.Env {
		envs[e.Name] = e
	}
	if e, ok := envs["POD_NAMESPACE"]; assert.True(t, ok, "POD_NAMESPACE") {
		assert.NotNil(t, e.ValueFrom, "POD_NAMESPACE comes from the downward API")
	}
	assert.Equal(t, natsURL, envs["NATS_URL"].Value, "the engine dials the manager's own bus")
}

// verifyExternalMetrics (§10.3.3's leader bullet): the leader's CertManager
// creates the serving Secret, the CA injector writes its ca.crt into the
// APIService, and the server then completes a handshake and answers a
// front-proxied request -- a client certificate the front-proxy CA signed,
// with an allowed CN and a user header -- with 200, refusing one with no
// certificate.
func verifyExternalMetrics(t *testing.T, c client.Client, addr string) {
	t.Helper()
	ctx := context.Background()
	var secret corev1.Secret
	starttest.WaitForLong(t, "the serving Secret default/external-metrics-tls", func() bool {
		return c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "external-metrics-tls"}, &secret) == nil
	})
	require.NotEmpty(t, secret.Data[extmetrics.KeyCACert])
	starttest.WaitForLong(t, "the APIService caBundle to equal the Secret's ca.crt", func() bool {
		svc := &unstructured.Unstructured{}
		svc.SetGroupVersionKind(extmetrics.APIServiceGVK)
		if c.Get(ctx, client.ObjectKey{Name: extmetrics.APIServiceName}, svc) != nil {
			return false
		}
		got, _, _ := unstructured.NestedString(svc.Object, "spec", "caBundle")
		raw, _ := json.Marshal(secret.Data[extmetrics.KeyCACert]) // []byte marshals as base64, as caBundle is stored
		return `"`+got+`"` == string(raw)
	})

	proxyCert := frontProxyClient(t, c)
	servingCAs := x509.NewCertPool()
	require.True(t, servingCAs.AppendCertsFromPEM(secret.Data[extmetrics.KeyCACert]))
	get := func(certs []tls.Certificate) (int, error) {
		hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: servingCAs, ServerName: "external-metrics.default.svc", Certificates: certs,
		}}}
		req, err := http.NewRequest(http.MethodGet, "https://"+addr+extmetrics.APIPrefix, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("X-Remote-User", "system:kube-controller-manager")
		resp, err := hc.Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	starttest.WaitForLong(t, "a front-proxied discovery request answered 200", func() bool {
		code, err := get([]tls.Certificate{proxyCert})
		return err == nil && code == http.StatusOK
	})
	code, err := get(nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, code, "a request without a front-proxy certificate must be refused")
}

// frontProxyClient makes kube-system/extension-apiserver-authentication name
// a front-proxy CA this test holds, with allowed name front-proxy-client,
// and returns a client certificate that CA signed for that name.
func frontProxyClient(t *testing.T, c client.Client) tls.Certificate {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "start-envtest-front-proxy-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "front-proxy-client"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &key.PublicKey, caKey)
	require.NoError(t, err)

	var cm corev1.ConfigMap
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: extmetrics.AuthConfigMapName}, &cm))
	base := cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["requestheader-client-ca-file"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	cm.Data["requestheader-allowed-names"] = `["front-proxy-client"]`
	cm.Data["requestheader-username-headers"] = `["X-Remote-User"]`
	cm.Data["requestheader-group-headers"] = `["X-Remote-Group"]`
	cm.Data["requestheader-extra-headers-prefix"] = `["X-Remote-Extra-"]`
	require.NoError(t, c.Patch(ctx, &cm, client.MergeFrom(base)))

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	return pair
}
