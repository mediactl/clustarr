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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func certManager(c client.Client, now *time.Time) *extmetrics.CertManager {
	return &extmetrics.CertManager{
		Client: c, Reader: c, Namespace: ns, ServiceName: "external-metrics", SecretName: "external-metrics-tls",
		Now: func() time.Time { return *now },
	}
}

func readSecret(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "external-metrics-tls"}, &s))
	return &s
}

func TestCertManagerCreatesTheSecretOnceAndRenews(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	now := time.Now()
	m := certManager(c, &now)
	assert.True(t, m.NeedLeaderElection())

	_, err := m.EnsureSecret(ctx)
	require.NoError(t, err)
	first := readSecret(t, c)
	assert.Equal(t, corev1.SecretTypeTLS, first.Type)
	for _, k := range []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey, extmetrics.KeyCACert, extmetrics.KeyCAKey} {
		assert.NotEmpty(t, first.Data[k], k)
	}

	_, err = m.EnsureSecret(ctx)
	require.NoError(t, err)
	assert.Equal(t, first.ResourceVersion, readSecret(t, c).ResourceVersion, "a valid Secret is left alone")

	now = now.Add(extmetrics.ServingValidity - 29*24*time.Hour)
	_, err = m.EnsureSecret(ctx)
	require.NoError(t, err)
	renewed := readSecret(t, c)
	assert.NotEqual(t, first.Data[corev1.TLSCertKey], renewed.Data[corev1.TLSCertKey], "under 30 days left: re-issued")
	assert.Equal(t, first.Data[extmetrics.KeyCACert], renewed.Data[extmetrics.KeyCACert], "the CA is kept")

	m.ServiceName = "clustarr-external-metrics"
	_, err = m.EnsureSecret(ctx)
	require.NoError(t, err)
	assert.Empty(t, extmetrics.RenewalReason(renewed.Data[extmetrics.KeyCACert], readSecret(t, c).Data[corev1.TLSCertKey],
		"clustarr-external-metrics", ns, now), "a renamed Service gets a certificate naming it")
}

func TestCertManagerReplacesAnUnusableCA(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	now := time.Now()
	m := certManager(c, &now)
	_, err := m.EnsureSecret(ctx)
	require.NoError(t, err)
	s := readSecret(t, c)
	s.Data[extmetrics.KeyCAKey] = []byte("lost")
	require.NoError(t, c.Update(ctx, s))

	_, err = m.EnsureSecret(ctx)
	require.NoError(t, err)
	fixed := readSecret(t, c)
	assert.False(t, extmetrics.CANeedsRenewal(fixed.Data[extmetrics.KeyCACert], fixed.Data[extmetrics.KeyCAKey], now))
	assert.Empty(t, extmetrics.RenewalReason(fixed.Data[extmetrics.KeyCACert], fixed.Data[corev1.TLSCertKey], "external-metrics", ns, now))
}
