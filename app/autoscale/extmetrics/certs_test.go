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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
)

func parseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(certPEM)
	require.NotNil(t, b)
	c, err := x509.ParseCertificate(b.Bytes)
	require.NoError(t, err)
	return c
}

func TestNewCAIsAP256CAForTenYears(t *testing.T) {
	now := time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)
	ca := newTestCA(t, now)
	assert.True(t, ca.cert.IsCA)
	assert.NotZero(t, ca.cert.KeyUsage&x509.KeyUsageCertSign)
	assert.Equal(t, elliptic.P256(), ca.key.Curve)
	assert.Equal(t, now.Add(extmetrics.CAValidity).Unix(), ca.cert.NotAfter.Unix())
	assert.Equal(t, 10*365*24*time.Hour, extmetrics.CAValidity)
}

func TestIssueServingNamesTheServiceFourWaysForOneYear(t *testing.T) {
	now := time.Now()
	ca := newTestCA(t, now)
	certPEM, keyPEM, err := extmetrics.IssueServing(ca.certPEM, ca.keyPEM, "external-metrics", ns, now)
	require.NoError(t, err)
	leaf := parseCert(t, certPEM)
	want := []string{"external-metrics", "external-metrics.clustarr-system", "external-metrics.clustarr-system.svc",
		"external-metrics.clustarr-system.svc.cluster.local"}
	assert.Equal(t, want, leaf.DNSNames)
	assert.Equal(t, want, extmetrics.SANs("external-metrics", ns))
	assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, leaf.ExtKeyUsage)
	assert.Equal(t, now.Add(extmetrics.ServingValidity).Unix(), leaf.NotAfter.Unix())
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.certPEM)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "external-metrics.clustarr-system.svc"})
	require.NoError(t, err)
	kb, _ := pem.Decode(keyPEM)
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	require.NoError(t, err)
	assert.True(t, key.PublicKey.Equal(leaf.PublicKey.(*ecdsa.PublicKey)))
}

func TestRenewalReason(t *testing.T) {
	now := time.Now()
	ca := newTestCA(t, now)
	other := newTestCA(t, now)
	fresh, _, err := extmetrics.IssueServing(ca.certPEM, ca.keyPEM, "external-metrics", ns, now)
	require.NoError(t, err)
	foreign, _, err := extmetrics.IssueServing(other.certPEM, other.keyPEM, "external-metrics", ns, now)
	require.NoError(t, err)

	assert.Empty(t, extmetrics.RenewalReason(ca.certPEM, fresh, "external-metrics", ns, now))
	assert.NotEmpty(t, extmetrics.RenewalReason(ca.certPEM, fresh, "external-metrics", ns,
		now.Add(extmetrics.ServingValidity-29*24*time.Hour)), "under 30 days left")
	assert.Empty(t, extmetrics.RenewalReason(ca.certPEM, fresh, "external-metrics", ns,
		now.Add(extmetrics.ServingValidity-31*24*time.Hour)), "31 days left is fine")
	assert.NotEmpty(t, extmetrics.RenewalReason(ca.certPEM, fresh, "clustarr-external-metrics", ns, now), "the Service was renamed")
	assert.NotEmpty(t, extmetrics.RenewalReason(ca.certPEM, foreign, "external-metrics", ns, now), "signed by another CA")
	assert.NotEmpty(t, extmetrics.RenewalReason(ca.certPEM, []byte("garbage"), "external-metrics", ns, now))
}

func TestCANeedsRenewal(t *testing.T) {
	now := time.Now()
	ca := newTestCA(t, now)
	assert.False(t, extmetrics.CANeedsRenewal(ca.certPEM, ca.keyPEM, now))
	assert.True(t, extmetrics.CANeedsRenewal(ca.certPEM, ca.keyPEM, now.Add(extmetrics.CAValidity-29*24*time.Hour)))
	assert.True(t, extmetrics.CANeedsRenewal(nil, nil, now))
	assert.True(t, extmetrics.CANeedsRenewal(ca.certPEM, newTestCA(t, now).keyPEM, now), "a key that is not the CA's")
}

func TestNewSecretDataHoldsFourKeys(t *testing.T) {
	now := time.Now()
	data, err := extmetrics.NewSecretData("external-metrics", ns, now)
	require.NoError(t, err)
	for _, k := range []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey, extmetrics.KeyCACert, extmetrics.KeyCAKey} {
		assert.NotEmpty(t, data[k], k)
	}
	assert.Empty(t, extmetrics.RenewalReason(data[extmetrics.KeyCACert], data[corev1.TLSCertKey], "external-metrics", ns, now))
}
