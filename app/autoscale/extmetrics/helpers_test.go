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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/events"
)

// ns is the manager's namespace in the unit tests.
const ns = "clustarr-system"

// testCA is a CA a test controls: the serving CA the manager makes, or a
// front-proxy CA standing in for the kube-apiserver's.
type testCA struct {
	certPEM, keyPEM []byte
	cert            *x509.Certificate
	key             *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, now time.Time) testCA {
	t.Helper()
	certPEM, keyPEM, err := extmetrics.NewCA(now)
	require.NoError(t, err)
	return parseCA(t, certPEM, keyPEM)
}

func parseCA(t *testing.T, certPEM, keyPEM []byte) testCA {
	t.Helper()
	cb, _ := pem.Decode(certPEM)
	require.NotNil(t, cb)
	cert, err := x509.ParseCertificate(cb.Bytes)
	require.NoError(t, err)
	kb, _ := pem.Decode(keyPEM)
	require.NotNil(t, kb)
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	require.NoError(t, err)
	return testCA{certPEM: certPEM, keyPEM: keyPEM, cert: cert, key: key}
}

// clientCert issues a front-proxy client certificate for cn, as the
// kube-apiserver's --proxy-client-cert-file is.
func (ca testCA) clientCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// getTLS makes one GET over a fresh connection. A client certificate is
// sent whatever CAs the server names (GetClientCertificate), so a foreign
// one reaches the server's verification instead of being withheld.
func getTLS(addr string, servingCA []byte, serverName string, cert *tls.Certificate, user, path string) (int, []byte, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(servingCA) {
		return 0, nil, fmt.Errorf("no serving CA")
	}
	cfg := &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}
	if cert != nil {
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
	}
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if user != "" {
		req.Header.Set("X-Remote-User", user)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// fakeStates answers ConsumerState from a table.
type fakeStates struct {
	mu    sync.Mutex
	lag   map[string]uint64 // by durable; missing means def
	def   uint64
	err   map[string]error
	at    time.Time
	calls atomic.Int64
}

func (f *fakeStates) ConsumerState(_ context.Context, _, durable string) (events.ConsumerState, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err[durable]; err != nil {
		return events.ConsumerState{}, err
	}
	lag, ok := f.lag[durable]
	if !ok {
		lag = f.def
	}
	return events.ConsumerState{Pending: lag, ObservedAt: f.at}, nil
}

// authConfigMap is extension-apiserver-authentication as a kube-apiserver
// with --requestheader-* flags publishes it.
func authConfigMap(namespace, name string, caPEM []byte, allowedNames string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data: map[string]string{
			"requestheader-client-ca-file":       string(caPEM),
			"requestheader-allowed-names":        allowedNames,
			"requestheader-username-headers":     `["X-Remote-User"]`,
			"requestheader-group-headers":        `["X-Remote-Group"]`,
			"requestheader-extra-headers-prefix": `["X-Remote-Extra-"]`,
		},
	}
}
