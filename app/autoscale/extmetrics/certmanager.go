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

package extmetrics

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ReasonAPIServiceForeign is the Warning when another provider holds the
// group.
const ReasonAPIServiceForeign = "ExternalMetricsAPIServiceForeign"

// DefaultSyncInterval is CertManager's period.
const DefaultSyncInterval = 60 * time.Second

// CertManager keeps the TLS Secret and the APIService's caBundle (§9.4).
// It is leader-only.
type CertManager struct {
	Client      client.Client // writes
	Reader      client.Reader // the manager's APIReader
	Recorder    k8sevents.EventRecorder
	Namespace   string
	ServiceName string
	SecretName  string
	Interval    time.Duration // 0 means DefaultSyncInterval
	Now         func() time.Time
}

// NeedLeaderElection makes it a cluster singleton.
func (m *CertManager) NeedLeaderElection() bool { return true }

// Start syncs at once and every Interval. A failed sync is logged and
// retried; it never stops the manager.
func (m *CertManager) Start(ctx context.Context) error {
	interval := m.Interval
	if interval <= 0 {
		interval = DefaultSyncInterval
	}
	for {
		if err := m.Sync(ctx); err != nil && ctx.Err() == nil {
			logging.FromContext(ctx).Warn("extmetrics: keeping the TLS Secret and caBundle failed; retrying", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// Sync ensures the Secret, then the caBundle.
func (m *CertManager) Sync(ctx context.Context) error {
	sec, err := m.EnsureSecret(ctx)
	if err != nil {
		return err
	}
	return m.InjectCABundle(ctx, sec)
}

// EnsureSecret creates the Secret if absent (create-only: a racing
// creator wins, and the next sync reads its Secret), replaces a CA that is
// unusable or under 30 days from expiry with a fresh CA and pair, and
// re-issues a serving certificate RenewalReason rejects. Updates carry the
// read resourceVersion, so a concurrent writer is a Conflict redone at the
// next sync.
func (m *CertManager) EnsureSecret(ctx context.Context) (*corev1.Secret, error) {
	now := m.now()
	var sec corev1.Secret
	err := m.Reader.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.SecretName}, &sec)
	if apierrors.IsNotFound(err) {
		data, err := NewSecretData(m.ServiceName, m.Namespace, now)
		if err != nil {
			return nil, err
		}
		created := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: m.Namespace, Name: m.SecretName,
				Labels: map[string]string{"app.kubernetes.io/part-of": "clustarr", "app.kubernetes.io/component": "manager"}},
			Type: corev1.SecretTypeTLS, Data: data,
		}
		if err := m.Client.Create(ctx, created, client.FieldOwner(k8s.ManagerAutoscale.String())); err != nil {
			return nil, fmt.Errorf("extmetrics: create Secret %s/%s: %w", m.Namespace, m.SecretName, err)
		}
		return created, nil
	}
	if err != nil {
		return nil, fmt.Errorf("extmetrics: read Secret %s/%s: %w", m.Namespace, m.SecretName, err)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	switch {
	case CANeedsRenewal(sec.Data[KeyCACert], sec.Data[KeyCAKey], now):
		data, err := NewSecretData(m.ServiceName, m.Namespace, now)
		if err != nil {
			return nil, err
		}
		sec.Data = data
	case RenewalReason(sec.Data[KeyCACert], sec.Data[corev1.TLSCertKey], m.ServiceName, m.Namespace, now) != "":
		cert, key, err := IssueServing(sec.Data[KeyCACert], sec.Data[KeyCAKey], m.ServiceName, m.Namespace, now)
		if err != nil {
			return nil, err
		}
		sec.Data[corev1.TLSCertKey], sec.Data[corev1.TLSPrivateKeyKey] = cert, key
	default:
		return &sec, nil
	}
	if err := m.Client.Update(ctx, &sec, client.FieldOwner(k8s.ManagerAutoscale.String())); err != nil {
		return nil, fmt.Errorf("extmetrics: renew Secret %s/%s: %w", m.Namespace, m.SecretName, err)
	}
	return &sec, nil
}

// InjectCABundle applies spec.caBundle = ca.crt when the APIService names
// this manager's Service; otherwise it records ReasonAPIServiceForeign on
// the Secret. A missing APIService (autoscaling.enabled=false) is fine.
func (m *CertManager) InjectCABundle(ctx context.Context, sec *corev1.Secret) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(APIServiceGVK)
	if err := m.Reader.Get(ctx, types.NamespacedName{Name: APIServiceName}, u); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return fmt.Errorf("extmetrics: read APIService %s: %w", APIServiceName, err)
	}
	svcNS, _, _ := unstructured.NestedString(u.Object, "spec", "service", "namespace")
	svcName, _, _ := unstructured.NestedString(u.Object, "spec", "service", "name")
	if svcNS != m.Namespace || svcName != m.ServiceName {
		if m.Recorder != nil {
			m.Recorder.Eventf(sec, nil, corev1.EventTypeWarning, ReasonAPIServiceForeign, "InjectCABundle",
				"%s serves %s/%s, not this manager's %s/%s: another external-metrics provider holds the group, so no caBundle is injected (set autoscaling.enabled=false to run beside it)",
				APIServiceName, svcNS, svcName, m.Namespace, m.ServiceName)
		}
		return nil
	}
	ca := sec.Data[KeyCACert]
	if current, _, _ := unstructured.NestedString(u.Object, "spec", "caBundle"); current == base64.StdEncoding.EncodeToString(ca) {
		return nil
	}
	if _, err := k8s.Apply(ctx, m.Client, k8s.ManagerAutoscale, newAPIServiceCABundle(ca)); err != nil {
		return fmt.Errorf("extmetrics: inject the caBundle: %w", err)
	}
	return nil
}

func (m *CertManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
